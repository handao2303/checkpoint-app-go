package gamification

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes the gamification domain over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts gamification routes behind the auth middleware.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(auth)
		r.Get("/me/passport", h.Passport)
		r.Get("/me/badges", h.Badges)
		r.Get("/leaderboard", h.Leaderboard)
	})
	r.With(optionalAuth).Get("/users/{username}/passport", h.UserPassport)
}

// Passport returns the caller's province stamps.
func (h *Handler) Passport(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	passport, err := h.svc.Passport(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load passport")
		return
	}
	respond.JSON(w, http.StatusOK, passport)
}

// Badges returns the badge catalog with the caller's unlock state.
func (h *Handler) Badges(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	badges, err := h.svc.Badges(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load badges")
		return
	}
	respond.JSON(w, http.StatusOK, badges)
}

// Leaderboard returns the global top users plus the caller's rank.
func (h *Handler) Leaderboard(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	board, err := h.svc.Leaderboard(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load leaderboard")
		return
	}
	respond.JSON(w, http.StatusOK, board)
}

// UserPassport returns a specific user's province stamps by username.
func (h *Handler) UserPassport(w http.ResponseWriter, r *http.Request) {
	username := chi.URLParam(r, "username")
	passport, err := h.svc.PassportByUsername(r.Context(), username)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, passport)
}
