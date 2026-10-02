package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/config"
	"megaapp-back/internal/platform/sqlite"

	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

const (
	keyOne = "oneoneoneoneoneoneoneoneone1111"
	keyTwo = "twotwotwotwotwotwotwotwotwo2222"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type testServer struct {
	url     string
	dataDir string
	handler *Handler
	session auth.LoginResult
}

func startServer(t *testing.T, sources []config.IngestSource, keys []string) testServer {
	t.Helper()
	authService, session := openTestSession(t)
	dataDir := t.TempDir()
	handler := NewHandler(dataDir, sources, keys, quietLogger())
	t.Cleanup(func() { _ = handler.Close() })

	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return testServer{url: server.URL, dataDir: dataDir, handler: handler, session: session}
}

func defaultServer(t *testing.T) testServer {
	t.Helper()
	return startServer(t, []config.IngestSource{
		{Name: "telemetry", RotateBytes: 1 << 30},
		{Name: "ext", RotateBytes: 1 << 30},
	}, []string{keyOne, keyTwo})
}

func openTestSession(t *testing.T) (*auth.Service, auth.LoginResult) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT, hashedPassword TEXT, isAdmin BOOLEAN);
		CREATE TABLE auth_sessions (id TEXT PRIMARY KEY, secretHash BLOB NOT NULL, userId INTEGER NOT NULL, createdAt INTEGER NOT NULL, expiresAt INTEGER NOT NULL, renewedAt INTEGER NOT NULL, revokedAt INTEGER DEFAULT NULL);
	`); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	authService := auth.NewService(auth.NewRepository(db, sqlite.WriteDB{DB: db}), auth.SessionConfig{})
	userID, err := authService.Register(context.Background(), "alice", "password123")
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	session, err := authService.CreateSession(context.Background(), userID)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return authService, session
}

type call struct {
	path    string
	body    string
	method  string
	headers map[string]string
	cookie  string
}

func (s testServer) do(t *testing.T, c call) (int, string) {
	t.Helper()
	method := c.method
	if method == "" {
		method = http.MethodPost
	}
	request, err := http.NewRequest(method, s.url+c.path, strings.NewReader(c.body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	for name, value := range c.headers {
		request.Header.Set(name, value)
	}
	if c.cookie != "" {
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: c.cookie})
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	return response.StatusCode, string(body)
}

func (s testServer) storedLines(t *testing.T, source string) []map[string]any {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(s.dataDir, "ingest", source, source+"-*.ndjson"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	sort.Strings(files)
	var lines []map[string]any
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		lines = append(lines, decodeLines(t, data)...)
	}
	return lines
}

func oneEvent(id string) string {
	return `{"id":"` + id + `","stream":"diagnostic","at":1790829406415,"data":{"message":"hello"}}`
}

func batchOf(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func withKey(key string) map[string]string {
	return map[string]string{keyHeader: key}
}

type answer struct {
	Received int `json:"received"`
	Stored   int `json:"stored"`
	Rejected []struct {
		Index int    `json:"index"`
		Code  string `json:"code"`
	} `json:"rejected"`
}

func decodeAnswer(t *testing.T, body string) answer {
	t.Helper()
	var decoded answer
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("answer %q is not JSON: %v", body, err)
	}
	return decoded
}

func TestEverySourceAcceptsAKeyAndStoresABatchWithoutAUser(t *testing.T) {
	for _, source := range []string{"telemetry", "ext"} {
		t.Run(source, func(t *testing.T) {
			server := defaultServer(t)

			status, body := server.do(t, call{
				path:    "/api/ingest/" + source,
				body:    batchOf(oneEvent("a"), oneEvent("b")),
				headers: map[string]string{keyHeader: keyOne, clientIDHeader: "install-123"},
			})

			if status != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", status, body)
			}
			if got := decodeAnswer(t, body); got.Received != 2 || got.Stored != 2 || len(got.Rejected) != 0 {
				t.Fatalf("answer = %s, want received 2, stored 2, nothing rejected", body)
			}
			if !strings.Contains(body, `"rejected":[]`) {
				t.Fatalf("answer = %s, want an empty list (not null) of rejected", body)
			}
			lines := server.storedLines(t, source)
			if len(lines) != 2 || lines[0]["id"] != "a" || lines[1]["id"] != "b" {
				t.Fatalf("stored = %v, want events a and b", lines)
			}
			if lines[0]["source"] != source || lines[0]["client"] != "install-123" {
				t.Fatalf("server fields = %v", lines[0])
			}
			if _, present := lines[0]["user"]; present {
				t.Fatal("a request with a key stored a user")
			}
		})
	}
}

func TestEverySourceAcceptsASessionAndStoresItsUser(t *testing.T) {
	for _, source := range []string{"telemetry", "ext"} {
		t.Run(source, func(t *testing.T) {
			server := defaultServer(t)

			status, body := server.do(t, call{path: "/api/ingest/" + source, body: batchOf(oneEvent("a")), cookie: server.session.Cookie})

			if status != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", status, body)
			}
			lines := server.storedLines(t, source)
			if len(lines) != 1 || lines[0]["user"] != float64(server.session.Identity.UserID) {
				t.Fatalf("stored = %v, want one line carrying the session's user", lines)
			}
			if lines[0]["client"] != "" {
				t.Fatalf("client = %v, want empty when the request carries no client id", lines[0]["client"])
			}
		})
	}
}

func TestEveryConfiguredKeyWorks(t *testing.T) {
	server := defaultServer(t)

	for _, key := range []string{keyOne, keyTwo} {
		status, body := server.do(t, call{path: "/api/ingest/ext", body: batchOf(oneEvent("a")), headers: withKey(key)})
		if status != http.StatusOK {
			t.Fatalf("key %q: status = %d (%s), want 200", key[:3], status, body)
		}
	}
}

func TestRequestsWithNeitherAValidKeyNorASessionAreRefused(t *testing.T) {
	tests := []struct {
		name       string
		call       func(server testServer) call
		wantStatus int
	}{
		{"no credentials", func(testServer) call { return call{} }, http.StatusUnauthorized},
		{"empty key", func(testServer) call { return call{headers: withKey("")} }, http.StatusUnauthorized},
		{"wrong key", func(testServer) call { return call{headers: withKey("wrongwrongwrongwrongwrong1234")} }, http.StatusUnauthorized},
		{"a prefix of the right key", func(testServer) call { return call{headers: withKey(keyOne[:20])} }, http.StatusUnauthorized},
		{"the right key with something appended", func(testServer) call { return call{headers: withKey(keyOne + "x")} }, http.StatusUnauthorized},
		{"a garbage cookie", func(testServer) call { return call{cookie: "garbage"} }, http.StatusUnauthorized},
		{
			"a valid session from another origin",
			func(server testServer) call {
				return call{cookie: server.session.Cookie, headers: map[string]string{"Origin": "https://evil.example"}}
			},
			http.StatusForbidden,
		},
	}

	for _, source := range []string{"telemetry", "ext"} {
		for _, tt := range tests {
			t.Run(source+"/"+tt.name, func(t *testing.T) {
				server := defaultServer(t)
				c := tt.call(server)
				c.path, c.body = "/api/ingest/"+source, batchOf(oneEvent("a"))

				status, _ := server.do(t, c)

				if status != tt.wantStatus {
					t.Fatalf("status = %d, want %d", status, tt.wantStatus)
				}
				if lines := server.storedLines(t, source); len(lines) != 0 {
					t.Fatalf("a refused request stored %v", lines)
				}
			})
		}
	}
}

func TestWithoutKeysOnlySessionsGetIn(t *testing.T) {
	server := startServer(t, []config.IngestSource{{Name: "telemetry", RotateBytes: 1 << 30}}, nil)

	if status, _ := server.do(t, call{path: "/api/ingest/telemetry", body: batchOf(oneEvent("a")), headers: withKey("")}); status != http.StatusUnauthorized {
		t.Fatalf("empty key: status = %d, want 401", status)
	}
	if status, _ := server.do(t, call{path: "/api/ingest/telemetry", body: batchOf(oneEvent("a")), cookie: server.session.Cookie}); status != http.StatusOK {
		t.Fatalf("session: status = %d, want 200", status)
	}
}

func TestAKeyIsAcceptedFromAnExtensionOrigin(t *testing.T) {
	server := defaultServer(t)

	status, body := server.do(t, call{
		path:    "/api/ingest/ext",
		body:    batchOf(oneEvent("a")),
		headers: map[string]string{keyHeader: keyOne, "Origin": "chrome-extension://abcdefghijklmnop"},
	})

	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200: the same-origin check belongs to sessions only", status, body)
	}
}

func TestUnknownSourceAndWrongMethodAreRoutedAway(t *testing.T) {
	server := defaultServer(t)

	if status, _ := server.do(t, call{path: "/api/ingest/nobody", body: batchOf(oneEvent("a")), headers: withKey(keyOne)}); status != http.StatusNotFound {
		t.Fatalf("unknown source: status = %d, want 404", status)
	}
	if status, _ := server.do(t, call{path: "/api/ingest/ext", method: http.MethodGet, headers: withKey(keyOne)}); status != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status = %d, want 405", status)
	}
}

func TestNoSourcesMeansNoRoutes(t *testing.T) {
	server := startServer(t, nil, []string{keyOne})

	status, _ := server.do(t, call{path: "/api/ingest/ext", body: batchOf(oneEvent("a")), headers: withKey(keyOne)})

	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
}

func TestBadEventsAreRejectedOneByOneWhileTheRestIsStored(t *testing.T) {
	server := defaultServer(t)

	status, body := server.do(t, call{
		path:    "/api/ingest/ext",
		body:    batchOf(oneEvent("a"), `{"id":"","stream":"s","at":1,"data":{}}`, oneEvent("c"), `null`),
		headers: withKey(keyOne),
	})

	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, body)
	}
	got := decodeAnswer(t, body)
	if got.Received != 4 || got.Stored != 2 || len(got.Rejected) != 2 {
		t.Fatalf("answer = %s, want received 4, stored 2, two rejected", body)
	}
	if got.Rejected[0].Index != 1 || got.Rejected[0].Code != reasonBadID || got.Rejected[1].Index != 3 || got.Rejected[1].Code != reasonNotAnObject {
		t.Fatalf("rejected = %+v, want index 1 bad_id and index 3 not_an_object", got.Rejected)
	}
	if lines := server.storedLines(t, "ext"); len(lines) != 2 || lines[0]["id"] != "a" || lines[1]["id"] != "c" {
		t.Fatalf("stored = %v, want events a and c", lines)
	}
}

func TestABatchOfNothingValidAnswers200AndWritesNothing(t *testing.T) {
	server := defaultServer(t)

	status, body := server.do(t, call{path: "/api/ingest/ext", body: batchOf(`null`), headers: withKey(keyOne)})

	if status != http.StatusOK || decodeAnswer(t, body).Stored != 0 {
		t.Fatalf("status = %d, answer = %s; want 200 with nothing stored", status, body)
	}
	if _, err := os.Stat(filepath.Join(server.dataDir, "ingest", "ext")); !os.IsNotExist(err) {
		t.Fatalf("a batch with nothing valid created the source's directory, err = %v", err)
	}
}

func TestMalformedRequestsAre400(t *testing.T) {
	events := make([]string, maxBatchEvents+1)
	for i := range events {
		events[i] = oneEvent(strconv.Itoa(i))
	}
	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `not json`},
		{"empty body", ``},
		{"a list instead of an object", `[]`},
		{"no events field", `{}`},
		{"events is not a list", `{"events":"x"}`},
		{"no events", `{"events":[]}`},
		{"too many events", batchOf(events...)},
		{"negative dropped", `{"events":[` + oneEvent("a") + `],"dropped":-1}`},
		{"dropped is not a number", `{"events":[` + oneEvent("a") + `],"dropped":"many"}`},
		{"trailing garbage", batchOf(oneEvent("a")) + `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := defaultServer(t)

			status, _ := server.do(t, call{path: "/api/ingest/ext", body: tt.body, headers: withKey(keyOne)})

			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if lines := server.storedLines(t, "ext"); len(lines) != 0 {
				t.Fatalf("a malformed request stored %v", lines)
			}
		})
	}
}

