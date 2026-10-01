package food

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/httpx/legacy"
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"
	wspkg "megaapp-back/internal/ws"

	"github.com/go-chi/chi/v5"
)

const (
	regularUserID  int64 = 1
	adminUserID    int64 = 2
	archivedItemID int64 = 3
)

type fakeAdminChecker map[int64]bool

func (f fakeAdminChecker) IsAdmin(_ context.Context, userID int64) (bool, error) {
	return f[userID], nil
}

func boolPtr(value bool) *bool {
	return &value
}

// newArchiveTestService seeds one archived product (id 3, "Old apple") next to the fixture's
// Apple (1) and Bread (2); user 2 is the only admin.
func newArchiveTestService(t *testing.T) (*Service, *sql.DB) {
	t.Helper()

	db := openFoodTestDB(t)
	if _, err := db.Exec(`
		INSERT INTO users(id, username, hashedPassword, isAdmin) VALUES (2, 'root', '', 1);
		INSERT INTO foodCatalogue(id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec, archived)
		VALUES (3, 'Old apple', 50, 1, 0, 10, 2, 'Fruit', 'Old apple', X'0000803F00000000', X'0000803F00000000', 1);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetEmbeddingGenerator(fakeEmbeddingGenerator{})
	service.SetAdminChecker(fakeAdminChecker{adminUserID: true})
	return service, db
}

func appleInput(archived *bool) ProductInput {
	return ProductInput{Name: "Apple", Kcals: 50, Protein: 1, Fat: 0, Carbs: 10, Fiber: 2, Description: "Fruit", Archived: archived}
}

func countDiaryEntries(t *testing.T, db *sql.DB, dateISO string) int {
	t.Helper()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foodDiary WHERE dateISO = ?`, dateISO).Scan(&count); err != nil {
		t.Fatalf("count diary entries: %v", err)
	}
	return count
}

func TestSearchArchiveMode(t *testing.T) {
	tests := []struct {
		name         string
		userID       int64
		archived     bool
		wantApplied  bool
		wantCatalogs []int64
	}{
		{name: "normal mode hides archived", userID: regularUserID, archived: false, wantApplied: false, wantCatalogs: []int64{1}},
		{name: "admin archive mode shows only archived", userID: adminUserID, archived: true, wantApplied: true, wantCatalogs: []int64{archivedItemID}},
		{name: "non-admin archive request falls back to normal", userID: regularUserID, archived: true, wantApplied: false, wantCatalogs: []int64{1}},
		{name: "admin normal mode hides archived", userID: adminUserID, archived: false, wantApplied: false, wantCatalogs: []int64{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _ := newArchiveTestService(t)

			ids, applied, err := service.SearchCatalogueRealtime(context.Background(), tt.userID, "apple-semantic", tt.archived)
			if err != nil {
				t.Fatalf("SearchCatalogueRealtime() error = %v", err)
			}
			if applied != tt.wantApplied {
				t.Fatalf("applied = %v, want %v", applied, tt.wantApplied)
			}
			if !slices.Equal(ids, tt.wantCatalogs) {
				t.Fatalf("ids = %v, want %v", ids, tt.wantCatalogs)
			}
		})
	}
}

func TestSearchWithoutAdminCheckerIgnoresArchiveMode(t *testing.T) {
	service, _ := newArchiveTestService(t)
	service.SetAdminChecker(nil)

	ids, applied, err := service.SearchCatalogueRealtime(context.Background(), adminUserID, "apple-semantic", true)
	if err != nil {
		t.Fatalf("SearchCatalogueRealtime() error = %v", err)
	}
	if applied || !slices.Equal(ids, []int64{1}) {
		t.Fatalf("ids = %v, applied = %v, want [1], false", ids, applied)
	}
}

func TestHTTPSearchHidesArchived(t *testing.T) {
	service, _ := newArchiveTestService(t)

	results, err := service.SearchCatalogue(context.Background(), "apple-semantic")
	if err != nil {
		t.Fatalf("SearchCatalogue() error = %v", err)
	}
	if len(results) != 1 || results[0].ID != 1 {
		t.Fatalf("results = %+v, want only Apple", results)
	}
}

