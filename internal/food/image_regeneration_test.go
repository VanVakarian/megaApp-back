package food

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"megaapp-back/internal/auth"
	clockplatform "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/sqlite"
	wspkg "megaapp-back/internal/ws"

	"github.com/go-chi/chi/v5"
)

type fakeImageRequester struct {
	outcome ImageRequestOutcome
	calls   []fakeImageRequest
}

type fakeImageRequest struct {
	catalogueID int64
	name        string
	replace     bool
}

func (f *fakeImageRequester) RequestProductImageGeneration(catalogueID int64, productName string, _ string, replace bool) ImageRequestOutcome {
	f.calls = append(f.calls, fakeImageRequest{catalogueID: catalogueID, name: productName, replace: replace})
	return f.outcome
}

func TestRegenerateProductImageOverHTTP(t *testing.T) {
	tests := []struct {
		name        string
		admin       bool
		productID   int64
		outcome     ImageRequestOutcome
		wantStatus  int
		wantRequest bool
	}{
		{name: "regular user is refused before any lookup", admin: false, productID: 1, outcome: ImageRequestQueued, wantStatus: http.StatusForbidden},
		{name: "admin queues a replacing generation", admin: true, productID: 1, outcome: ImageRequestQueued, wantStatus: http.StatusAccepted, wantRequest: true},
		{name: "already queued is still accepted", admin: true, productID: 1, outcome: ImageRequestAlreadyQueued, wantStatus: http.StatusAccepted, wantRequest: true},
		{name: "unknown product", admin: true, productID: 999, outcome: ImageRequestQueued, wantStatus: http.StatusNotFound},
		{name: "full queue is reported", admin: true, productID: 1, outcome: ImageRequestQueueFull, wantStatus: http.StatusTooManyRequests, wantRequest: true},
		{name: "disabled generation is reported", admin: true, productID: 1, outcome: ImageRequestDisabled, wantStatus: http.StatusConflict, wantRequest: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, db := newArchiveTestService(t)
			requester := &fakeImageRequester{outcome: tt.outcome}
			service.SetImageGenerationRequester(requester)
			authService := auth.NewService(auth.NewRepository(db, sqlite.WriteDB{DB: db}), auth.SessionConfig{})
			service.SetAdminChecker(authService)
			hub := wspkg.NewHub(time.Second, wspkg.NewSyncState())
			defer func() { _ = hub.Close() }()

			router := chi.NewRouter()
			RegisterCatalogueRoutes(router, authService, NewCatalogueHandler(service, NewWSRealtimePublisher(hub, clockplatform.NewRealClock()), &fakeMetricsRecorder{}))
			server := httptest.NewServer(router)
			defer server.Close()

			userID := regularUserID
			if tt.admin {
				userID = adminUserID
			}
			session, err := authService.CreateSession(t.Context(), userID)
			if err != nil {
				t.Fatalf("CreateSession() error = %v", err)
			}

			assertJSONRequestStatus(t, http.MethodPost, server.URL+"/api/food/catalogue/"+strconv.FormatInt(tt.productID, 10)+"/regenerate-image", session.Cookie, "tab", nil, tt.wantStatus)

			if !tt.wantRequest {
				if len(requester.calls) != 0 {
					t.Fatalf("requester calls = %+v, want none", requester.calls)
				}
				return
			}
			if len(requester.calls) != 1 || requester.calls[0] != (fakeImageRequest{catalogueID: tt.productID, name: "Apple", replace: true}) {
				t.Fatalf("requester calls = %+v, want one replacing request for Apple", requester.calls)
			}
		})
	}
}

func TestRegenerateProductImageWithoutPipelineIsReported(t *testing.T) {
	service, _ := newArchiveTestService(t)

	if _, err := service.RegenerateProductImage(context.Background(), adminUserID, 1); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("RegenerateProductImage() error = %v, want a disabled-generation error", err)
	}
}

func TestBumpVersionKeepsCachedEntries(t *testing.T) {
	cache := NewCatalogueCache()
	cache.Set(map[int64]CatalogueEntry{1: {ID: 1, Name: "Apple"}})
	before := cache.Version()

	cache.BumpVersion()

	if after := cache.Version(); after != before+1 {
		t.Fatalf("version = %d, want %d", after, before+1)
	}
	if entries, ok := cache.Get(); !ok || len(entries) != 1 {
		t.Fatalf("entries = %v (cached=%v), want the entry kept", entries, ok)
	}
}

