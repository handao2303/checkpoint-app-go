package gamification

import (
	"fmt"
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
		r.Post("/me/badges/claim-unsent", h.ClaimUnsentBadges)
	})
	// Leaderboard is public; the caller's rank ("Me") is only included when signed in.
	r.With(optionalAuth).Get("/leaderboard", h.Leaderboard)
	r.With(optionalAuth).Get("/users/{id}/passport", h.UserPassport)
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

// ClaimUnsentBadges returns newly eligible badges that have not been shown yet,
// then marks them as sent.
func (h *Handler) ClaimUnsentBadges(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	badges, err := h.svc.ClaimUnsentBadges(r.Context(), p.UserID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not claim unsent badges")
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

// UserPassport returns a specific user's province stamps by id (preferred) or
// username (legacy).
func (h *Handler) UserPassport(w http.ResponseWriter, r *http.Request) {
	ref := chi.URLParam(r, "id")
	passport, err := h.svc.PassportByRef(r.Context(), ref)
	if err != nil {
		fmt.Println("error fetching passport for user", ref, ":", err)
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}
	respond.JSON(w, http.StatusOK, passport)
}
