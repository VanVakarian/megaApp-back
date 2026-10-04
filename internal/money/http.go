package money

import (
	"net/http"
	"strconv"

	"megaapp-back/internal/auth"
	"megaapp-back/internal/httpx/legacy"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func RegisterRoutes(router chi.Router, authService *auth.Service, handler *Handler) {
	router.Route("/api/money", func(r chi.Router) {
		r.Use(auth.Middleware(authService))

		r.Get("/snapshot", handler.GetSnapshot)

		r.Get("/organizations", handler.GetOrganizations)
		r.Post("/organizations", handler.CreateOrganization)
		r.Put("/organizations/{id}", handler.UpdateOrganization)
		r.Delete("/organizations/{id}", handler.DeleteOrganization)

		r.Get("/currencies", handler.GetCurrencies)
		r.Post("/currencies", handler.CreateCurrency)
		r.Put("/currencies/{id}", handler.UpdateCurrency)
		r.Delete("/currencies/{id}", handler.DeleteCurrency)

		r.Get("/categories", handler.GetCategories)
		r.Post("/categories", handler.CreateCategory)
		r.Put("/categories/{id}", handler.UpdateCategory)
		r.Delete("/categories/{id}", handler.DeleteCategory)

		r.Get("/accounts", handler.GetAccounts)
		r.Post("/accounts", handler.CreateAccount)
		r.Put("/accounts/{id}", handler.UpdateAccount)
		r.Delete("/accounts/{id}", handler.DeleteAccount)

		r.Get("/assets", handler.GetAssets)
		r.Post("/assets", handler.CreateAsset)
		r.Put("/assets/{id}", handler.UpdateAsset)
		r.Delete("/assets/{id}", handler.DeleteAsset)

		r.Get("/transactions", handler.GetTransactions)
		r.Get("/rate-history", handler.GetRateHistory)
		r.Post("/transactions", handler.CreateTransaction)
		r.Put("/transactions/{id}", handler.UpdateTransaction)
		r.Delete("/transactions/{id}", handler.DeleteTransaction)
	})
}

func (h *Handler) GetSnapshot(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetSnapshot(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get money snapshot")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) GetOrganizations(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetOrganizations(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get organizations")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request OrganizationInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	createdID, err := h.service.CreateOrganization(r.Context(), claims.UserID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create organization")
		return
	}

	writeSuccessData(w, http.StatusCreated, map[string]int64{"id": createdID})
}

func (h *Handler) UpdateOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request OrganizationInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateOrganization(r.Context(), claims.UserID, id, request); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update organization")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Organization updated successfully")
}

func (h *Handler) DeleteOrganization(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteOrganization(r.Context(), claims.UserID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete organization")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Organization deleted successfully")
}

func (h *Handler) GetCurrencies(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetCurrencies(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get currencies")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateCurrency(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request CurrencyInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	createdID, err := h.service.CreateCurrency(r.Context(), claims.UserID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create currency")
		return
	}

	writeSuccessData(w, http.StatusCreated, map[string]int64{"id": createdID})
}

func (h *Handler) UpdateCurrency(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request CurrencyInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateCurrency(r.Context(), claims.UserID, id, request); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update currency")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Currency updated successfully")
}

func (h *Handler) DeleteCurrency(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteCurrency(r.Context(), claims.UserID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete currency")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Currency deleted successfully")
}

func (h *Handler) GetCategories(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetCategories(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get categories")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request CategoryInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	createdID, err := h.service.CreateCategory(r.Context(), claims.UserID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create category")
		return
	}

	writeSuccessData(w, http.StatusCreated, map[string]int64{"id": createdID})
}

func (h *Handler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request CategoryInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateCategory(r.Context(), claims.UserID, id, request); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update category")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Category updated successfully")
}

func (h *Handler) DeleteCategory(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteCategory(r.Context(), claims.UserID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete category")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Category deleted successfully")
}

