package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"megaapp-back/internal/config"

	"github.com/gorilla/websocket"
)

func TestAppWebSocketUpgradeWorksThroughMiddleware(t *testing.T) {
	tempDir := t.TempDir()
	cfg := appTestConfig(tempDir)

	prepareAppTestFiles(t, cfg)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	registerBody, err := json.Marshal(map[string]string{"username": "alice", "password": "password123"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	registerResponse, err := http.Post(server.URL+"/api/auth/register", "application/json", bytes.NewReader(registerBody))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	_ = registerResponse.Body.Close()
	if registerResponse.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d, want 201", registerResponse.StatusCode)
	}

	loginBody, err := json.Marshal(map[string]string{"username": "alice", "password": "password123"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	loginResponse, err := http.Post(server.URL+"/api/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer loginResponse.Body.Close()
	if loginResponse.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", loginResponse.StatusCode)
	}

	var session struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(loginResponse.Body).Decode(&session); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !session.Authenticated {
		t.Fatal("login response is not authenticated")
	}
	cookies := loginResponse.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookies = %d, want 1", len(cookies))
	}

	wsURL := "ws" + server.URL[len("http"):] + "/api/ws?clientId=tab-a"
	headers := http.Header{"Cookie": {cookies[0].Name + "=" + cookies[0].Value}, "Origin": {server.URL}}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		statusCode := 0
		if response != nil {
			statusCode = response.StatusCode
		}
		t.Fatalf("Dial() error = %v, status = %d", err, statusCode)
	}
	defer func() { _ = conn.Close() }()

	var message map[string]any
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("ReadJSON() error = %v", err)
	}
	if message["type"] != "SYNC_STATUS" {
		t.Fatalf("message type = %v, want SYNC_STATUS", message["type"])
	}
}

func TestAppServeAndShutdown(t *testing.T) {
	tempDir := t.TempDir()
	cfg := appTestConfig(tempDir)

	prepareAppTestFiles(t, cfg)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer listener.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Serve(listener)
	}()

	time.Sleep(100 * time.Millisecond)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := app.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("Serve() error = %v", err)
	}
}

func TestAppRejectsOversizedRequestBody(t *testing.T) {
	tempDir := t.TempDir()
	cfg := appTestConfig(tempDir)
	cfg.MaxRequestBodyBytes = 64

	prepareAppTestFiles(t, cfg)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	oversizedBody := `{"username":"` + strings.Repeat("a", 200) + `","password":"password123"}`
	response, err := http.Post(server.URL+"/api/auth/register", "application/json", strings.NewReader(oversizedBody))
	if err != nil {
		t.Fatalf("http.Post() error = %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.StatusCode)
	}
}

func TestAppServesTheConfiguredIngestSourcesOnly(t *testing.T) {
	tempDir := t.TempDir()
	cfg := appTestConfig(tempDir)
	const key = "wiringwiringwiringwiring12345678"
	cfg.IngestSources = []config.IngestSource{{Name: "ext", RotateBytes: 1_000_000}}
	cfg.IngestKeys = []string{key}

	prepareAppTestFiles(t, cfg)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("NewApp() error = %v", err)
	}
	defer func() { _ = app.Shutdown(context.Background()) }()

	server := httptest.NewServer(app.Handler)
	defer server.Close()

	post := func(path, withKey string) int {
		request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(`{"events":[{"id":"a","stream":"s","at":1,"data":{}}]}`))
		if err != nil {
			t.Fatalf("NewRequest() error = %v", err)
		}
		request.Header.Set("X-Ingest-Key", withKey)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("Do() error = %v", err)
		}
		_ = response.Body.Close()
		return response.StatusCode
	}

	if status := post("/api/ingest/ext", key); status != http.StatusOK {
		t.Fatalf("configured source: status = %d, want 200", status)
	}
	if status := post("/api/ingest/ext", "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", status)
	}
	if status := post("/api/ingest/telemetry", key); status != http.StatusNotFound {
		t.Fatalf("source that is not configured: status = %d, want 404", status)
	}
	if files, _ := filepath.Glob(filepath.Join(cfg.DataDir, "ingest", "ext", "ext-*.ndjson")); len(files) != 1 {
		t.Fatalf("stored files = %v, want one under the source's own directory", files)
	}
}

func appTestConfig(tempDir string) config.Config {
	return config.Config{
		AppEnv:                              "test",
		AppHost:                             "127.0.0.1",
		AppPort:                             3001,
		LogLevel:                            "info",
		DataDir:                             filepath.Join(tempDir, "data"),
		DatabaseName:                        "megaapp",
		DatabasePath:                        filepath.Join(tempDir, "data", "megaapp-test.db"),
		MigrationsDir:                       filepath.Join(tempDir, "migrations"),
		PublicDir:                           filepath.Join(tempDir, "public"),
		BackupsDir:                          filepath.Join(tempDir, "backups"),
		FlatlineBaseURL:                     "http://127.0.0.1:1",
		FlatlinePushTimeout:                 time.Second,
		FlatlinePollInterval:                time.Hour,
		FlatlinePollInitialLookback:         time.Minute,
		FlatlinePollMaxCatchUp:              time.Hour,
		OpenRouterTimeout:                   time.Minute,
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
		HTTPReadTimeout:                     time.Second,
		HTTPWriteTimeout:                    2 * time.Second,
		HTTPIdleTimeout:                     2 * time.Second,
		ShutdownTimeout:                     time.Second,
		MaxRequestBodyBytes:                 1024,
		MaxMultipartBodyBytes:               8 * 1024,
		WSReadLimitBytes:                    1024,
		WSWriteTimeout:                      time.Second,
	}
}

func prepareAppTestFiles(t *testing.T, cfg config.Config) {
	t.Helper()

	if err := os.MkdirAll(cfg.MigrationsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(cfg.PublicDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MigrationsDir, "000001_auth_and_settings.sql"), []byte(`
		CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT,
			hashedPassword TEXT,
			isAdmin BOOLEAN
		);
		CREATE TABLE IF NOT EXISTS userSettings (
			usersId INTEGER NOT NULL,
			namespace TEXT NOT NULL,
			payload TEXT NOT NULL,
			updatedAt TEXT NOT NULL,
			PRIMARY KEY (usersId, namespace)
		);
	`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.MigrationsDir, "000008_server_sessions.sql"), []byte(`
		CREATE TABLE auth_sessions (
			id TEXT PRIMARY KEY,
			secretHash BLOB NOT NULL,
			userId INTEGER NOT NULL,
			createdAt TEXT NOT NULL,
			expiresAt TEXT NOT NULL,
			renewedAt TEXT NOT NULL,
			revokedAt TEXT
		);
	`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
