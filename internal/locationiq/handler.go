package locationiq

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes the place suggestion endpoint.
type Handler struct {
	client *Client
}

// NewHandler builds the handler.
func NewHandler(client *Client) *Handler {
	return &Handler{client: client}
}

// Register mounts GET /places/suggest (requires auth).
func (h *Handler) Register(r chi.Router, auth func(http.Handler) http.Handler) {
	r.With(auth).Get("/places/suggest", h.Suggest)
}

// Suggest returns nearby place suggestions for check-in auto-fill.
// Returns an empty JSON array on missing API key, quota exhaustion, or any error.
func (h *Handler) Suggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	suggestions := h.client.Search(r.Context(), lat, lng)
	respond.JSON(w, http.StatusOK, suggestions)
}
