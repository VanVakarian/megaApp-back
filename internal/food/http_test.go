package food

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"megaapp-back/internal/auth"
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"
	wspkg "megaapp-back/internal/ws"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type fakeMetricsRecorder struct {
	counts map[string]int
}

func (f *fakeMetricsRecorder) Increment(name string) {
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	f.counts[name]++
}

func TestFoodWriteEndpointsAndWebSocketBroadcasts(t *testing.T) {
	db := openFoodTestDB(t)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetProductGenerator(fakeProductGenerator{})
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.June, 20, 12, 0, 0, 0, time.UTC)})
	seedFoodDiaryAndWeightHistory(t, db, 1)
	hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
	defer func() { _ = hub.Close() }()
	clk := clockplatform.NewRealClock()
	realtime := NewWSRealtimePublisher(hub, clk)
	readHandler := NewHandler(service, realtime)
	hub.RegisterHandler("SEARCH_QUERY", NewSearchWSHandler(service, clk))
	writeHandler := NewWriteHandler(service, realtime, &fakeMetricsRecorder{})
	catalogueHandler := NewCatalogueHandler(service, realtime, &fakeMetricsRecorder{})
	wsHandler := wspkg.NewHandler(authService, hub)

	router := chi.NewRouter()
	RegisterRoutes(router, authService, readHandler)
	RegisterWriteRoutes(router, authService, writeHandler)
	RegisterCatalogueRoutes(router, authService, catalogueHandler)
	wspkg.RegisterRoutes(router, wsHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	connA := dialFoodWS(t, server.URL+"/api/ws?clientId=tab-a", session.Cookie)
	defer func() { _ = connA.Close() }()
	connB := dialFoodWS(t, server.URL+"/api/ws?clientId=tab-b", session.Cookie)
	defer func() { _ = connB.Close() }()
	drainFoodWSMessage(t, connA)
	drainFoodWSMessage(t, connB)

	createBody := map[string]any{
		"operationId":     "op-create",
		"dateISO":         "2026-06-18",
		"foodCatalogueId": 1,
		"foodWeight":      120,
		"history":         []map[string]any{{"action": "init", "value": 120}},
	}
	assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/diary/", session.Cookie, "tab-a", createBody, http.StatusCreated)

	_ = connA.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var senderMessage map[string]any
	if err := connA.ReadJSON(&senderMessage); err == nil {
		t.Fatalf("sender received unexpected ws message: %+v", senderMessage)
	}

	_ = connB.SetReadDeadline(time.Now().Add(time.Second))
	var receiverMessage map[string]any
	if err := connB.ReadJSON(&receiverMessage); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if receiverMessage["type"] != "DIARY_ENTRY_CREATED" {
		t.Fatalf("ws type = %v, want DIARY_ENTRY_CREATED", receiverMessage["type"])
	}

	assertJSONRequestStatus(t, http.MethodPut, server.URL+"/api/food/diary", session.Cookie, "tab-a", map[string]any{
		"operationId":     "op-edit",
		"id":              10,
		"foodCatalogueId": 2,
		"foodWeight":      80,
		"historyAction":   "set",
	}, http.StatusOK)
	_ = connB.SetReadDeadline(time.Now().Add(time.Second))
	var updatedMessage map[string]any
	if err := connB.ReadJSON(&updatedMessage); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if updatedMessage["type"] != "DIARY_ENTRY_UPDATED" {
		t.Fatalf("ws type = %v, want DIARY_ENTRY_UPDATED", updatedMessage["type"])
	}
	if payload, ok := updatedMessage["payload"].(map[string]any); !ok || payload["version"] != float64(1) {
		t.Fatalf("updatedMessage payload version = %v, want 1", updatedMessage["payload"])
	}

	assertJSONRequestStatus(t, http.MethodDelete, server.URL+"/api/food/diary/10", session.Cookie, "tab-a", map[string]any{"operationId": "op-delete"}, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/body-weight", session.Cookie, "tab-a", map[string]any{
		"operationId": "op-weight",
		"dateISO":     "2026-06-18",
		"bodyWeight":  "81.0",
	}, http.StatusCreated)
	assertJSONRequestStatus(t, http.MethodDelete, server.URL+"/api/food/diary/day/2026-06-18", session.Cookie, "tab-a", map[string]any{"operationId": "op-day-delete"}, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/diary/day/2026-06-18/restore", session.Cookie, "tab-a", map[string]any{
		"operationId": "op-restore",
		"entries": []map[string]any{{
			"foodCatalogueId": 1,
			"foodWeight":      120,
			"history":         []map[string]any{{"action": "init", "value": 120}},
		}},
	}, http.StatusCreated)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/personal-kcals", session.Cookie, "tab-a", nil, http.StatusOK)
}