func (h *Handler) GetAccounts(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetAccounts(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get accounts")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request AccountInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	createdID, err := h.service.CreateAccount(r.Context(), claims.UserID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create account")
		return
	}

	writeSuccessData(w, http.StatusCreated, map[string]int64{"id": createdID})
}

func (h *Handler) UpdateAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request AccountInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateAccount(r.Context(), claims.UserID, id, request); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update account")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Account updated successfully")
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteAccount(r.Context(), claims.UserID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete account")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Account deleted successfully")
}

func (h *Handler) GetAssets(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetAssets(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get assets")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateAsset(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request AssetInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	createdID, err := h.service.CreateAsset(r.Context(), claims.UserID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create asset")
		return
	}

	writeSuccessData(w, http.StatusCreated, map[string]int64{"id": createdID})
}

func (h *Handler) UpdateAsset(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request AssetInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}

	if err := h.service.UpdateAsset(r.Context(), claims.UserID, id, request); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update asset")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Asset updated successfully")
}

func (h *Handler) DeleteAsset(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteAsset(r.Context(), claims.UserID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete asset")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Asset deleted successfully")
}

func (h *Handler) GetTransactions(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	response, err := h.service.GetTransactions(r.Context(), claims.UserID)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get transactions")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) GetRateHistory(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	_ = claims
	response, err := h.service.GetRateHistory(r.Context())
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to get money rate history")
		return
	}

	writeSuccessData(w, http.StatusOK, response)
}

func (h *Handler) CreateTransaction(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	var request TransactionInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}
	if request.OperationID == "" {
		writeError(w, http.StatusBadRequest, "operationId is required")
		return
	}

	created, _, err := h.service.CreateTransaction(r.Context(), claims.UserID, request.OperationID, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to create transaction")
		return
	}

	if created.TwinID != nil {
		writeSuccessData(w, http.StatusCreated, map[string]any{"id": created.ID, "twinId": *created.TwinID, "version": created.Version})
		return
	}
	writeSuccessData(w, http.StatusCreated, map[string]any{"id": created.ID, "version": created.Version})
}

func (h *Handler) UpdateTransaction(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request TransactionInput
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}
	if request.OperationID == "" {
		writeError(w, http.StatusBadRequest, "operationId is required")
		return
	}

	newVersion, _, err := h.service.UpdateTransaction(r.Context(), claims.UserID, request.OperationID, id, request)
	if err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to update transaction")
		return
	}

	writeSuccessData(w, http.StatusOK, map[string]any{"version": newVersion})
}

func (h *Handler) DeleteTransaction(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.UserClaimsFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	id, ok := parseIDParam(w, r, "id")
	if !ok {
		return
	}

	var request struct {
		OperationID string `json:"operationId"`
	}
	if err := legacy.DecodeJSON(r, &request); err != nil {
		writeAppError(w, err, http.StatusBadRequest, "Invalid request body")
		return
	}
	if request.OperationID == "" {
		writeError(w, http.StatusBadRequest, "operationId is required")
		return
	}

	if _, err := h.service.DeleteTransaction(r.Context(), claims.UserID, request.OperationID, id); err != nil {
		writeAppError(w, err, http.StatusInternalServerError, "Failed to delete transaction")
		return
	}

	writeSuccessMessage(w, http.StatusOK, "Transaction deleted successfully")
}

func parseIDParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "Invalid id")
		return 0, false
	}
	return id, true
}

func writeSuccessData(w http.ResponseWriter, statusCode int, data any) {
	legacy.WriteJSON(w, statusCode, map[string]any{"success": true, "data": data})
}

func writeSuccessMessage(w http.ResponseWriter, statusCode int, message string) {
	legacy.WriteJSON(w, statusCode, map[string]any{"success": true, "message": message})
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	legacy.WriteJSON(w, statusCode, map[string]any{"success": false, "error": message})
}

func writeAppError(w http.ResponseWriter, err error, fallbackStatus int, fallbackMessage string) {
	writeError(w, legacy.ErrorStatusCodeOf(err, fallbackStatus), legacy.ErrorMessageOf(err, fallbackMessage))
}