func TestLogProductsWithoutImageSkipsArchivedAndProductsWithImage(t *testing.T) {
	service, _ := newArchiveTestService(t)
	store, err := NewImageStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageStore() error = %v", err)
	}
	store.SetImageVersion(1, 1)
	service.SetImageVersionProvider(store)
	var logs bytes.Buffer

	service.LogProductsWithoutImage(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))

	// Fixture: Apple (1) has an image, Bread (2) has none, the archived Old apple (3) is not counted.
	if output := logs.String(); !strings.Contains(output, "products without image") || !strings.Contains(output, "catalogueIds=[2]") {
		t.Fatalf("log = %q, want a warning listing only product 2", output)
	}
}

type recordingRealtime struct {
	RealtimePublisher
	mu        sync.Mutex
	generated []int64
	failed    []int64
}

func (r *recordingRealtime) PublishCatalogueImageGenerated(_ int64, imageVersion int64, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.generated = append(r.generated, imageVersion)
}

func (r *recordingRealtime) PublishCatalogueImageFailed(catalogueID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = append(r.failed, catalogueID)
}

func (r *recordingRealtime) counts() (generated int, failed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.generated), len(r.failed)
}

type fakeImageGenerator struct {
	failing atomic.Bool
	block   chan struct{}
	data    []byte
}

func (g *fakeImageGenerator) GenerateFoodImage(_ context.Context, _ string) (GeneratedImage, error) {
	if g.block != nil {
		<-g.block
	}
	if g.failing.Load() {
		return GeneratedImage{}, io.ErrUnexpectedEOF
	}
	return GeneratedImage{Data: g.data, Format: "png", Model: "fake", Provider: "fake"}, nil
}