func TestFoodDiaryEditRetryIsIdempotentOverHTTPAndWS(t *testing.T) {
	db := openFoodTestDB(t)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
	defer func() { _ = hub.Close() }()
	clk := clockplatform.NewRealClock()
	realtime := NewWSRealtimePublisher(hub, clk)
	writeHandler := NewWriteHandler(service, realtime, &fakeMetricsRecorder{})
	wsHandler := wspkg.NewHandler(authService, hub)

	router := chi.NewRouter()
	RegisterWriteRoutes(router, authService, writeHandler)
	wspkg.RegisterRoutes(router, wsHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	connB := dialFoodWS(t, server.URL+"/api/ws?clientId=tab-b", session.Cookie)
	defer func() { _ = connB.Close() }()
	drainFoodWSMessage(t, connB)

	editBody := map[string]any{
		"operationId":     "op-retry",
		"id":              10,
		"foodCatalogueId": 2,
		"foodWeight":      80,
		"historyAction":   "subtract",
	}

	first := decodeJSONRequest(t, http.MethodPut, server.URL+"/api/food/diary", session.Cookie, "tab-a", editBody, http.StatusOK)
	if first["appliedHistoryEntry"] == nil {
		t.Fatalf("first response appliedHistoryEntry = %v, want a real entry", first["appliedHistoryEntry"])
	}
	if first["version"] != float64(1) {
		t.Fatalf("first response version = %v, want 1", first["version"])
	}
	_ = connB.SetReadDeadline(time.Now().Add(time.Second))
	var updatedMessage map[string]any
	if err := connB.ReadJSON(&updatedMessage); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if updatedMessage["type"] != "DIARY_ENTRY_UPDATED" {
		t.Fatalf("ws type = %v, want DIARY_ENTRY_UPDATED", updatedMessage["type"])
	}

	retry := decodeJSONRequest(t, http.MethodPut, server.URL+"/api/food/diary", session.Cookie, "tab-a", editBody, http.StatusOK)
	if retry["appliedHistoryEntry"] == nil {
		t.Fatalf("retry response appliedHistoryEntry = %v, want the original applied entry echoed back (same operationId)", retry["appliedHistoryEntry"])
	}
	if retry["version"] != first["version"] {
		t.Fatalf("retry response version = %v, want %v (replay echoes cached version)", retry["version"], first["version"])
	}
	_ = connB.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var unexpectedMessage map[string]any
	if err := connB.ReadJSON(&unexpectedMessage); err == nil {
		t.Fatalf("received unexpected ws message on retry: %+v", unexpectedMessage)
	}
}

func TestFoodSearchAndCatalogueMutationEndpoints(t *testing.T) {
	db := openFoodTestDB(t)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetProductGenerator(fakeProductGenerator{})
	hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
	defer func() { _ = hub.Close() }()
	clk := clockplatform.NewRealClock()
	realtime := NewWSRealtimePublisher(hub, clk)
	readHandler := NewHandler(service, realtime)
	hub.RegisterHandler("SEARCH_QUERY", NewSearchWSHandler(service, clk))
	catalogueHandler := NewCatalogueHandler(service, realtime, &fakeMetricsRecorder{})
	wsHandler := wspkg.NewHandler(authService, hub)

	router := chi.NewRouter()
	RegisterRoutes(router, authService, readHandler)
	RegisterCatalogueRoutes(router, authService, catalogueHandler)
	wspkg.RegisterRoutes(router, wsHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	connA := dialFoodWS(t, server.URL+"/api/ws?clientId=tab-a", session.Cookie)
	defer func() { _ = connA.Close() }()
	connB := dialFoodWS(t, server.URL+"/api/ws?clientId=tab-b", session.Cookie)
	defer func() { _ = connB.Close() }()
	drainFoodWSMessage(t, connA)
	drainFoodWSMessage(t, connB)

	if err := connA.WriteJSON(map[string]any{"type": "SEARCH_QUERY", "query": "apple-semantic", "sequenceNumber": 1}); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	_ = connA.SetReadDeadline(time.Now().Add(time.Second))
	var searchMessage map[string]any
	if err := connA.ReadJSON(&searchMessage); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if searchMessage["type"] != "SEARCH_RESULTS" {
		t.Fatalf("type = %v, want SEARCH_RESULTS", searchMessage["type"])
	}

	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/search?query=apple-semantic", session.Cookie, "tab-a", nil, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/generate-product-preview", session.Cookie, "tab-a", map[string]any{"description": "apple-semantic"}, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/save-product", session.Cookie, "tab-a", map[string]any{
		"operationId": "op-save-orange",
		"name":        "Orange",
		"kcals":       47,
		"protein":     1,
		"fat":         0,
		"carbs":       12,
		"fiber":       2,
		"description": "Orange fruit",
	}, http.StatusCreated)
	_ = connB.SetReadDeadline(time.Now().Add(time.Second))
	var savedMessage map[string]any
	if err := connB.ReadJSON(&savedMessage); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if savedMessage["type"] != "CATALOGUE_ENTRY_SAVED" {
		t.Fatalf("type = %v, want CATALOGUE_ENTRY_SAVED", savedMessage["type"])
	}
	assertJSONRequestStatus(t, http.MethodDelete, server.URL+"/api/food/catalogue/3", session.Cookie, "tab-a", map[string]any{
		"operationId": "op-delete-orange",
	}, http.StatusOK)
}

