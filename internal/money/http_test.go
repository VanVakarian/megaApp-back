package money

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/platform/idempotency"
	"megaapp-back/internal/platform/sqlite"

	"github.com/go-chi/chi/v5"
)

func TestMoneyRoutesSnapshotAndReferenceCrud(t *testing.T) {
	db := openMoneyTestDB(t)
	insertMoneyTestUser(t, db, 1, "alice")
	insertMoneyReferenceFixtures(t, db, 1)
	insertMoneyReadFixtures(t, db, 1)
	insertMoneyBrokerageAccount(t, db, 1, 2, "Brokerage", AccountKindBrokerage)

	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	handler := NewHandler(NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db})))

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	tokens := issueMoneySession(t, authService, 1)

	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/snapshot", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/organizations", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/currencies", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/categories", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/accounts", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/assets", tokens.AccessToken, nil, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/organizations", tokens.AccessToken, map[string]any{"title": "Wallet"}, http.StatusCreated)
	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/organizations/1", tokens.AccessToken, map[string]any{"title": "Updated Bank"}, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/currencies", tokens.AccessToken, map[string]any{
		"title":         "Dollar",
		"ticker":        "USD",
		"symbol":        "$",
		"symbolPosEnum": "before",
		"whitespace":    false,
	}, http.StatusCreated)
	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/currencies/1", tokens.AccessToken, map[string]any{
		"title":         "Ruble Updated",
		"ticker":        "RUB",
		"symbol":        "₽",
		"symbolPosEnum": "before",
		"whitespace":    false,
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/categories", tokens.AccessToken, map[string]any{
		"name":         "Salary",
		"categoryType": "income",
	}, http.StatusCreated)
	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/categories/2", tokens.AccessToken, map[string]any{
		"name":         "Groceries Updated",
		"categoryType": "expense",
		"parentId":     1,
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/accounts", tokens.AccessToken, map[string]any{
		"title":          "Card",
		"currencyId":     1,
		"isInvest":       false,
		"isArchived":     false,
		"kind":           "card",
		"organizationId": 1,
	}, http.StatusCreated)
	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/accounts/1", tokens.AccessToken, map[string]any{
		"title":          "Wallet Updated",
		"currencyId":     1,
		"isInvest":       false,
		"isArchived":     false,
		"kind":           "cash",
		"organizationId": 1,
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/assets", tokens.AccessToken, map[string]any{
		"title":          "Apple",
		"ticker":         "AAPL",
		"type":           "stock",
		"accountIds":     []int64{2, 2},
		"suspendedSince": "2026-06-01",
	}, http.StatusCreated)
	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/assets/2", tokens.AccessToken, map[string]any{
		"title":          "Bitcoin",
		"ticker":         "BTC",
		"type":           "crypto",
		"accountIds":     []int64{2},
		"suspendedSince": nil,
		"suspendedUntil": nil,
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/organizations/1", tokens.AccessToken, nil, http.StatusConflict)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/currencies/1", tokens.AccessToken, nil, http.StatusConflict)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/categories/1", tokens.AccessToken, nil, http.StatusConflict)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/accounts/1", tokens.AccessToken, nil, http.StatusConflict)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/assets/1", tokens.AccessToken, nil, http.StatusConflict)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/assets/2", tokens.AccessToken, nil, http.StatusOK)

	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/organizations/2", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/currencies/2", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/categories/3", tokens.AccessToken, nil, http.StatusOK)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/accounts/2", tokens.AccessToken, nil, http.StatusOK)
}

