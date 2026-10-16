package food

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"megaapp-back/internal/auth"

	"github.com/go-chi/chi/v5"
)

const productHistoryMaxCatalogueIDs = 20

type Handler struct {
	service  *Service
	realtime RealtimePublisher
}

func NewHandler(service *Service, realtime RealtimePublisher) *Handler {
	return &Handler{service: service, realtime: realtime}
}

func RegisterRoutes(router chi.Router, authService *auth.Service, handler *Handler) {
	router.Route("/api/food", func(r chi.Router) {
		r.Use(auth.Middleware(authService))
		r.Get("/diary-full-update", handler.GetDiaryFullUpdate)
		r.Get("/catalogue", handler.GetCatalogue)
		r.Get("/catalogue/version", handler.GetCatalogueVersion)
		r.Get("/catalogue/{catalogueId}", handler.GetCatalogueEntry)
		r.Get("/personal-kcals", handler.GetPersonalKcals)
		r.Get("/stats", handler.GetStats)
		r.Get("/product-history", handler.GetProductHistory)
	})
}

func (h *Handler) GetDiaryFullUpdate(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
		return
	}

	dateISO := strings.TrimSpace(r.URL.Query().Get("date"))
	if dateISO == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "date is required"})
		return
	}
	offset, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("offset")))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "offset is required"})
		return
	}

	response, err := h.service.GetDiaryFullUpdate(r.Context(), claims.UserID, dateISO, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) GetCatalogue(w http.ResponseWriter, r *http.Request) {
	response, err := h.service.GetCatalogue(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"version": h.service.CatalogueVersion(), "entries": response})
}

// GetCatalogueVersion is a cheap "did the catalogue change" check for reconnect catch-up — the
// coordinator calls this instead of re-downloading the whole catalogue on every reconnect.
func (h *Handler) GetCatalogueVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": h.service.CatalogueVersion()})
}

func (h *Handler) GetCatalogueEntry(w http.ResponseWriter, r *http.Request) {
	catalogueID, err := strconv.ParseInt(chi.URLParam(r, "catalogueId"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"result": false, "error": "Invalid catalogueId"})
		return
	}

	response, err := h.service.GetCatalogueEntry(r.Context(), catalogueID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"result": false, "error": err.Error()})
		return
	}
	if response == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"result": false, "error": "Product not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": true, "data": response})
}

func (h *Handler) GetPersonalKcals(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
		return
	}

	response, err := h.service.GetPersonalKcalsNow(r.Context(), claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"result": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": true, "data": response})
}

func (h *Handler) GetStats(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
		return
	}

	response, err := h.service.GetStats(r.Context(), claims.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to get stats"})
		return
	}

	// Default response is windowed to a recent range — full history is a deliberate opt-in
	// (?from=all), same principle as diary segments: the wide, rare request stays explicit.
	if strings.TrimSpace(r.URL.Query().Get("from")) != "all" {
		response = h.service.TrimStatsToRecentWindow(response)
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) GetProductHistory(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Unauthorized"})
		return
	}

	catalogueIDs, err := parseCatalogueIDs(r.URL.Query().Get("catalogueIds"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"result": false, "error": err.Error()})
		return
	}
	if len(catalogueIDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"result": false, "error": "catalogueIds is required"})
		return
	}

	if len(catalogueIDs) > productHistoryMaxCatalogueIDs {
		writeJSON(w, http.StatusBadRequest, map[string]any{"result": false, "error": "Too many catalogueIds"})
		return
	}

	response, err := h.service.GetProductHistory(r.Context(), claims.UserID, catalogueIDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"result": false, "error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": true, "data": response})
}

func parseCatalogueIDs(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	result := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid catalogueIds")
		}
		result = append(result, id)
	}
	return result, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}
