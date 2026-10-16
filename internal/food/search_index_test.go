package food

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func vectorBlob(values ...float64) []byte {
	return encodeFloat32Blob(values)
}

func TestSearchWords(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "lowercases and splits on spaces", input: "Хлеб Белый", want: []string{"хлеб", "белый"}},
		{name: "folds ё into е", input: "Хлеб чёрный", want: []string{"хлеб", "черный"}},
		{name: "splits on punctuation", input: "Пшённая каша (молоко/вода)", want: []string{"пшенная", "каша", "молоко", "вода"}},
		{name: "splits a number on its separator", input: "Молоко 2,5%", want: []string{"молоко", "2", "5"}},
		{name: "empty", input: "  ", want: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchWords(tt.input)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("searchWords(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestStemSearchWord(t *testing.T) {
	tests := []struct {
		word string
		want string
	}{
		{word: "яблоко", want: "ябло"},
		{word: "сметана", want: "смета"},
		{word: "огурец", want: "огур"},
		{word: "яйцо", want: "яйц"},
		{word: "хлеб", want: "хле"},
		{word: "моло", want: "мол"},
		{word: "сыр", want: "сыр"},
		{word: "5", want: "5"},
		{word: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			if got := stemSearchWord(tt.word); got != tt.want {
				t.Fatalf("stemSearchWord(%q) = %q, want %q", tt.word, got, tt.want)
			}
		})
	}
}

// indexRow builds a search row whose name and description share one vector.
func indexRow(id int64, name string, archived bool, vector ...float64) SearchIndexRow {
	return SearchIndexRow{ID: id, Name: name, Archived: archived, NameVec: vectorBlob(vector...), DescriptionVec: vectorBlob(vector...)}
}