func newFakeImageGenerator(t *testing.T) *fakeImageGenerator {
	t.Helper()

	source := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for x := range 8 {
		for y := range 8 {
			source.Set(x, y, color.NRGBA{R: uint8(x * 30), G: uint8(y * 30), B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return &fakeImageGenerator{data: encoded.Bytes()}
}

type testPipeline struct {
	*ImagePipeline
	store     *ImageStore
	realtime  *recordingRealtime
	readyRuns *atomic.Int32
}

func newTestPipeline(t *testing.T, generator ImageGenerator, maxAttempts int, retryBaseWait time.Duration) testPipeline {
	t.Helper()

	store, err := NewImageStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewImageStore() error = %v", err)
	}
	realtime := &recordingRealtime{}
	readyRuns := &atomic.Int32{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pipeline := newImagePipeline(store, generator, realtime, func() { readyRuns.Add(1) }, maxAttempts, logger, time.Millisecond, retryBaseWait)
	t.Cleanup(func() { _ = pipeline.Close() })
	return testPipeline{ImagePipeline: pipeline, store: store, realtime: realtime, readyRuns: readyRuns}
}

func waitFor(t *testing.T, description string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fileExists(t *testing.T, dir string, name string) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func TestPipelineReplacesImageAndRemovesOldVersionAfterSuccess(t *testing.T) {
	generator := newFakeImageGenerator(t)
	pipeline := newTestPipeline(t, generator, 2, time.Millisecond)

	if got := pipeline.RequestProductImageGeneration(7, "Egg yolk", "yolk", false); got != ImageRequestQueued {
		t.Fatalf("first request = %q, want queued", got)
	}
	waitFor(t, "first image", func() bool { generated, _ := pipeline.realtime.counts(); return generated == 1 })
	if !fileExists(t, pipeline.store.FoodDir(), "7-thumb-v1.webp") {
		t.Fatal("version 1 thumbnail is missing")
	}

	if got := pipeline.RequestProductImageGeneration(7, "Egg yolk", "yolk", false); got != ImageRequestHasImage {
		t.Fatalf("request without replace over an existing image = %q, want has_image", got)
	}
	if got := pipeline.RequestProductImageGeneration(7, "Egg yolk", "yolk", true); got != ImageRequestQueued {
		t.Fatalf("replacing request = %q, want queued", got)
	}
	waitFor(t, "second image", func() bool { generated, _ := pipeline.realtime.counts(); return generated == 2 })

	// The old version goes only after the announcement of the new one, so give the cleanup a moment.
	waitFor(t, "old version removal", func() bool { return !fileExists(t, pipeline.store.FoodDir(), "7-thumb-v1.webp") })
	if !fileExists(t, pipeline.store.FoodDir(), "7-thumb-v2.webp") {
		t.Fatal("version 2 thumbnail is missing")
	}
	if version := pipeline.store.ImageVersion(7); version == nil || *version != 2 {
		t.Fatalf("image version = %v, want 2", version)
	}
	if got := pipeline.readyRuns.Load(); got != 2 {
		t.Fatalf("catalogue version bumps = %d, want 2", got)
	}
}

func TestPipelineKeepsExistingImageWhenReplacementFails(t *testing.T) {
	generator := newFakeImageGenerator(t)
	pipeline := newTestPipeline(t, generator, 2, time.Millisecond)

	pipeline.RequestProductImageGeneration(7, "Egg yolk", "yolk", false)
	waitFor(t, "first image", func() bool { generated, _ := pipeline.realtime.counts(); return generated == 1 })

	generator.failing.Store(true)
	pipeline.RequestProductImageGeneration(7, "Egg yolk", "yolk", true)
	waitFor(t, "failure event", func() bool { _, failed := pipeline.realtime.counts(); return failed == 1 })

	if !fileExists(t, pipeline.store.FoodDir(), "7-thumb-v1.webp") {
		t.Fatal("the existing image was lost by a failed replacement")
	}
	if version := pipeline.store.ImageVersion(7); version == nil || *version != 1 {
		t.Fatalf("image version = %v, want 1", version)
	}
	if got := pipeline.readyRuns.Load(); got != 1 {
		t.Fatalf("catalogue version bumps = %d, want 1 (a failure must not bump)", got)
	}
}

func TestPipelineRefusals(t *testing.T) {
	t.Run("generation is not configured", func(t *testing.T) {
		pipeline := newTestPipeline(t, nil, 1, time.Millisecond)
		if got := pipeline.RequestProductImageGeneration(1, "A", "a", true); got != ImageRequestDisabled {
			t.Fatalf("outcome = %q, want disabled", got)
		}
	})

	t.Run("same product already queued, other product finds the queue full", func(t *testing.T) {
		generator := newFakeImageGenerator(t)
		generator.block = make(chan struct{})
		pipeline := newTestPipeline(t, generator, 1, time.Millisecond)
		defer close(generator.block)
		pipeline.mu.Lock()
		pipeline.maxQueueSize = 1
		pipeline.mu.Unlock()

		if got := pipeline.RequestProductImageGeneration(1, "A", "a", true); got != ImageRequestQueued {
			t.Fatalf("first outcome = %q, want queued", got)
		}
		if got := pipeline.RequestProductImageGeneration(1, "A", "a", true); got != ImageRequestAlreadyQueued {
			t.Fatalf("duplicate outcome = %q, want already_queued", got)
		}
		if got := pipeline.RequestProductImageGeneration(2, "B", "b", true); got != ImageRequestQueueFull {
			t.Fatalf("other product outcome = %q, want queue_full", got)
		}
	})
}

func TestPipelineRetryWaitGrowsAndFinalFailureIsReported(t *testing.T) {
	pipeline := newTestPipeline(t, newFakeImageGenerator(t), 3, 10*time.Second)
	task := &queueTask{catalogueID: 1}
	pipeline.mu.Lock()
	pipeline.queue[1] = task
	pipeline.mu.Unlock()

	for attempt, wantWait := range []time.Duration{10 * time.Second, 40 * time.Second} {
		before := time.Now()
		if final := pipeline.recordFailedAttempt(task, io.ErrUnexpectedEOF); final {
			t.Fatalf("attempt %d reported final, want a retry", attempt+1)
		}
		gotWait := task.availableAt.Sub(before)
		if gotWait < wantWait || gotWait > wantWait+time.Second {
			t.Fatalf("attempt %d wait = %v, want about %v", attempt+1, gotWait, wantWait)
		}
	}
	if final := pipeline.recordFailedAttempt(task, io.ErrUnexpectedEOF); !final {
		t.Fatal("third failed attempt not reported final")
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.queue[1] != nil {
		t.Fatal("a permanently failed task stayed in the queue")
	}
}