func TestOversizedRequestIs413(t *testing.T) {
	server := defaultServer(t)
	huge := `{"events":[{"id":"a","stream":"s","at":1,"data":{"blob":"` + strings.Repeat("x", maxRequestBytes) + `"}}]}`

	status, _ := server.do(t, call{path: "/api/ingest/ext", body: huge, headers: withKey(keyOne)})

	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", status)
	}
}

func TestDroppedCountLandsOnTheFirstStoredEvent(t *testing.T) {
	server := defaultServer(t)

	status, body := server.do(t, call{
		path:    "/api/ingest/ext",
		body:    `{"events":[` + oneEvent("a") + `,` + oneEvent("b") + `],"dropped":7}`,
		headers: withKey(keyOne),
	})

	if status != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", status, body)
	}
	lines := server.storedLines(t, "ext")
	if lines[0]["droppedBefore"] != float64(7) {
		t.Fatalf("first line = %v, want droppedBefore 7", lines[0])
	}
	if _, present := lines[1]["droppedBefore"]; present {
		t.Fatalf("second line = %v, want no droppedBefore", lines[1])
	}
}

func TestMalformedClientIDIsStoredEmptyInsteadOfRefusingTheBatch(t *testing.T) {
	server := defaultServer(t)

	status, _ := server.do(t, call{
		path:    "/api/ingest/ext",
		body:    batchOf(oneEvent("a")),
		headers: map[string]string{keyHeader: keyOne, clientIDHeader: "has spaces & symbols"},
	})

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if lines := server.storedLines(t, "ext"); lines[0]["client"] != "" {
		t.Fatalf("client = %v, want empty", lines[0]["client"])
	}
}

