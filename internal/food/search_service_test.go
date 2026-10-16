package food

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sync/atomic"
	"testing"

	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"
)

// countingEmbeddingGenerator answers from a fixed table (an unknown text gets a zero vector that the
// index ignores), counts the calls and can be switched to fail like an unreachable OpenAI.
type countingEmbeddingGenerator struct {
	vectors map[string][]float64
	calls   atomic.Int32
	failing atomic.Bool
}

func (g *countingEmbeddingGenerator) GenerateEmbedding(_ context.Context, text string) ([]float64, error) {
	g.calls.Add(1)
	if g.failing.Load() {
		return nil, errors.New("openai is unreachable")
	}
	if vec, ok := g.vectors[text]; ok {
		return vec, nil
	}
	return []float64{0, 0, 0}, nil
}

func newSearchTestService(t *testing.T, generator EmbeddingGenerator) (*Service, *sql.DB) {
	t.Helper()

	db := openFoodTestDB(t)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetEmbeddingGenerator(generator)
	return service, db
}

func saveSearchProduct(t *testing.T, service *Service, operationID string, name string) int64 {
	t.Helper()

	entry, applied, err := service.SaveProduct(context.Background(), 1, operationID, nil, ProductInput{
		Name: name, Description: name, Kcals: 100, Protein: 10, Fat: 5, Carbs: 5, Fiber: 1,
	})
	if err != nil || !applied {
		t.Fatalf("SaveProduct(%q) error = %v applied = %v", name, err, applied)
	}
	return entry.ID
}

func searchNames(t *testing.T, service *Service, query string) []string {
	t.Helper()

	entries, err := service.SearchCatalogue(context.Background(), query)
	if err != nil {
		t.Fatalf("SearchCatalogue(%q) error = %v", query, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
	}
	return names
}

func TestSearchRanksByVectorWithNameWordStartLift(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Молоко": {1, 0, 0},
		"Кисломолочный напиток": {0.8, 0.6, 0},
		"Говядина":              {0, 1, 0},
		"моло":                  {1, 0, 0},
	}}
	service, _ := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-milk", "Молоко")
	saveSearchProduct(t, service, "op-kefir", "Кисломолочный напиток")
	saveSearchProduct(t, service, "op-beef", "Говядина")

	got := searchNames(t, service, "моло")

	want := []string{"Молоко", "Кисломолочный напиток"}
	if !slices.Equal(got, want) {
		t.Fatalf("SearchCatalogue() = %v, want %v: the product with a word starting with the query first, the unrelated beef absent", got, want)
	}
}

