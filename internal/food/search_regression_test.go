package food

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"megaapp-back/internal/platform/sqlite"
)

// productionCopyPath is the local copy of the production database. The regression below reads it
// without writing (immutable), takes the real queries people typed from the query vector cache table
// and ranks them over the real catalogue, so it needs no OpenAI. Without the file it is skipped.
const productionCopyPath = "../../data/megaapp-prod.db"

const regressionQueryCount = 600

type regressionQuery struct {
	text   string
	words  []string
	vector []float32
}

func openProductionCopy(t *testing.T) *sql.DB {
	t.Helper()

	path, err := filepath.Abs(productionCopyPath)
	if err != nil {
		t.Fatalf("Abs() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("production database copy is not available: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// popularCyrillicQueries returns the most used queries that contain a Cyrillic letter (the front
// retypes forgotten layouts, so Latin queries never reach the server) together with their vectors.
func popularCyrillicQueries(t *testing.T, db *sql.DB) []regressionQuery {
	t.Helper()

	rows, err := db.Query(`SELECT query, embedding FROM foodSearchQueryEmbeddings ORDER BY hitCount DESC`)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	defer rows.Close()

	queries := make([]regressionQuery, 0, regressionQueryCount)
	for rows.Next() && len(queries) < regressionQueryCount {
		var text string
		var blob []byte
		if err := rows.Scan(&text, &blob); err != nil {
			t.Fatalf("Scan() error = %v", err)
		}
		if !strings.ContainsAny(text, "абвгдеёжзийклмнопрстуфхцчшщъыьэюя") {
			continue
		}
		if vector := decodeUnitVector(blob); vector != nil {
			queries = append(queries, regressionQuery{text: text, words: searchWords(text), vector: vector})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error = %v", err)
	}
	return queries
}

// TestSearchRegressionOnProductionCopy ranks the real popular queries over the real catalogue. For a
// query of four letters or more (a whole word, not a typing prefix) the first result must have a name
// word starting with the query whenever the catalogue has such a product. Run it with -v to see how
// the size of the per-word lift changes the lists.
func TestSearchRegressionOnProductionCopy(t *testing.T) {
	db := openProductionCopy(t)
	rows, err := NewRepository(db, sqlite.WriteDB{DB: db}).GetSearchIndexRows(context.Background())
	if err != nil {
		t.Fatalf("GetSearchIndexRows() error = %v", err)
	}
	queries := popularCyrillicQueries(t, db)
	if len(queries) == 0 {
		t.Skip("no cached query vectors in the production copy")
	}
	index := newSearchIndex(rows)
	entriesByID := make(map[int64]*searchIndexEntry, len(index.entries))
	for i := range index.entries {
		entriesByID[index.entries[i].id] = &index.entries[i]
	}

	// measure returns how many whole-word queries have a product with a matching name word, how many of
	// them rank such a product first, and how many matching products the top 5 hold in total.
	measure := func(wordLift float64) (checked int, topMatches int, top5Matches int) {
		for _, query := range queries {
			if utf8.RuneCountInString(query.text) < 4 || len(query.words) == 0 {
				continue
			}
			stems := make([]string, 0, len(query.words))
			for _, word := range query.words {
				stems = append(stems, stemSearchWord(word))
			}
			hasMatchingProduct := false
			for i := range index.entries {
				if !index.entries[i].archived && index.entries[i].matchedWords(stems) > 0 {
					hasMatchingProduct = true
					break
				}
			}
			if !hasMatchingProduct {
				continue
			}

			checked++
			ids := index.search(query.words, query.vector, SearchScopeActive, wordLift)
			for rank, id := range ids[:min(5, len(ids))] {
				if entriesByID[id].matchedWords(stems) > 0 {
					top5Matches++
					if rank == 0 {
						topMatches++
					}
				}
			}
		}
		return checked, topMatches, top5Matches
	}

	for _, wordLift := range []float64{0, 0.05, 0.1, 0.15, searchWordLift, 0.5} {
		checked, topMatches, top5Matches := measure(wordLift)
		t.Logf("lift %.2f: the first result has a name word starting with the query in %d of %d queries; matching products in the top 5: %.2f on average",
			wordLift, topMatches, checked, float64(top5Matches)/float64(max(checked, 1)))
	}

	checked, topMatches, _ := measure(searchWordLift)
	if checked == 0 {
		t.Skip("no whole-word queries with a matching product in the production copy")
	}
	if share := float64(topMatches) / float64(checked); share < 0.99 {
		t.Fatalf("with lift %.2f the first result matches the query in %d of %d queries (%.1f%%), want at least 99%%", searchWordLift, topMatches, checked, share*100)
	}

	for _, query := range queries[:30] {
		ids := index.search(query.words, query.vector, SearchScopeActive, searchWordLift)
		top := make([]string, 0, 5)
		for _, id := range ids[:min(5, len(ids))] {
			top = append(top, entriesByID[id].name)
		}
		t.Logf("%q → %s", query.text, strings.Join(top, " | "))
	}
}
