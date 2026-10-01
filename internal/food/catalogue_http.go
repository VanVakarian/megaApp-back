package food

import (
	"net/http"
	"strconv"
	"strings"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/httpx/legacy"

	"github.com/go-chi/chi/v5"
)

type CatalogueHandler struct {
	service  *Service
	realtime RealtimePublisher
	metrics  MetricsRecorder
}

type generateProductPreviewRequest struct {
	Description string `json:"description"`
}

type saveProductRequest struct {
	OperationID string  `json:"operationId"`
	ID          *int64  `json:"id"`
	Name        string  `json:"name"`
	Kcals       int64   `json:"kcals"`
	Protein     float64 `json:"protein"`
	Fat         float64 `json:"fat"`
	Carbs       float64 `json:"carbs"`
	Fiber       float64 `json:"fiber"`
	Description string  `json:"description"`
	Archived    *bool   `json:"archived"`
}

type deleteCatalogueEntryRequest struct {
	OperationID string `json:"operationId"`
}

func NewCatalogueHandler(service *Service, realtime RealtimePublisher, metricsRecorder MetricsRecorder) *CatalogueHandler {
	return &CatalogueHandler{service: service, realtime: realtime, metrics: metricsRecorder}
}

func RegisterCatalogueRoutes(router chi.Router, authService *auth.Service, handler *CatalogueHandler) {
	router.With(auth.Middleware(authService)).Get("/api/food/search", handler.SearchCatalogue)
	router.With(auth.Middleware(authService)).Post("/api/food/generate-product-preview", handler.GenerateProductPreview)
	router.With(auth.Middleware(authService)).Post("/api/food/save-product", handler.SaveProduct)
	router.With(auth.Middleware(authService)).Delete("/api/food/catalogue/{catalogueId}", handler.DeleteCatalogueEntry)
}

func (h *CatalogueHandler) SearchCatalogue(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		legacy.WriteResultError(w, http.StatusBadRequest, "Query parameter is required")
		return
	}

	response, err := h.service.SearchCatalogue(r.Context(), query)
	if err != nil {
		legacy.WriteAppResultError(w, err, http.StatusInternalServerError, "Internal server error")
		return
	}

	legacy.WriteJSON(w, http.StatusOK, map[string]any{"result": true, "data": response})
}

func (h *CatalogueHandler) GenerateProductPreview(w http.ResponseWriter, r *http.Request) {
	var request generateProductPreviewRequest
	if err := legacy.DecodeJSON(r, &request); err != nil {
		legacy.WriteAppResultError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	response, err := h.service.GenerateProductPreview(r.Context(), request.Description)
	if err != nil {
		legacy.WriteAppResultError(w, err, http.StatusInternalServerError, "Internal server error")
		return
	}

	legacy.WriteJSON(w, http.StatusOK, map[string]any{"result": true, "data": response})
}

func (h *CatalogueHandler) SaveProduct(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		legacy.WriteMessage(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request saveProductRequest
	if err := legacy.DecodeJSON(r, &request); err != nil {
		legacy.WriteAppResultError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}
	if request.OperationID == "" {
		legacy.WriteResultError(w, http.StatusBadRequest, "operationId is required")
		return
	}

	entry, applied, err := h.service.SaveProduct(r.Context(), claims.UserID, request.OperationID, request.ID, ProductInput{
		Name:        request.Name,
		Kcals:       request.Kcals,
		Protein:     request.Protein,
		Fat:         request.Fat,
		Carbs:       request.Carbs,
		Fiber:       request.Fiber,
		Description: request.Description,
		Archived:    request.Archived,
	})
	if err != nil {
		legacy.WriteAppResultError(w, err, http.StatusInternalServerError, "Internal server error")
		return
	}

	statusCode := http.StatusCreated
	if request.ID != nil {
		statusCode = http.StatusOK
	}
	if applied && entry != nil {
		h.realtime.MarkUserUpdated(claims.UserID)
		h.realtime.PublishCatalogueEntrySaved(claims.UserID, *entry, extractClientID(r))
		if request.ID != nil {
			h.metrics.Increment(MetricCatalogueEntryUpdated)
		} else {
			h.metrics.Increment(MetricCatalogueEntryCreated)
		}
	}
	legacy.WriteJSON(w, statusCode, map[string]any{"result": true, "data": map[string]any{"catalogueEntry": entry}})
}

func (h *CatalogueHandler) DeleteCatalogueEntry(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		legacy.WriteMessage(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	catalogueID, err := strconv.ParseInt(chi.URLParam(r, "catalogueId"), 10, 64)
	if err != nil {
		legacy.WriteResultError(w, http.StatusBadRequest, "Invalid catalogueId")
		return
	}

	var request deleteCatalogueEntryRequest
	if err := legacy.DecodeJSON(r, &request); err != nil {
		legacy.WriteAppResultError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}
	if request.OperationID == "" {
		legacy.WriteResultError(w, http.StatusBadRequest, "operationId is required")
		return
	}

	deleted, applied, err := h.service.DeleteProduct(r.Context(), claims.UserID, request.OperationID, catalogueID)
	if err != nil {
		legacy.WriteAppResultError(w, err, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !deleted {
		legacy.WriteResultError(w, http.StatusNotFound, "product not found")
		return
	}

	if applied {
		h.realtime.MarkUserUpdated(claims.UserID)
		h.realtime.PublishCatalogueEntryDeleted(catalogueID, extractClientID(r))
		h.metrics.Increment(MetricCatalogueEntryDeleted)
	}
	legacy.WriteJSON(w, http.StatusOK, map[string]any{"result": true, "data": map[string]any{"catalogueId": catalogueID}})
}