func TestSearchKeepsTheTypedYoInTheVectorCacheKey(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{"Хлеб черный": {1, 0, 0}, "хлеб чёрный": {1, 0, 0}}}
	service, db := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-bread", "Хлеб черный")

	got := searchNames(t, service, "Хлеб  ЧЁРНЫЙ")

	if !slices.Equal(got, []string{"Хлеб черный"}) {
		t.Fatalf("SearchCatalogue() = %v, want the product spelled with е found by a query spelled with ё", got)
	}
	var cachedKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foodSearchQueryEmbeddings WHERE query = 'хлеб чёрный'`).Scan(&cachedKeys); err != nil || cachedKeys != 1 {
		t.Fatalf("cached vector rows for the lowercased query = %d (err %v), want 1", cachedKeys, err)
	}
}

func TestSearchResultsCacheSkipsTheVectorOnRepeat(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{"Молоко": {1, 0, 0}, "молоко": {1, 0, 0}}}
	service, _ := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-milk", "Молоко")
	callsAfterSave := generator.calls.Load()

	searchNames(t, service, "молоко")
	searchNames(t, service, "молоко")

	if got := generator.calls.Load() - callsAfterSave; got != 1 {
		t.Fatalf("embedding calls for two identical searches = %d, want 1", got)
	}
}

func TestSearchFallsBackToNameWordsWhenTheVectorIsUnavailable(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Молоко": {1, 0, 0},
		"Кисломолочный напиток": {0.8, 0.6, 0},
		"моло": {1, 0, 0},
	}}
	service, _ := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-milk", "Молоко")
	saveSearchProduct(t, service, "op-kefir", "Кисломолочный напиток")
	generator.failing.Store(true)

	got := searchNames(t, service, "моло")
	if !slices.Equal(got, []string{"Молоко"}) {
		t.Fatalf("SearchCatalogue() with OpenAI down = %v, want only the product whose name word starts with the query", got)
	}

	generator.failing.Store(false)
	got = searchNames(t, service, "моло")
	if !slices.Equal(got, []string{"Молоко", "Кисломолочный напиток"}) {
		t.Fatalf("SearchCatalogue() after OpenAI is back = %v, want the vector ranking: the fallback result must not be cached", got)
	}
}

func TestSearchReplacesAnUnusableCachedQueryVector(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Молоко": {1, 0, 0},
		"Кисломолочный напиток": {0.8, 0.6, 0},
		"моло": {1, 0, 0},
	}}
	service, db := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-milk", "Молоко")
	saveSearchProduct(t, service, "op-kefir", "Кисломолочный напиток")
	if _, err := db.Exec(`INSERT INTO foodSearchQueryEmbeddings(query, embedding, hitCount, lastUsedAt, createdAt) VALUES ('моло', X'00000000', 1, 0, 0)`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	got := searchNames(t, service, "моло")

	if !slices.Equal(got, []string{"Молоко", "Кисломолочный напиток"}) {
		t.Fatalf("SearchCatalogue() = %v, want the vector ranking from a freshly generated vector", got)
	}
	var embedding []byte
	if err := db.QueryRow(`SELECT embedding FROM foodSearchQueryEmbeddings WHERE query = 'моло'`).Scan(&embedding); err != nil || decodeUnitVector(embedding) == nil {
		t.Fatalf("cached vector = %v (err %v), want the bad row replaced by a usable one", embedding, err)
	}
}

func TestSearchDoesNotCacheTheResultOfAnEmptyGeneratedVector(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Молоко": {1, 0, 0},
		"Кисломолочный напиток": {0.8, 0.6, 0},
		"моло": {0, 0, 0},
	}}
	service, db := newSearchTestService(t, generator)
	saveSearchProduct(t, service, "op-milk", "Молоко")
	saveSearchProduct(t, service, "op-kefir", "Кисломолочный напиток")

	if got := searchNames(t, service, "моло"); !slices.Equal(got, []string{"Молоко"}) {
		t.Fatalf("SearchCatalogue() with a zero vector = %v, want the name matches only", got)
	}
	var cachedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foodSearchQueryEmbeddings WHERE query = 'моло'`).Scan(&cachedRows); err != nil || cachedRows != 0 {
		t.Fatalf("cached rows for a zero vector = %d (err %v), want none", cachedRows, err)
	}

	generator.vectors["моло"] = []float64{1, 0, 0}
	if got := searchNames(t, service, "моло"); !slices.Equal(got, []string{"Молоко", "Кисломолочный напиток"}) {
		t.Fatalf("SearchCatalogue() after the vector became usable = %v, want the vector ranking: the first result must not be cached", got)
	}
}