func TestArchiveSearchBypassesResultsCache(t *testing.T) {
	service, _ := newArchiveTestService(t)
	ctx := context.Background()

	if _, _, err := service.SearchCatalogueRealtime(ctx, adminUserID, "apple-semantic", true); err != nil {
		t.Fatalf("archive search error = %v", err)
	}
	if cached, ok := service.searchCache.Get("apple-semantic"); ok {
		t.Fatalf("archive search wrote the results cache: %v", cached)
	}

	if _, _, err := service.SearchCatalogueRealtime(ctx, regularUserID, "apple-semantic", false); err != nil {
		t.Fatalf("normal search error = %v", err)
	}
	ids, applied, err := service.SearchCatalogueRealtime(ctx, adminUserID, "apple-semantic", true)
	if err != nil {
		t.Fatalf("archive search after cached normal search error = %v", err)
	}
	if !applied || !slices.Equal(ids, []int64{archivedItemID}) {
		t.Fatalf("archive search = %v (applied %v), want [3] (not the cached normal result)", ids, applied)
	}
	if cached, _ := service.searchCache.Get("apple-semantic"); !slices.Equal(cached, []int64{1}) {
		t.Fatalf("cached normal result = %v, want [1] untouched by archive search", cached)
	}
}

func TestCatalogueExposesArchivedFlag(t *testing.T) {
	service, _ := newArchiveTestService(t)

	catalogue, err := service.GetCatalogue(context.Background())
	if err != nil {
		t.Fatalf("GetCatalogue() error = %v", err)
	}
	if len(catalogue) != 3 || !catalogue[archivedItemID].Archived || catalogue[1].Archived {
		t.Fatalf("catalogue = %+v, want 3 entries with only #3 archived", catalogue)
	}

	entry, err := service.GetCatalogueEntry(context.Background(), archivedItemID)
	if err != nil {
		t.Fatalf("GetCatalogueEntry() error = %v", err)
	}
	if entry == nil || !entry.Archived {
		t.Fatalf("entry = %+v, want archived", entry)
	}
}

func TestCreateDiaryEntryProductChecks(t *testing.T) {
	tests := []struct {
		name      string
		productID int64
		wantErr   bool
	}{
		{name: "regular product", productID: 1},
		{name: "archived product", productID: archivedItemID, wantErr: true},
		{name: "missing product", productID: 999, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, db := newArchiveTestService(t)

			_, applied, err := service.CreateDiaryEntry(context.Background(), regularUserID, "op-create", "2026-06-20", tt.productID, 100, []HistoryEntry{{Action: "init", Value: 100}})
			if tt.wantErr {
				if legacy.ErrorKindOf(err) != legacy.ErrorKindValidation {
					t.Fatalf("error = %v, want validation error", err)
				}
				if applied || countDiaryEntries(t, db, "2026-06-20") != 0 {
					t.Fatal("entry was created for an unavailable product")
				}
				return
			}
			if err != nil || !applied {
				t.Fatalf("CreateDiaryEntry() = (applied=%v, err=%v), want success", applied, err)
			}
		})
	}
}

func TestCreateDiaryEntryReplayAfterProductArchived(t *testing.T) {
	service, _ := newArchiveTestService(t)
	ctx := context.Background()
	history := []HistoryEntry{{Action: "init", Value: 100}}

	created, applied, err := service.CreateDiaryEntry(ctx, regularUserID, "op-create", "2026-06-20", 1, 100, history)
	if err != nil || !applied {
		t.Fatalf("CreateDiaryEntry() = (applied=%v, err=%v), want success", applied, err)
	}
	productID := int64(1)
	if _, _, err := service.SaveProduct(ctx, adminUserID, "op-archive", &productID, appleInput(boolPtr(true))); err != nil {
		t.Fatalf("SaveProduct() archive error = %v", err)
	}

	replayed, applied, err := service.CreateDiaryEntry(ctx, regularUserID, "op-create", "2026-06-20", 1, 100, history)
	if err != nil {
		t.Fatalf("CreateDiaryEntry() replay error = %v, want the stored result", err)
	}
	if applied || replayed.ID != created.ID {
		t.Fatalf("replay = (applied=%v, id=%d), want (false, %d)", applied, replayed.ID, created.ID)
	}
}

