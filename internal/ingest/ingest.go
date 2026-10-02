package ingest

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/config"
	"megaapp-back/internal/httpx/legacy"
	"megaapp-back/internal/platform/rotatingfile"

	"github.com/go-chi/chi/v5"
)

const (
	routePrefix    = "/api/ingest/"
	keyHeader      = "X-Ingest-Key"
	clientIDHeader = "X-Client-ID"
	dirName        = "ingest"
)

type Handler struct {
	logger    *slog.Logger
	keyHashes [][sha256.Size]byte
	sources   []*source
}

type source struct {
	name   string
	writer *rotatingfile.Writer
}

func NewHandler(dataDir string, sources []config.IngestSource, keys []string, logger *slog.Logger) *Handler {
	handler := &Handler{logger: logger}
	for _, key := range keys {
		handler.keyHashes = append(handler.keyHashes, sha256.Sum256([]byte(key)))
	}
	for _, configured := range sources {
		dir := filepath.Join(dataDir, dirName, configured.Name)
		handler.sources = append(handler.sources, &source{
			name:   configured.Name,
			writer: rotatingfile.New(dir, configured.Name, configured.RotateBytes),
		})
		logger.Info("ingest_source_configured", "source", configured.Name, "rotateBytes", configured.RotateBytes, "dir", dir)
	}
	return handler
}

func RegisterRoutes(router chi.Router, authService *auth.Service, handler *Handler) {
	for _, configured := range handler.sources {
		router.Route(routePrefix+configured.name, func(r chi.Router) {
			r.Use(handler.keyOrSession(authService))
			r.Post("/", handler.post(configured))
		})
	}
}

func (h *Handler) Close() error {
	var errs []error
	for _, configured := range h.sources {
		errs = append(errs, configured.writer.Close())
	}
	return errors.Join(errs...)
}

func (h *Handler) acceptsKey(candidate string) bool {
	sum := sha256.Sum256([]byte(candidate))
	matched := 0
	for _, hash := range h.keyHashes {
		matched |= subtle.ConstantTimeCompare(sum[:], hash[:])
	}
	return matched == 1
}

func (h *Handler) keyOrSession(authService *auth.Service) func(http.Handler) http.Handler {
	session := auth.Middleware(authService)
	return func(next http.Handler) http.Handler {
		viaSession := session(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.acceptsKey(r.Header.Get(keyHeader)) {
				next.ServeHTTP(w, r)
				return
			}
			viaSession.ServeHTTP(w, r)
		})
	}
}

func (h *Handler) post(s *source) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var user *int64
		if identity, ok := auth.IdentityFromContext(r.Context()); ok {
			user = &identity.UserID
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				legacy.WriteDetail(w, http.StatusRequestEntityTooLarge, "Request too large")
				return
			}
			legacy.WriteDetail(w, http.StatusBadRequest, "Invalid request body")
			return
		}

		var batch request
		if err := json.Unmarshal(body, &batch); err != nil {
			legacy.WriteDetail(w, http.StatusBadRequest, "Invalid request body")
			return
		}
		if len(batch.Events) == 0 || len(batch.Events) > maxBatchEvents || batch.Dropped < 0 {
			legacy.WriteDetail(w, http.StatusBadRequest, "Invalid events batch")
			return
		}

		client := r.Header.Get(clientIDHeader)
		if !clientIDPattern.MatchString(client) {
			client = ""
		}

		lines, stored, rejected := encodeBatch(batch.Events, batch.Dropped, origin{
			source:     s.name,
			client:     client,
			user:       user,
			receivedAt: time.Now(),
		})
		if len(rejected) > 0 {
			h.logger.Warn("ingest_events_rejected", "source", s.name, "rejected", len(rejected), "firstCode", rejected[0].Code)
		}

		if stored > 0 {
			if err := s.writer.Append(lines); err != nil {
				h.logger.Error("ingest_store_failed", "source", s.name, "error", err)
				legacy.WriteDetail(w, http.StatusInternalServerError, "Failed to store events")
				return
			}
		}

		legacy.WriteJSON(w, http.StatusOK, response{Received: len(batch.Events), Stored: stored, Rejected: rejected})
	}
}