func TestSearchReturnsADatabaseErrorOfTheVectorCache(t *testing.T) {
	service, db := newSearchTestService(t, &countingEmbeddingGenerator{})
	saveSearchProduct(t, service, "op-milk", "Молоко")
	if _, err := db.Exec(`DROP TABLE foodSearchQueryEmbeddings`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	if _, err := service.SearchCatalogue(context.Background(), "моло"); err == nil {
		t.Fatal("SearchCatalogue() error = nil, want the failed read of the vector cache to be reported, not hidden as a name-only result")
	}
}

func TestSearchDoesNotCacheTheResultWithoutAnEmbeddingGenerator(t *testing.T) {
	service, _ := newSearchTestService(t, nil)
	saveSearchProduct(t, service, "op-milk", "Молоко")

	if got := searchNames(t, service, "моло"); !slices.Equal(got, []string{"Молоко"}) {
		t.Fatalf("SearchCatalogue() = %v, want the name match", got)
	}

	index, err := service.searchIndex.Get(context.Background(), service.buildSearchIndex)
	if err != nil {
		t.Fatalf("searchIndex.Get() error = %v", err)
	}
	if _, cached := index.results.Get("моло"); cached {
		t.Fatal("the result built without a query vector is cached, want it recomputed on the next search")
	}
}

func TestSearchSeesEveryCatalogueChange(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Молоко": {1, 0, 0},
		"Груша":  {0, 0, 1},
		"Кефир":  {0.7, 0.7, 0},
		"молоко": {1, 0, 0},
		"груша":  {0, 0, 1},
		"кефир":  {0.7, 0.7, 0},
	}}
	service, _ := newSearchTestService(t, generator)
	ctx := context.Background()
	milkID := saveSearchProduct(t, service, "op-milk", "Молоко")
	if got := searchNames(t, service, "молоко"); !slices.Equal(got, []string{"Молоко"}) {
		t.Fatalf("SearchCatalogue() = %v, want [Молоко]", got)
	}

	t.Run("a created product appears", func(t *testing.T) {
		saveSearchProduct(t, service, "op-pear", "Груша")
		if got := searchNames(t, service, "груша"); !slices.Equal(got, []string{"Груша"}) {
			t.Fatalf("SearchCatalogue() = %v, want [Груша]", got)
		}
	})

	t.Run("a renamed product is found by its new name", func(t *testing.T) {
		if _, applied, err := service.SaveProduct(ctx, 1, "op-rename", &milkID, ProductInput{
			Name: "Кефир", Description: "Кефир", Kcals: 100, Protein: 10, Fat: 5, Carbs: 5, Fiber: 1,
		}); err != nil || !applied {
			t.Fatalf("SaveProduct(rename) error = %v applied = %v", err, applied)
		}
		if got := searchNames(t, service, "кефир"); !slices.Equal(got, []string{"Кефир"}) {
			t.Fatalf("SearchCatalogue(кефир) = %v, want [Кефир]", got)
		}
		if got := searchNames(t, service, "молоко"); slices.Contains(got, "Молоко") {
			t.Fatalf("SearchCatalogue(молоко) = %v, want the old name gone", got)
		}
	})

	t.Run("an archived product leaves the normal search", func(t *testing.T) {
		archived := true
		if _, _, err := service.SaveProduct(ctx, 1, "op-archive", &milkID, ProductInput{
			Name: "Кефир", Description: "Кефир", Kcals: 100, Protein: 10, Fat: 5, Carbs: 5, Fiber: 1, Archived: &archived,
		}); err == nil {
			t.Fatal("SaveProduct(archive) error = nil, want the admin-only error without an admin checker")
		}
		service.SetAdminChecker(fakeAdminChecker{1: true})
		if _, applied, err := service.SaveProduct(ctx, 1, "op-archive-admin", &milkID, ProductInput{
			Name: "Кефир", Description: "Кефир", Kcals: 100, Protein: 10, Fat: 5, Carbs: 5, Fiber: 1, Archived: &archived,
		}); err != nil || !applied {
			t.Fatalf("SaveProduct(archive) error = %v applied = %v", err, applied)
		}
		if got := searchNames(t, service, "кефир"); slices.Contains(got, "Кефир") {
			t.Fatalf("SearchCatalogue(кефир) = %v, want the archived product gone", got)
		}
		ids, applied, err := service.SearchCatalogueRealtime(ctx, 1, "кефир", SearchScopeArchived)
		if err != nil || applied != SearchScopeArchived || !slices.Contains(ids, milkID) {
			t.Fatalf("archive search = %v (applied %v, err %v), want it to contain the archived product", ids, applied, err)
		}
	})

	t.Run("a deleted product disappears", func(t *testing.T) {
		pear := searchEntryID(t, service, "груша")
		if _, applied, err := service.DeleteProduct(ctx, 1, "op-delete-pear", pear); err != nil || !applied {
			t.Fatalf("DeleteProduct() error = %v applied = %v", err, applied)
		}
		if got := searchNames(t, service, "груша"); slices.Contains(got, "Груша") {
			t.Fatalf("SearchCatalogue(груша) = %v, want the deleted product gone", got)
		}
	})
}

func searchEntryID(t *testing.T, service *Service, query string) int64 {
	t.Helper()

	entries, err := service.SearchCatalogue(context.Background(), query)
	if err != nil || len(entries) == 0 {
		t.Fatalf("SearchCatalogue(%q) = %v, error = %v, want a result", query, entries, err)
	}
	return entries[0].ID
}

func TestLabEmbeddingsRefreshTheSearchIndex(t *testing.T) {
	generator := &countingEmbeddingGenerator{vectors: map[string][]float64{
		"Груша": {0, 0, 1},
		"фрукт": {0, 0, 1},
	}}
	service, db := newSearchTestService(t, generator)
	if _, err := db.Exec(`INSERT INTO foodCatalogue(id, name, kcals, protein, fat, carbs, fiber, description) VALUES (10, 'Груша', 50, 0, 0, 12, 3, 'Груша')`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if got := searchNames(t, service, "фрукт"); len(got) != 0 {
		t.Fatalf("SearchCatalogue(фрукт) = %v, want nothing: the pear has no vector yet", got)
	}

	lab := NewLabService(service.repo, service, nil)
	if _, err := lab.GenerateEmbeddings(context.Background(), 5); err != nil {
		t.Fatalf("GenerateEmbeddings() error = %v", err)
	}

	if got := searchNames(t, service, "фрукт"); !slices.Equal(got, []string{"Груша"}) {
		t.Fatalf("SearchCatalogue(фрукт) after the lab filled the vectors = %v, want [Груша]", got)
	}
}