func TestMoneyRoutesTransactionsCrud(t *testing.T) {
	db := openMoneyTestDB(t)
	insertMoneyTestUser(t, db, 1, "alice")
	insertMoneyReferenceFixtures(t, db, 1)
	insertMoneyAccount(t, db, 1, 2, "Card", AccountKindCard)
	insertMoneyCategory(t, db, 1, 3, "Salary", nil, CategoryTypeIncome)

	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	handler := NewHandler(NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db})))

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	tokens := issueMoneySession(t, authService, 1)

	assertMoneyStatus(t, http.MethodGet, server.URL+"/api/money/transactions", tokens.AccessToken, nil, http.StatusOK)

	incomeBody := map[string]any{
		"operationId": "op-create-income",
		"dateISO":     "2026-06-18",
		"accountId":   1,
		"amount":      1000,
		"categoryId":  3,
		"kind":        "income",
		"isGift":      true,
		"notes":       "Salary",
	}
	incomeResponse := assertMoneyJSON(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, incomeBody, http.StatusCreated)
	incomeData := mustMoneyDataMap(t, incomeResponse)
	incomeID := int64(incomeData["id"].(float64))

	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/transactions/"+jsonNumberID(incomeID), tokens.AccessToken, map[string]any{
		"operationId": "op-update-income",
		"dateISO":     "2026-06-19",
		"accountId":   1,
		"amount":      1100,
		"kind":        "income",
		"isGift":      false,
		"notes":       "Salary updated",
	}, http.StatusOK)

	zeroResponse := assertMoneyJSON(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-create-zero-expense",
		"dateISO":     "2026-06-18",
		"accountId":   1,
		"amount":      0,
		"categoryId":  2,
		"kind":        "expense",
		"notes":       "Refunded",
	}, http.StatusCreated)
	zeroID := int64(mustMoneyDataMap(t, zeroResponse)["id"].(float64))
	var zeroAmount float64
	if err := db.QueryRow(`SELECT amount FROM moneyTransaction WHERE id = ?`, zeroID).Scan(&zeroAmount); err != nil {
		t.Fatalf("QueryRow() error = %v", err)
	}
	if zeroAmount != 0 {
		t.Fatalf("zero expense amount = %v, want 0", zeroAmount)
	}
	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-create-negative-expense",
		"dateISO":     "2026-06-18",
		"accountId":   1,
		"amount":      -1,
		"kind":        "expense",
	}, http.StatusBadRequest)
	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId":   "op-create-zero-transfer",
		"dateISO":       "2026-06-18",
		"accountId":     1,
		"amount":        0,
		"twinAccountId": 2,
		"twinAmount":    0,
		"kind":          "transfer",
	}, http.StatusBadRequest)

	transferResponse := assertMoneyJSON(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId":   "op-create-transfer",
		"dateISO":       "2026-06-20",
		"accountId":     1,
		"amount":        100,
		"twinAccountId": 2,
		"twinAmount":    95,
		"kind":          "transfer",
		"isGift":        false,
		"categoryId":    nil,
		"notes":         "Move",
	}, http.StatusCreated)
	transferData := mustMoneyDataMap(t, transferResponse)
	transferID := int64(transferData["id"].(float64))
	twinID := int64(transferData["twinId"].(float64))

	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/transactions/"+jsonNumberID(transferID), tokens.AccessToken, map[string]any{
		"operationId":   "op-update-transfer",
		"dateISO":       "2026-06-21",
		"accountId":     1,
		"amount":        120,
		"twinAccountId": 2,
		"twinAmount":    118,
		"kind":          "transfer",
		"isGift":        false,
		"categoryId":    nil,
		"notes":         "Move updated",
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/transactions/"+jsonNumberID(incomeID), tokens.AccessToken, map[string]any{"operationId": "op-delete-income"}, http.StatusOK)
	assertMoneyStatus(t, http.MethodDelete, server.URL+"/api/money/transactions/"+jsonNumberID(transferID), tokens.AccessToken, map[string]any{"operationId": "op-delete-transfer"}, http.StatusOK)

	var count int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM moneyTransaction WHERE id IN (?, ?)`, transferID, twinID).Scan(&count); err != nil {
		t.Fatalf("QueryRow() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("transfer pair rows count = %d, want 0", count)
	}
}

func TestMoneyRoutesInvestTransactions(t *testing.T) {
	db := openMoneyTestDB(t)
	insertMoneyTestUser(t, db, 1, "alice")
	insertMoneyReferenceFixtures(t, db, 1)
	insertMoneyBrokerageAccount(t, db, 1, 2, "Brokerage", AccountKindBrokerage)
	insertMoneyBrokerageAccount(t, db, 1, 3, "Crypto", AccountKindCrypto)
	insertMoneyAsset(t, db, 1, 1, "Apple", "AAPL", AssetTypeStock, []int64{2})
	insertMoneyAsset(t, db, 1, 2, "Bond", "OFZ", AssetTypeBond, []int64{2})

	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})
	handler := NewHandler(NewService(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db})))

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	tokens := issueMoneySession(t, authService, 1)

	buyResponse := assertMoneyJSON(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-buy",
		"dateISO":     "2026-06-18",
		"accountId":   2,
		"kind":        "invest_buy",
		"detailsJSON": map[string]any{
			"assetId":          1,
			"quantity":         2,
			"price":            100,
			"commissionAmount": 5,
		},
	}, http.StatusCreated)
	buyData := mustMoneyDataMap(t, buyResponse)
	buyID := int64(buyData["id"].(float64))

	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/transactions/"+jsonNumberID(buyID), tokens.AccessToken, map[string]any{
		"operationId": "op-update-buy-1",
		"dateISO":     "2026-06-19",
		"accountId":   2,
		"kind":        "invest_buy",
		"detailsJSON": map[string]any{
			"assetId":          1,
			"quantity":         3,
			"price":            90,
			"commissionAmount": 1,
		},
	}, http.StatusOK)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-sell",
		"dateISO":     "2026-06-20",
		"accountId":   2,
		"kind":        "invest_sell",
		"detailsJSON": map[string]any{
			"assetId":          1,
			"quantity":         1,
			"price":            120,
			"commissionAmount": 2,
		},
	}, http.StatusCreated)

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-dividend",
		"dateISO":     "2026-06-21",
		"accountId":   2,
		"amount":      15,
		"kind":        "invest_dividend",
		"detailsJSON": map[string]any{
			"assetId": 2,
		},
	}, http.StatusCreated)

	snapshotResponse := assertMoneyJSON(t, http.MethodGet, server.URL+"/api/money/snapshot", tokens.AccessToken, nil, http.StatusOK)
	snapshotData := snapshotResponse["data"].(map[string]any)
	trades := snapshotData["investAssetTrades"].([]any)
	if len(trades) != 2 {
		t.Fatalf("investAssetTrades len = %d, want 2", len(trades))
	}

	assertMoneyStatus(t, http.MethodPost, server.URL+"/api/money/transactions", tokens.AccessToken, map[string]any{
		"operationId": "op-invalid-account",
		"dateISO":     "2026-06-22",
		"accountId":   1,
		"kind":        "invest_buy",
		"detailsJSON": map[string]any{
			"assetId":          1,
			"quantity":         1,
			"price":            100,
			"commissionAmount": 1,
		},
	}, http.StatusBadRequest)

	assertMoneyStatus(t, http.MethodPut, server.URL+"/api/money/transactions/"+jsonNumberID(buyID), tokens.AccessToken, map[string]any{
		"operationId": "op-update-buy-2",
		"dateISO":     "2026-06-19",
		"accountId":   2,
		"kind":        "invest_buy",
		"detailsJSON": map[string]any{
			"assetId":          2,
			"quantity":         3,
			"price":            90,
			"commissionAmount": 1,
		},
	}, http.StatusBadRequest)
}

func TestMoneyRoutesTradesAndRateHistory(t *testing.T) {
	db := openMoneyTestDB(t)
	insertMoneyTestUser(t, db, 1, "alice")
	insertMoneyReferenceFixtures(t, db, 1)
	insertMoneyBrokerageAccount(t, db, 1, 2, "Brokerage", AccountKindBrokerage)
	insertMoneyCurrency(t, db, 1, 2, "Dollar", "USD", "$", SymbolPositionBefore)
	insertMoneyAsset(t, db, 1, 1, "Apple", "AAPL", AssetTypeStock, []int64{2})
	insertMoneyRateHistory(t, db, 1, "2026-06-10", `{"USD":1,"RUB":90,"AAPL":210}`)
	insertMoneyRateHistory(t, db, 2, "2026-06-30", `{"USD":1,"RUB":91,"AAPL":220}`)

	handler := NewHandler(NewServiceWithClock(NewRepository(db, sqlite.WriteDB{DB: db}), idempotency.NewStore(sqlite.WriteDB{DB: db}), fixedMoneyClock{now: time.Date(2026, time.June, 30, 12, 0, 0, 0, time.UTC)}))
	authRepo := auth.NewRepository(db, sqlite.WriteDB{DB: db})
	authService := auth.NewService(authRepo, auth.SessionConfig{})

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	defer server.Close()

	tokens := issueMoneySession(t, authService, 1)

	if _, _, err := handler.service.CreateTransaction(context.Background(), 1, "op-trade-buy", TransactionInput{
		DateISO:   "2026-06-15",
		AccountID: 2,
		Kind:      TransactionKindInvestBuy,
		DetailsJSON: map[string]any{
			"assetId":          1,
			"quantity":         2,
			"price":            100,
			"commissionAmount": 1,
		},
	}); err != nil {
		t.Fatalf("CreateTransaction() error = %v", err)
	}

	tradesResponse := assertMoneyJSON(t, http.MethodGet, server.URL+"/api/money/trades", tokens.AccessToken, nil, http.StatusOK)
	trades := tradesResponse["data"].([]any)
	if len(trades) != 1 {
		t.Fatalf("trades len = %d, want 1", len(trades))
	}
	trade := trades[0].(map[string]any)
	if trade["assetTicker"] != "AAPL" || trade["kind"] != "invest_buy" {
		t.Fatalf("trade = %#v", trade)
	}

	rateHistoryResponse := assertMoneyJSON(t, http.MethodGet, server.URL+"/api/money/rate-history", tokens.AccessToken, nil, http.StatusOK)
	rateHistory := rateHistoryResponse["data"].([]any)
	if len(rateHistory) != 2 {
		t.Fatalf("rateHistory len = %d, want 2", len(rateHistory))
	}
	firstRateHistory := rateHistory[0].(map[string]any)
	if firstRateHistory["ratesJson"] != `{"USD":1,"RUB":90,"AAPL":210}` {
		t.Fatalf("first rate history = %#v", firstRateHistory)
	}

	snapshotResponse := assertMoneyJSON(t, http.MethodGet, server.URL+"/api/money/snapshot", tokens.AccessToken, nil, http.StatusOK)
	snapshotRateHistory := snapshotResponse["data"].(map[string]any)["rateHistory"].([]any)
	if len(snapshotRateHistory) != 2 {
		t.Fatalf("snapshot rateHistory len = %d, want 2", len(snapshotRateHistory))
	}
	firstSnapshotRateHistory := snapshotRateHistory[0].(map[string]any)
	if _, ok := firstSnapshotRateHistory["ratesJson"].(map[string]any); !ok {
		t.Fatalf("snapshot ratesJson type = %T, want object", firstSnapshotRateHistory["ratesJson"])
	}
}

type moneySession struct {
	AccessToken string
}

func issueMoneySession(t *testing.T, service *auth.Service, userID int64) moneySession {
	t.Helper()

	session, err := service.CreateSession(t.Context(), userID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return moneySession{AccessToken: session.Cookie}
}

func assertMoneyStatus(t *testing.T, method string, url string, sessionCookie string, body any, wantStatus int) {
	t.Helper()

	var requestBody *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	} else {
		requestBody = bytes.NewReader(nil)
	}

	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie})
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != wantStatus {
		var payload map[string]any
		_ = json.NewDecoder(response.Body).Decode(&payload)
		t.Fatalf("%s %s status = %d, want %d, body = %+v", method, url, response.StatusCode, wantStatus, payload)
	}
}

func assertMoneyJSON(t *testing.T, method string, url string, sessionCookie string, body any, wantStatus int) map[string]any {
	t.Helper()

	var requestBody *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	} else {
		requestBody = bytes.NewReader(nil)
	}

	request, err := http.NewRequest(method, url, requestBody)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionCookie})
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body = %+v", method, url, response.StatusCode, wantStatus, payload)
	}
	return payload
}

func mustMoneyDataMap(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload data = %+v, want map", payload["data"])
	}
	return data
}

func jsonNumberID(id int64) string {
	return fmt.Sprintf("%d", id)
}