func TestFoodImageStaticRoutes(t *testing.T) {
	publicDir := t.TempDir()
	store, err := NewImageStore(publicDir)
	if err != nil {
		t.Fatalf("NewImageStore() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(publicDir, "images", "food", "1-thumb-v3.webp"), []byte("thumb"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	router := chi.NewRouter()
	RegisterImageRoutes(router, NewImageHandler(store))
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/images/food/1-thumb-v3.webp")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	response, err = http.Get(server.URL + "/api/images/food/../secret.txt")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestFoodDebugRunPersonalKcalJobRoute(t *testing.T) {
	db := openFoodTestDB(t)
	seedFoodDiaryAndWeightHistory(t, db, 1)
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.July, 5, 12, 0, 0, 0, time.UTC)})
	service.SetPersonalKcalConfig(testPersonalKcalConfig())
	debugHandler := NewDebugHandler(NewDebugService(NewRepository(db, sqlite.WriteDB{DB: db}), t.TempDir(), nil, service, nil))

	router := chi.NewRouter()
	RegisterDebugRoutes(router, debugHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := http.Get(server.URL + "/api/debug/run-personal-kcal-job")
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	resetResponse, err := http.Post(server.URL+"/api/debug/reset-personal-kcal/1", "application/json", nil)
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resetResponse.Body.Close()
	if resetResponse.StatusCode != http.StatusOK {
		t.Fatalf("reset status = %d, want 200", resetResponse.StatusCode)
	}

	resetAllResponse, err := http.Post(server.URL+"/api/debug/reset-personal-kcal-all", "application/json", nil)
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer resetAllResponse.Body.Close()
	if resetAllResponse.StatusCode != http.StatusOK {
		t.Fatalf("reset-all status = %d, want 200", resetAllResponse.StatusCode)
	}
}

