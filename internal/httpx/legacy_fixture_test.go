package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/config"
	clockplatform "megaapp-back/internal/platform/clock"
)

func TestLegacyFixtureCriticalRoutesMatchRecordedParity(t *testing.T) {
	tempDir := t.TempDir()
	cfg := legacyFixtureConfig(t, tempDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := fixedLegacyClock{now: time.Date(2026, time.June, 18, 12, 0, 0, 0, time.UTC)}

	app, err := newApp(context.Background(), cfg, logger, clk)
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	token := legacyFixtureSession(t, app)

	statsData := assertLegacyFixtureJSON(t, http.MethodGet, server.URL+"/api/food/stats", token)
	statsDates := make([]string, 0, len(statsData))
	for date := range statsData {
		statsDates = append(statsDates, date)
	}
	sort.Strings(statsDates)
	if len(statsDates) != 2074 {
		t.Fatalf("stats len = %d, want 2074", len(statsDates))
	}
	if statsDates[0] != "2020-10-14" || statsDates[len(statsDates)-1] != "2026-06-18" {
		t.Fatalf("stats range = %s..%s, want 2020-10-14..2026-06-18", statsDates[0], statsDates[len(statsDates)-1])
	}
	assertLegacyStatsDay(t, statsData, "2025-01-15", 74.3, 74.1, 1993.755, 2228, false)
	assertLegacyStatsDay(t, statsData, "2025-06-15", 74.1, 74.8, 2205.261, 1939, false)
	assertLegacyStatsDay(t, statsData, "2026-01-15", 78.4, 78.2, 1733.645, 2244, false)
	assertLegacyStatsDay(t, statsData, "2026-05-15", 73.3, 72.5, 1597.285, 1952, false)
	assertLegacyStatsDay(t, statsData, "2026-06-10", 72, 71.4, 1512.225, 1849, false)

	snapshotResponse := assertLegacyFixtureJSON(t, http.MethodGet, server.URL+"/api/money/snapshot", token)
	snapshotData := snapshotResponse["data"].(map[string]any)
	assertLegacySliceLen(t, snapshotData, "currencies", 4)
	assertLegacySliceLen(t, snapshotData, "categories", 23)
	assertLegacySliceLen(t, snapshotData, "organizations", 3)
	assertLegacySliceLen(t, snapshotData, "accounts", 16)
	assertLegacySliceLen(t, snapshotData, "assets", 53)
	assertLegacySliceLen(t, snapshotData, "transactions", 3979)
	assertLegacySliceLen(t, snapshotData, "rateHistory", 4180)
	if _, exists := snapshotData["investAssetTrades"]; exists {
		t.Fatal("snapshot must not carry investAssetTrades")
	}

	firstSnapshotRate := snapshotData["rateHistory"].([]any)[0].(map[string]any)
	assertLegacyField(t, firstSnapshotRate, "dateISO", "2015-01-01")
	assertLegacyRateValue(t, firstSnapshotRate["ratesJson"].(map[string]any), "EUR", 1.2098628282546997)
	assertLegacyRateValue(t, firstSnapshotRate["ratesJson"].(map[string]any), "KZT", 0.0055429298802792785)
	assertLegacyRateValue(t, firstSnapshotRate["ratesJson"].(map[string]any), "RUB", 0.016474464372833297)

	lastSnapshotRate := snapshotData["rateHistory"].([]any)[len(snapshotData["rateHistory"].([]any))-1].(map[string]any)
	assertLegacyField(t, lastSnapshotRate, "dateISO", "2026-06-13")
	lastSnapshotRates := lastSnapshotRate["ratesJson"].(map[string]any)
	if len(lastSnapshotRates) != 1 {
		t.Fatalf("last snapshot rate tickers = %d, want 1", len(lastSnapshotRates))
	}
	assertLegacyRateValue(t, lastSnapshotRates, "RUB", 0.013807386370220238)

	firstAsset := snapshotData["assets"].([]any)[0].(map[string]any)
	assertLegacyField(t, firstAsset, "title", "Ark")
	assertLegacyField(t, firstAsset, "ticker", "ARK")
	assertLegacyField(t, firstAsset, "type", "crypto")
	accountIDs := firstAsset["accountIds"].([]any)
	if len(accountIDs) != 1 || accountIDs[0].(float64) != 8 {
		t.Fatalf("first asset accountIds = %v, want [8]", accountIDs)
	}

	rateHistoryResponse := assertLegacyFixtureJSON(t, http.MethodGet, server.URL+"/api/money/rate-history", token)
	rateHistory := rateHistoryResponse["data"].([]any)
	if len(rateHistory) != 4182 {
		t.Fatalf("rateHistory len = %d, want 4182", len(rateHistory))
	}
	firstRawRate := rateHistory[0].(map[string]any)
	lastRawRate := rateHistory[len(rateHistory)-1].(map[string]any)
	assertLegacyField(t, firstRawRate, "dateISO", "2015-01-01")
	assertLegacyField(t, firstRawRate, "ratesJson", "{\"EUR\":1.2098628282546997,\"KZT\":0.0055429298802792785,\"RUB\":0.016474464372833297}")
	assertLegacyField(t, lastRawRate, "dateISO", "2026-06-13")
	assertLegacyField(t, lastRawRate, "ratesJson", "{\"RUB\":0.013807386370220238,\"BTC\":64437.99982792179,\"ETH\":1681.02612153385,\"DASH\":35.44206725104095,\"XMR\":339.82449126772894,\"ZEC\":421.9166351467492,\"ARK\":0.12128499895334244,\"GLM\":0.11100000143051147,\"XRP\":1.1500450372695923}")

	transactionsResponse := assertLegacyFixtureJSON(t, http.MethodGet, server.URL+"/api/money/transactions", token)
	transactions := transactionsResponse["data"].([]any)
	if len(transactions) != 3979 {
		t.Fatalf("transactions len = %d, want 3979", len(transactions))
	}
}

func TestLegacyFixtureProdLikeStartupWorks(t *testing.T) {
	tempDir := t.TempDir()
	cfg := legacyFixtureConfig(t, tempDir)
	cfg.AppEnv = "prod"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := newApp(context.Background(), cfg, logger, fixedLegacyClock{now: time.Date(2026, time.June, 18, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	response, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", response.StatusCode)
	}
}

func TestLegacyFixtureCriticalRoutesHandleLowConcurrency(t *testing.T) {
	tempDir := t.TempDir()
	cfg := legacyFixtureConfig(t, tempDir)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clk := fixedLegacyClock{now: time.Date(2026, time.June, 18, 12, 0, 0, 0, time.UTC)}

	app, err := newApp(context.Background(), cfg, logger, clk)
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	token := legacyFixtureSession(t, app)
	urls := []string{
		server.URL + "/api/food/stats",
		server.URL + "/api/money/snapshot",
		server.URL + "/api/money/rate-history",
		server.URL + "/api/money/transactions",
	}

	errCh := make(chan error, len(urls)*3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		for _, url := range urls {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				request, err := http.NewRequest(http.MethodGet, url, nil)
				if err != nil {
					errCh <- err
					return
				}
				request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					errCh <- err
					return
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					errCh <- errUnexpectedStatus{url: url, statusCode: response.StatusCode}
				}
			}(url)
		}
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent request error = %v", err)
		}
	}
}

