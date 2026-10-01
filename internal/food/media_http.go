package food

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type ImageHandler struct {
	store *ImageStore
}

func NewImageHandler(store *ImageStore) *ImageHandler {
	return &ImageHandler{store: store}
}

func RegisterImageRoutes(router chi.Router, handler *ImageHandler) {
	if handler == nil {
		return
	}
	router.Get("/api/images/food/{filename}", handler.ServeFoodImage)
}

func (h *ImageHandler) ServeFoodImage(w http.ResponseWriter, r *http.Request) {
	filename := strings.TrimSpace(chi.URLParam(r, "filename"))
	path, err := h.store.VariantPath(filename)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
