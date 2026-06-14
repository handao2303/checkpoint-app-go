package reaction

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes checkpoint reactions over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a reaction HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts reaction routes. The summary read uses optionalAuth so shared
// checkpoint pages show counts anonymously (mine=null); set/remove require auth.
// Full paths are used with r.With(...) rather than r.Route("/checkpoints") so they
// don't collide with the checkpoint handler's own /checkpoints subrouter.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler) {
	r.With(optionalAuth).Get("/checkpoints/{id}/reactions", h.Summary)
	r.With(auth).Post("/checkpoints/{id}/reaction", h.Set)
	r.With(auth).Delete("/checkpoints/{id}/reaction", h.Remove)
}

// Summary returns the reaction counts and the caller's own reaction.
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	s, err := h.svc.Summary(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load reactions")
		return
	}
	respond.JSON(w, http.StatusOK, s)
}

// Set records (or switches) the caller's reaction. Body: {"reaction":"love"}.
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Reaction string `json:"reaction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	s, err := h.svc.Set(r.Context(), chi.URLParam(r, "id"), p.UserID, req.Reaction)
	switch {
	case err == nil:
		respond.JSON(w, http.StatusOK, s)
	case errors.Is(err, ErrInvalidReaction):
		respond.Error(w, http.StatusBadRequest, "invalid reaction")
	default:
		respond.Error(w, http.StatusInternalServerError, "could not save reaction")
	}
}

// Remove clears the caller's reaction.
func (h *Handler) Remove(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	s, err := h.svc.Remove(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not remove reaction")
		return
	}
	respond.JSON(w, http.StatusOK, s)
}
