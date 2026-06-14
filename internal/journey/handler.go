package journey

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes the journey domain over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a journey HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts journey routes under /journeys. GET /{id} uses optionalAuth so
// a shared journey is viewable anonymously; the rest require auth.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler) {
	r.Route("/journeys", func(r chi.Router) {
		r.With(optionalAuth).Get("/{id}", h.Get) // shared journey page
		r.With(auth).Post("/", h.Create)
		r.With(auth).Get("/me", h.ListMine)
		r.With(auth).Patch("/{id}", h.Update)
		r.With(auth).Delete("/{id}", h.Delete)
		r.With(auth).Post("/{id}/checkpoints/{cpId}", h.AssignCheckpoint)
	})
}

// Create makes a new journey.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Title       string  `json:"title"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	j, err := h.svc.Create(r.Context(), p.UserID, req.Title, req.Description)
	if err != nil {
		h.writeError(w, err, "could not create journey")
		return
	}
	respond.JSON(w, http.StatusCreated, j)
}

// ListMine returns the caller's journeys.
func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	list, err := h.svc.ListMine(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not list journeys")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"journeys": list})
}

// Get returns a journey detail (stats + stops).
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.svc.GetDetail(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		respond.Error(w, http.StatusNotFound, "journey not found")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load journey")
		return
	}
	respond.JSON(w, http.StatusOK, detail)
}

// Update applies a partial update (title/description/active).
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var in UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	j, err := h.svc.Update(r.Context(), chi.URLParam(r, "id"), p.UserID, in)
	if err != nil {
		h.writeError(w, err, "could not update journey")
		return
	}
	respond.JSON(w, http.StatusOK, j)
}

// Delete soft-deletes the caller's journey.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "id"), p.UserID); err != nil {
		h.writeError(w, err, "could not delete journey")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// AssignCheckpoint attaches a checkpoint to the journey.
func (h *Handler) AssignCheckpoint(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	err := h.svc.AssignCheckpoint(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "cpId"), p.UserID)
	if err != nil {
		h.writeError(w, err, "could not assign checkpoint")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

func (h *Handler) writeError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, ErrNotFound):
		respond.Error(w, http.StatusNotFound, "journey not found")
	case errors.Is(err, ErrInvalidInput):
		respond.Error(w, http.StatusBadRequest, err.Error())
	default:
		respond.Error(w, http.StatusInternalServerError, fallback)
	}
}