func TestFoodReadEndpoints(t *testing.T) {
	db := openFoodTestDB(t)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetProductGenerator(fakeProductGenerator{})
	handler := NewHandler(service, nil)
	hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
	defer func() { _ = hub.Close() }()
	clk := clockplatform.NewRealClock()
	realtime := NewWSRealtimePublisher(hub, clk)
	catalogueHandler := NewCatalogueHandler(service, realtime, &fakeMetricsRecorder{})

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	RegisterCatalogueRoutes(router, authService, catalogueHandler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/catalogue", session.Cookie, "tab-a", nil, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/personal-kcals", session.Cookie, "tab-a", nil, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/stats", session.Cookie, "tab-a", nil, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/search?query=apple-semantic", session.Cookie, "tab-a", nil, http.StatusOK)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/diary-full-update?date=2026-06-17&offset=1", session.Cookie, "tab-a", nil, http.StatusOK)

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/food/catalogue/1", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session.Cookie})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if payload["result"] != true {
		t.Fatalf("result = %v, want true", payload["result"])
	}
}

func TestGetStatsHTTPResponseShape(t *testing.T) {
	db := openFoodTestDB(t)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	service.SetClock(fixedFoodClock{now: time.Date(2026, time.June, 18, 12, 0, 0, 0, time.UTC)})
	handler := NewHandler(service, nil)

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	decoded := decodeJSONRequest(t, http.MethodGet, server.URL+"/api/food/stats", session.Cookie, "tab-a", nil, http.StatusOK)

	days, ok := decoded["days"].(map[string]any)
	if !ok || len(days) == 0 {
		t.Fatalf("days = %v, want non-empty object", decoded["days"])
	}
	dayEntry, ok := days["2026-06-17"].(map[string]any)
	if !ok {
		t.Fatalf("days missing 2026-06-17: %+v", days)
	}
	for _, key := range []string{"weight", "weightAvg", "consumedKcal", "targetKcal", "hasNoData"} {
		if _, ok := dayEntry[key]; !ok {
			t.Fatalf("day entry missing %q: %+v", key, dayEntry)
		}
	}

	topProductsByKcal, ok := decoded["topProductsByKcal"].([]any)
	if !ok || len(topProductsByKcal) != 1 {
		t.Fatalf("topProductsByKcal = %v, want single-element array", decoded["topProductsByKcal"])
	}
	product, ok := topProductsByKcal[0].(map[string]any)
	if !ok {
		t.Fatalf("topProductsByKcal[0] not an object: %+v", topProductsByKcal[0])
	}
	if product["catalogueId"] != float64(2) || product["name"] != "Bread" || product["kcal"] != float64(250) || product["weight"] != float64(100) {
		t.Fatalf("topProductsByKcal[0] = %+v, want {catalogueId:2 name:Bread kcal:250 weight:100}", product)
	}

	topProductsByWeight, ok := decoded["topProductsByWeight"].([]any)
	if !ok || len(topProductsByWeight) != 1 {
		t.Fatalf("topProductsByWeight = %v, want single-element array", decoded["topProductsByWeight"])
	}
	weightProduct, ok := topProductsByWeight[0].(map[string]any)
	if !ok {
		t.Fatalf("topProductsByWeight[0] not an object: %+v", topProductsByWeight[0])
	}
	if weightProduct["catalogueId"] != float64(2) || weightProduct["name"] != "Bread" || weightProduct["kcal"] != float64(250) || weightProduct["weight"] != float64(100) {
		t.Fatalf("topProductsByWeight[0] = %+v, want {catalogueId:2 name:Bread kcal:250 weight:100}", weightProduct)
	}

	if decoded["topProductsWindowTotalKcal"] != float64(250) {
		t.Fatalf("topProductsWindowTotalKcal = %v, want 250", decoded["topProductsWindowTotalKcal"])
	}
	if decoded["topProductsWindowTotalWeight"] != float64(100) {
		t.Fatalf("topProductsWindowTotalWeight = %v, want 100", decoded["topProductsWindowTotalWeight"])
	}

	if decoded["totalEntries"] != float64(1) {
		t.Fatalf("totalEntries = %v, want 1", decoded["totalEntries"])
	}
}