func TestRestoreDiaryEntriesForDayProductChecks(t *testing.T) {
	tests := []struct {
		name      string
		productID int64
		wantErr   bool
	}{
		{name: "archived product is restored", productID: archivedItemID},
		{name: "missing product is rejected", productID: 999, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, db := newArchiveTestService(t)

			restored, applied, err := service.RestoreDiaryEntriesForDay(context.Background(), regularUserID, "op-restore", "2026-06-21", []RestoreDiaryEntryInput{
				{FoodCatalogueID: 1, FoodWeight: 50},
				{FoodCatalogueID: tt.productID, FoodWeight: 120},
			})
			if tt.wantErr {
				if legacy.ErrorKindOf(err) != legacy.ErrorKindValidation {
					t.Fatalf("error = %v, want validation error", err)
				}
				if applied || countDiaryEntries(t, db, "2026-06-21") != 0 {
					t.Fatal("restore left entries behind after a rejected product")
				}
				return
			}
			if err != nil || !applied || len(restored) != 2 {
				t.Fatalf("RestoreDiaryEntriesForDay() = (restored=%d, applied=%v, err=%v), want 2 entries", len(restored), applied, err)
			}
		})
	}
}

func TestSaveProductArchiveFlag(t *testing.T) {
	service, _ := newArchiveTestService(t)
	ctx := context.Background()
	productID := int64(1)

	archive, applied, err := service.SaveProduct(ctx, adminUserID, "op-archive", &productID, appleInput(boolPtr(true)))
	if err != nil || !applied || !archive.Archived {
		t.Fatalf("admin archive = (%+v, applied=%v, err=%v), want archived", archive, applied, err)
	}

	for _, flag := range []bool{true, false} {
		_, applied, err := service.SaveProduct(ctx, regularUserID, "op-denied", &productID, appleInput(boolPtr(flag)))
		if legacy.ErrorKindOf(err) != legacy.ErrorKindForbidden || applied {
			t.Fatalf("non-admin with archived=%v: err = %v, applied = %v, want forbidden", flag, err, applied)
		}
	}
	if _, _, err := service.SaveProduct(ctx, regularUserID, "op-archive", &productID, appleInput(boolPtr(true))); legacy.ErrorKindOf(err) != legacy.ErrorKindForbidden {
		t.Fatalf("non-admin replay of an applied operation: err = %v, want forbidden before the idempotency check", err)
	}

	renamed := appleInput(nil)
	renamed.Description = "Fruit, edited by a regular user"
	edited, applied, err := service.SaveProduct(ctx, regularUserID, "op-edit", &productID, renamed)
	if err != nil || !applied {
		t.Fatalf("non-admin edit without the flag: applied = %v, err = %v", applied, err)
	}
	if !edited.Archived || edited.Description != renamed.Description {
		t.Fatalf("edited = %+v, want description changed and archive flag untouched", edited)
	}

	restored, _, err := service.SaveProduct(ctx, adminUserID, "op-unarchive", &productID, appleInput(boolPtr(false)))
	if err != nil || restored.Archived {
		t.Fatalf("admin unarchive = (%+v, err=%v), want not archived", restored, err)
	}
}

func TestSaveProductCreateDefaultsToNotArchived(t *testing.T) {
	service, _ := newArchiveTestService(t)

	created, applied, err := service.SaveProduct(context.Background(), regularUserID, "op-create", nil, ProductInput{Name: "Kiwi", Kcals: 61, Protein: 1, Fat: 1, Carbs: 15, Fiber: 3, Description: "Kiwi fruit"})
	if err != nil || !applied || created.Archived {
		t.Fatalf("create = (%+v, applied=%v, err=%v), want not archived", created, applied, err)
	}
}