func TestAStorageFailureIs500SoTheClientKeepsItsEvents(t *testing.T) {
	server := defaultServer(t)
	if err := os.WriteFile(filepath.Join(server.dataDir, "ingest"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	status, _ := server.do(t, call{path: "/api/ingest/ext", body: batchOf(oneEvent("a")), headers: withKey(keyOne)})

	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", status)
	}
}

func TestEachSourceRotatesAtItsOwnSize(t *testing.T) {
	server := startServer(t, []config.IngestSource{
		{Name: "small", RotateBytes: 600},
		{Name: "large", RotateBytes: 1 << 30},
	}, []string{keyOne})

	for i := 0; i < 5; i++ {
		for _, source := range []string{"small", "large"} {
			status, body := server.do(t, call{
				path:    "/api/ingest/" + source,
				body:    batchOf(oneEvent("event-" + strconv.Itoa(i))),
				headers: withKey(keyOne),
			})
			if status != http.StatusOK {
				t.Fatalf("%s: status = %d (%s), want 200", source, status, body)
			}
		}
	}
	if err := server.handler.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	smallArchives, _ := filepath.Glob(filepath.Join(server.dataDir, "ingest", "small", "logs-archive", "small-*.zip"))
	if len(smallArchives) == 0 {
		t.Fatal("the small source never rotated")
	}
	largeArchives, _ := filepath.Glob(filepath.Join(server.dataDir, "ingest", "large", "logs-archive", "*"))
	if len(largeArchives) != 0 {
		t.Fatalf("the large source rotated: %v", largeArchives)
	}
	if lines := server.storedLines(t, "large"); len(lines) != 5 {
		t.Fatalf("large stored %d lines, want 5 in one file", len(lines))
	}
}

func TestNoKeyIsLogged(t *testing.T) {
	var logs bytes.Buffer
	handler := NewHandler(t.TempDir(), []config.IngestSource{{Name: "ext", RotateBytes: 1 << 30}}, []string{keyOne, keyTwo}, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { _ = handler.Close() })
	authService, _ := openTestSession(t)
	router := chi.NewRouter()
	RegisterRoutes(router, authService, handler)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ingest/ext", strings.NewReader(batchOf(oneEvent("a"))))
	request.Header.Set(keyHeader, "wrongwrongwrongwrongwrong1234")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()

	for _, secret := range []string{keyOne, keyTwo, "wrongwrongwrongwrongwrong1234"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log contains a key:\n%s", logs.String())
		}
	}
	if !strings.Contains(logs.String(), "ingest_source_configured") {
		t.Fatalf("the log lacks ingest_source_configured:\n%s", logs.String())
	}
}