func TestGetProductHistoryHTTPResponseShapeAndPagination(t *testing.T) {
	db := openFoodTestDB(t)
	seedProductHistoryRows(t, db)
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	service := NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}))
	handler := NewHandler(service, nil)

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	session, err := authService.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}

	decoded := decodeJSONRequest(t, http.MethodGet, server.URL+"/api/food/product-history?catalogueIds=1&limit=2", session.Cookie, "tab-a", nil, http.StatusOK)
	if decoded["result"] != true {
		t.Fatalf("result = %v, want true", decoded["result"])
	}
	data, ok := decoded["data"].(map[string]any)
	if !ok {
		t.Fatalf("data not an object: %+v", decoded)
	}
	entries, ok := data["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %v, want 2-element array", data["entries"])
	}
	first, ok := entries[0].(map[string]any)
	if !ok || first["dateISO"] != "2026-06-19" || first["foodCatalogueId"] != float64(1) || first["foodWeight"] != float64(440) || first["percentOfNorm"] != float64(10) {
		t.Fatalf("entries[0] = %+v, want {dateISO:2026-06-19 foodCatalogueId:1 foodWeight:440 percentOfNorm:10}", first)
	}
	if _, hasKcal := first["kcal"]; hasKcal {
		t.Fatalf("entries[0] = %+v, want no kcal field exposed", first)
	}
	nextCursor, ok := data["nextCursor"].(map[string]any)
	if !ok || nextCursor["dateISO"] != "2026-06-18" || nextCursor["id"] != float64(21) {
		t.Fatalf("nextCursor = %v, want {dateISO:2026-06-18 id:21}", data["nextCursor"])
	}

	// Follow the cursor to the next page over real HTTP query params.
	nextURL := server.URL + "/api/food/product-history?catalogueIds=1&limit=2&cursorDate=2026-06-18&cursorId=21"
	page2 := decodeJSONRequest(t, http.MethodGet, nextURL, session.Cookie, "tab-a", nil, http.StatusOK)
	page2Data := page2["data"].(map[string]any)
	page2Entries := page2Data["entries"].([]any)
	if len(page2Entries) != 2 {
		t.Fatalf("page2 entries = %v, want 2-element array", page2Data["entries"])
	}
	if page2Data["nextCursor"] != nil {
		t.Fatalf("page2 nextCursor = %v, want nil (last page)", page2Data["nextCursor"])
	}

	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/product-history", session.Cookie, "tab-a", nil, http.StatusBadRequest)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/product-history?catalogueIds=1&limit=not-a-number", session.Cookie, "tab-a", nil, http.StatusBadRequest)
	assertJSONRequestStatus(t, http.MethodGet, server.URL+"/api/food/product-history?catalogueIds=not-a-number", session.Cookie, "tab-a", nil, http.StatusBadRequest)
}

func assertJSONRequestStatus(t *testing.T, method string, url string, sessionCookie string, clientID string, payload any, wantStatus int) {
	t.Helper()

	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		jsonBody, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		body = bytes.NewReader(jsonBody)
	}

	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie})
	request.Header.Set("X-Client-ID", clientID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", response.StatusCode, wantStatus)
	}
}

func decodeJSONRequest(t *testing.T, method string, url string, sessionCookie string, clientID string, payload any, wantStatus int) map[string]any {
	t.Helper()

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	request, err := http.NewRequest(method, url, bytes.NewReader(jsonBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie})
	request.Header.Set("X-Client-ID", clientID)
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", response.StatusCode, wantStatus)
	}

	var decoded map[string]any
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return decoded
}

func dialFoodWS(t *testing.T, httpURL string, sessionCookie string) *websocket.Conn {
	t.Helper()

	wsURL := "ws" + httpURL[len("http"):]
	parsed, err := url.Parse(httpURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	headers := http.Header{
		"Cookie": {auth.SessionCookieName + "=" + sessionCookie},
		"Origin": {"http://" + parsed.Host},
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	return conn
}

func drainFoodWSMessage(t *testing.T, conn *websocket.Conn) {
	t.Helper()

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var message map[string]any
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
}