func TestSaveProductBumpsCatalogueVersion(t *testing.T) {
	service, _ := newArchiveTestService(t)
	productID := int64(1)
	before := service.CatalogueVersion()

	if _, _, err := service.SaveProduct(context.Background(), adminUserID, "op-archive", &productID, appleInput(boolPtr(true))); err != nil {
		t.Fatalf("SaveProduct() error = %v", err)
	}
	if after := service.CatalogueVersion(); after != before+1 {
		t.Fatalf("version = %d, want %d", after, before+1)
	}
}

func TestCatalogueVersionNeverRewindsAfterRestart(t *testing.T) {
	first := NewCatalogueCache()
	for range 3 {
		first.Invalidate()
	}
	time.Sleep(10 * time.Millisecond)

	if restarted := NewCatalogueCache(); restarted.Version() <= first.Version() {
		t.Fatalf("version after restart = %d, want > %d", restarted.Version(), first.Version())
	}
}

func TestFoodArchiveOverHTTPAndWS(t *testing.T) {
	service, db := newArchiveTestService(t)
	authService := auth.NewService(auth.NewRepository(db, sqlite.WriteDB{DB: db}), auth.SessionConfig{})
	service.SetAdminChecker(authService)
	hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
	defer func() { _ = hub.Close() }()
	clk := clockplatform.NewRealClock()
	realtime := NewWSRealtimePublisher(hub, clk)
	hub.RegisterHandler("SEARCH_QUERY", NewSearchWSHandler(service, clk))

	router := chi.NewRouter()
	RegisterCatalogueRoutes(router, authService, NewCatalogueHandler(service, realtime, &fakeMetricsRecorder{}))
	wspkg.RegisterRoutes(router, wspkg.NewHandler(authService, hub))
	server := httptest.NewServer(router)
	defer server.Close()

	regular, err := authService.CreateSession(t.Context(), regularUserID)
	if err != nil {
		t.Fatalf("CreateSession() regular error = %v", err)
	}
	admin, err := authService.CreateSession(t.Context(), adminUserID)
	if err != nil {
		t.Fatalf("CreateSession() admin error = %v", err)
	}

	save := func(cookie string, operationID string, wantStatus int) {
		assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/save-product", cookie, "tab", map[string]any{
			"operationId": operationID, "id": 1, "name": "Apple", "kcals": 50, "protein": 1, "fat": 0, "carbs": 10, "fiber": 2,
			"description": "Fruit", "archived": true,
		}, wantStatus)
	}
	// 403, not 401: the client logs the user out on 401.
	save(regular.Cookie, "op-regular", http.StatusForbidden)
	save(admin.Cookie, "op-admin", http.StatusOK)

	search := func(cookie string, archived bool) map[string]any {
		conn := dialFoodWS(t, server.URL+"/api/ws?clientId=search", cookie)
		defer func() { _ = conn.Close() }()
		drainFoodWSMessage(t, conn)
		if err := conn.WriteJSON(map[string]any{"type": "SEARCH_QUERY", "query": "apple-semantic", "archived": archived, "sequenceNumber": 1}); err != nil {
			t.Fatalf("WriteJSON() error = %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("ReadJSON() error = %v", err)
		}
		return message["payload"].(map[string]any)
	}

	adminPayload := search(admin.Cookie, true)
	if adminPayload["archived"] != true || len(adminPayload["catalogueIds"].([]any)) != 2 {
		t.Fatalf("admin archive search payload = %v, want archived=true with products 1 and 3", adminPayload)
	}
	regularPayload := search(regular.Cookie, true)
	if regularPayload["archived"] != false || len(regularPayload["catalogueIds"].([]any)) != 0 {
		t.Fatalf("non-admin archive search payload = %v, want normal mode without archived products", regularPayload)
	}
}