func TestSearchIndexRanking(t *testing.T) {
	queryVector := decodeUnitVector(vectorBlob(1, 0, 0))

	tests := []struct {
		name        string
		rows        []SearchIndexRow
		query       string
		queryVector []float32
		scope       SearchScope
		want        []int64
	}{
		{
			name: "a name word starting with the query lifts a product over a closer one without it",
			rows: []SearchIndexRow{
				indexRow(1, "Кефир", false, 0.6, 0.8, 0),
				indexRow(2, "Молоко", false, 0.5, 0.866, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{2, 1},
		},
		{
			name: "a query word inside another word gets no lift",
			rows: []SearchIndexRow{
				indexRow(1, "Кисломолочный напиток", false, 0.5, 0.866, 0),
				indexRow(2, "Молоко", false, 0.5, 0.866, 0),
			},
			query:       "моло",
			queryVector: queryVector,
			want:        []int64{2, 1},
		},
		{
			name: "any word of the name can start with the query word",
			rows: []SearchIndexRow{
				indexRow(1, "Каша овсяная", false, 0.5, 0.866, 0),
				indexRow(2, "Пшённая каша (молоко/вода)", false, 0.45, 0.893, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{2, 1},
		},
		{
			name: "every matched query word adds a lift, so two words beat one even over a closer product",
			rows: []SearchIndexRow{
				indexRow(1, "Молоко сгущённое", false, 0.5, 0.866, 0),
				indexRow(2, "Молоко", false, 0.7, 0.714, 0),
			},
			query:       "молоко сгущенное",
			queryVector: queryVector,
			want:        []int64{1, 2},
		},
		{
			name: "the product holding all query words comes before products holding one",
			rows: []SearchIndexRow{
				indexRow(1, "Хлеб белый", false, 0.5, 0.866, 0),
				indexRow(2, "Хлеб черный", false, 0.5, 0.866, 0),
				indexRow(3, "Белый рис", false, 0.5, 0.866, 0),
			},
			query:       "хлеб белый",
			queryVector: queryVector,
			want:        []int64{1, 3, 2},
		},
		{
			name: "ё and е are the same letter",
			rows: []SearchIndexRow{
				indexRow(1, "Хлеб черный", false, 0.5, 0.866, 0),
				indexRow(2, "Хлеб белый", false, 0.5, 0.866, 0),
			},
			query:       "чёрный",
			queryVector: queryVector,
			want:        []int64{1, 2},
		},
		{
			name: "a form of the word still matches by its stem",
			rows: []SearchIndexRow{
				indexRow(1, "Блины со сметаной", false, 0.5, 0.866, 0),
				indexRow(2, "Блины с вареньем", false, 0.5, 0.866, 0),
			},
			query:       "сметана",
			queryVector: queryVector,
			want:        []int64{1, 2},
		},
		{
			name: "equal scores are ordered by name",
			rows: []SearchIndexRow{
				indexRow(1, "Молоко 6%", false, 0.5, 0.866, 0),
				indexRow(2, "Молоко 2,5%", false, 0.5, 0.866, 0),
				indexRow(3, "Молоко 3,2%", false, 0.5, 0.866, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{2, 3, 1},
		},
		{
			name: "a product with no text match and no similarity is not a result",
			rows: []SearchIndexRow{
				indexRow(1, "Говядина", false, 0, 1, 0),
				indexRow(2, "Свинина", false, -0.5, 0.866, 0),
				indexRow(3, "Курица", false, 0.1, 0.995, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{3},
		},
		{
			name: "without a query vector only products with a text match remain",
			rows: []SearchIndexRow{
				indexRow(1, "Кефир", false, 1, 0, 0),
				indexRow(2, "Молоко", false, 0, 1, 0),
				indexRow(3, "Молоко сгущённое", false, 0, 0, 1),
			},
			query: "молоко",
			want:  []int64{2, 3},
		},
		{
			name: "a vector of another size than the query's is ignored",
			rows: []SearchIndexRow{
				indexRow(1, "Кефир", false, 1, 0),
				indexRow(2, "Молоко", false, 1, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{2},
		},
		{
			name: "the name vector alone counts when the description vector is missing",
			rows: []SearchIndexRow{
				{ID: 1, Name: "Кефир", NameVec: vectorBlob(0.5, 0.866, 0)},
				{ID: 2, Name: "Йогурт"},
			},
			query:       "молоко",
			queryVector: queryVector,
			want:        []int64{1},
		},
		{
			name: "the active scope skips archived products",
			rows: []SearchIndexRow{
				indexRow(1, "Молоко", false, 0.5, 0.866, 0),
				indexRow(2, "Молоко старое", true, 0.5, 0.866, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			scope:       SearchScopeActive,
			want:        []int64{1},
		},
		{
			name: "the archived scope returns only archived products",
			rows: []SearchIndexRow{
				indexRow(1, "Молоко", false, 0.5, 0.866, 0),
				indexRow(2, "Молоко старое", true, 0.5, 0.866, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			scope:       SearchScopeArchived,
			want:        []int64{2},
		},
		{
			name: "the all scope ranks active and archived products together",
			rows: []SearchIndexRow{
				indexRow(1, "Кефир", false, 0.6, 0.8, 0),
				indexRow(2, "Молоко старое", true, 0.5, 0.866, 0),
				indexRow(3, "Молоко", false, 0.5, 0.866, 0),
			},
			query:       "молоко",
			queryVector: queryVector,
			scope:       SearchScopeAll,
			want:        []int64{3, 2, 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index := newSearchIndex(tt.rows)

			got := index.search(searchWords(tt.query), tt.queryVector, tt.scope, searchWordLift)

			if !slices.Equal(got, tt.want) {
				t.Fatalf("search(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

func TestSearchIndexReturnsAtMostThirtyBestProducts(t *testing.T) {
	rows := make([]SearchIndexRow, 0, 40)
	for i := 1; i <= 40; i++ {
		rows = append(rows, indexRow(int64(i), fmt.Sprintf("Продукт %02d", i), false, 1, float64(i), 0))
	}
	index := newSearchIndex(rows)

	got := index.search(searchWords("молоко"), decodeUnitVector(vectorBlob(1, 0, 0)), SearchScopeActive, searchWordLift)

	if len(got) != searchResultLimit {
		t.Fatalf("len(search()) = %d, want %d", len(got), searchResultLimit)
	}
	if got[0] != 1 || got[len(got)-1] != 30 {
		t.Fatalf("search() = %v, want products 1..30 (the closest to the query)", got)
	}
}

func TestDecodeUnitVector(t *testing.T) {
	if got := decodeUnitVector(vectorBlob(3, 4)); len(got) != 2 || got[0] != 0.6 || got[1] != 0.8 {
		t.Fatalf("decodeUnitVector(3, 4) = %v, want [0.6 0.8]", got)
	}
	for name, blob := range map[string][]byte{"empty": nil, "not a whole float": {1, 2, 3}, "all zeros": vectorBlob(0, 0)} {
		if got := decodeUnitVector(blob); got != nil {
			t.Fatalf("decodeUnitVector(%s) = %v, want nil", name, got)
		}
	}
}

func TestSearchIndexHolderBuildsOncePerSnapshot(t *testing.T) {
	var holder searchIndexHolder
	var builds atomic.Int32
	build := func(context.Context) (*searchIndex, error) {
		builds.Add(1)
		time.Sleep(10 * time.Millisecond)
		return newSearchIndex(nil), nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := holder.Get(context.Background(), build); err != nil {
				t.Errorf("Get() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if got := builds.Load(); got != 1 {
		t.Fatalf("builds = %d, want 1 for concurrent first searches", got)
	}

	holder.Invalidate()
	if _, err := holder.Get(context.Background(), build); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("builds = %d, want 2 after Invalidate", got)
	}
}

func TestSearchIndexHolderDoesNotKeepFailedBuild(t *testing.T) {
	var holder searchIndexHolder
	fail := true
	build := func(context.Context) (*searchIndex, error) {
		if fail {
			return nil, errors.New("database is busy")
		}
		return newSearchIndex(nil), nil
	}

	if _, err := holder.Get(context.Background(), build); err == nil {
		t.Fatal("Get() error = nil, want the build error")
	}
	fail = false
	if _, err := holder.Get(context.Background(), build); err != nil {
		t.Fatalf("Get() error = %v, want a fresh successful build", err)
	}
}

// A build that read the catalogue before a write must not survive that write's Invalidate: Invalidate
// waits for the running build and then drops what it produced.
func TestSearchIndexHolderInvalidateDropsSnapshotOfRunningBuild(t *testing.T) {
	var holder searchIndexHolder
	var builds atomic.Int32
	buildStarted := make(chan struct{})
	releaseBuild := make(chan struct{})
	build := func(context.Context) (*searchIndex, error) {
		if builds.Add(1) == 1 {
			close(buildStarted)
			<-releaseBuild
		}
		return newSearchIndex(nil), nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := holder.Get(context.Background(), build); err != nil {
			t.Errorf("Get() error = %v", err)
		}
	}()
	<-buildStarted
	go func() {
		defer wg.Done()
		holder.Invalidate()
	}()
	time.Sleep(20 * time.Millisecond) // let Invalidate queue behind the running build
	close(releaseBuild)
	wg.Wait()

	if _, err := holder.Get(context.Background(), build); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("builds = %d, want 2: the snapshot of the build running during Invalidate must be dropped", got)
	}
}