type fixedLegacyClock struct {
	now time.Time
}

func (c fixedLegacyClock) Now() time.Time {
	return c.now
}

type errUnexpectedStatus struct {
	url        string
	statusCode int
}

func (e errUnexpectedStatus) Error() string {
	return e.url + " returned unexpected status"
}

func legacyFixtureConfig(t *testing.T, tempDir string) config.Config {
	t.Helper()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "public"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempDir, "backups"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	databasePath := filepath.Join(dataDir, "megaapp-test.db")
	copyLegacyFixtureDB(t, legacyFixtureDBPath(t), databasePath)

	cfg := config.Config{
		AppEnv:                              "test",
		AppHost:                             "127.0.0.1",
		AppPort:                             3001,
		LogLevel:                            "info",
		DataDir:                             dataDir,
		DatabaseName:                        "megaapp",
		DatabasePath:                        databasePath,
		MigrationsDir:                       filepath.Join("..", "..", "migrations"),
		PublicDir:                           filepath.Join(tempDir, "public"),
		BackupsDir:                          filepath.Join(tempDir, "backups"),
		FlatlineBaseURL:                     "http://127.0.0.1:1",
		FlatlinePushTimeout:                 time.Second,
		FlatlinePollInterval:                time.Hour,
		FlatlinePollInitialLookback:         time.Minute,
		FlatlinePollMaxCatchUp:              time.Hour,
		OpenRouterTimeout:                   time.Minute,
		OpenAIEmbeddingDims:                 768,
		OpenAITimeout:                       time.Minute,
		PersonalKcalJobSchedule:             "0 2 1 * *",
		PersonalKcalLookbackMonths:          3,
		PersonalKcalDecayRate:               0.6,
		PersonalKcalCoverageThreshold:       0.5,
		PersonalKcalMaxMonthlyChangePercent: 10,
		PersonalKcalAnchorLambda:            3,
		PersonalKcalEvidenceHalfKcal:        333,
		PersonalKcalCoefLogStep:             0.03,
		PersonalKcalNormStep:                33,
		PersonalKcalXStep:                   33,
		PersonalKcalPopulation:              33,
		PersonalKcalMaxGenerations:          333,
		PersonalKcalMaxStale:                33,
		QuotesJobSchedule:                   "0 3 * * *",
		QuotesFetchDays:                     7,
		QuotesRetryAttempts:                 3,
		QuotesRetryDelay:                    time.Second,
		QuotesRequestTimeout:                time.Second,
		BackupJobSchedule:                   "0 2 * * *",
		BackupOperationTimeout:              5 * time.Minute,
		HTTPReadTimeout:                     time.Second,
		HTTPWriteTimeout:                    2 * time.Second,
		HTTPIdleTimeout:                     2 * time.Second,
		ShutdownTimeout:                     time.Second,
		MaxRequestBodyBytes:                 1 << 20,
		MaxMultipartBodyBytes:               8 << 20,
		WSReadLimitBytes:                    64 << 10,
		WSWriteTimeout:                      time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	return cfg
}

func legacyFixtureDBPath(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if override := filepath.Clean(os.Getenv("MEGAAPP_LEGACY_FIXTURE_DB")); override != "." && override != "" {
		candidates = append(candidates, override)
	}
	candidates = append(candidates, filepath.Join("..", "..", "old-js", "megaapp-test-005.db"))
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate
		}
	}
	t.Skip("legacy fixture db not available; reference parity suite skipped")
	return ""
}

func copyLegacyFixtureDB(t *testing.T, from string, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer src.Close()

	dst, err := os.Create(to)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
}

func legacyFixtureSession(t *testing.T, app *App) string {
	t.Helper()
	service := auth.NewService(auth.NewRepository(app.DB.Read(), app.DB.Write()), auth.SessionConfig{})
	session, err := service.CreateSession(t.Context(), 1)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return session.Cookie
}

func assertLegacyFixtureJSON(t *testing.T, method string, url string, token string) map[string]any {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 200, body = %s", response.StatusCode, string(body))
	}

	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return payload
}

func assertLegacyStatsDay(t *testing.T, statsData map[string]any, date string, weight float64, avgWeight float64, dailyKcals float64, targetKcals float64, virtual bool) {
	t.Helper()
	day := statsData[date].([]any)
	assertLegacyFloat(t, day[0], weight)
	assertLegacyFloat(t, day[1], avgWeight)
	assertLegacyFloat(t, day[2], dailyKcals)
	assertLegacyFloat(t, day[3], targetKcals)
	if day[4].(bool) != virtual {
		t.Fatalf("stats[%s][4] = %v, want %v", date, day[4], virtual)
	}
}

func assertLegacySliceLen(t *testing.T, data map[string]any, key string, want int) {
	t.Helper()
	got := len(data[key].([]any))
	if got != want {
		t.Fatalf("%s len = %d, want %d", key, got, want)
	}
}

func assertLegacyRateValue(t *testing.T, rates map[string]any, key string, want float64) {
	t.Helper()
	assertLegacyFloat(t, rates[key], want)
}

func assertLegacyField(t *testing.T, data map[string]any, key string, want string) {
	t.Helper()
	got, ok := data[key].(string)
	if !ok {
		t.Fatalf("%s type = %T, want string", key, data[key])
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func assertLegacyFloat(t *testing.T, value any, want float64) {
	t.Helper()
	got, ok := value.(float64)
	if !ok {
		t.Fatalf("value type = %T, want float64", value)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("float = %.12f, want %.12f", got, want)
	}
}

var _ clockplatform.Clock = fixedLegacyClock{}
