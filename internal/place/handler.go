package place

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

// Handler exposes the place (discovery) domain over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a place HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts place routes under /places. The discover for-you feed and a
// place detail use optionalAuth so they're viewable anonymously (public discover
// + shared pages); the following feed and search/popular/nearby require auth.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler) {
	r.Route("/places", func(r chi.Router) {
		r.With(optionalAuth).Get("/search", h.Search)
		r.With(optionalAuth).Get("/nearby", h.Nearby)
		r.With(optionalAuth).Get("/popular", h.Popular)
		r.With(optionalAuth).Get("/{id}", h.Get)
	})
	r.With(optionalAuth).Get("/discover/feed", h.Feed)
	r.With(auth).Get("/discover/feed/following", h.FeedFollowing)
}

// Popular returns top places by check-ins (?lat&lng&limit).
func (h *Handler) Popular(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	viewerID := optionalViewerID(r)
	var near *Point
	if lat, errLat := strconv.ParseFloat(q.Get("lat"), 64); errLat == nil {
		if lng, errLng := strconv.ParseFloat(q.Get("lng"), 64); errLng == nil {
			near = &Point{Lat: lat, Lng: lng}
		}
	}
	places, err := h.svc.Popular(r.Context(), near, limit, viewerID)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"places": places})
}

// Feed returns community check-ins newest → oldest (?lat&lng&cursor&limit).
func (h *Handler) Feed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	feed, err := h.svc.Feed(r.Context(), lat, lng, q.Get("cursor"), limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, feed)
}

// FeedFollowing returns the caller's following-only feed (?lat&lng&cursor&limit).
func (h *Handler) FeedFollowing(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	feed, err := h.svc.FeedFollowing(r.Context(), p.UserID, lat, lng, q.Get("cursor"), limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, feed)
}

// Search finds places by name (?q=&lat=&lng=&limit=).
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	viewerID := optionalViewerID(r)
	var near *Point
	if lat, errLat := strconv.ParseFloat(q.Get("lat"), 64); errLat == nil {
		if lng, errLng := strconv.ParseFloat(q.Get("lng"), 64); errLng == nil {
			near = &Point{Lat: lat, Lng: lng}
		}
	}
	places, err := h.svc.Search(r.Context(), q.Get("q"), near, limit, viewerID)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"places": places})
}

// Nearby returns places near a point (?lat&lng&limit&offset).
func (h *Handler) Nearby(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	viewerID := optionalViewerID(r)
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter := NearbyFilter{Category: q.Get("category"), Sort: q.Get("sort")}
	places, err := h.svc.ListNearby(r.Context(), lng, lat, limit, offset, viewerID, filter)
	if err != nil {
		h.listError(w, err)
		return
	}
	// Total (cached) so the list can show an accurate count without the client
	// guessing from loaded pages. Non-fatal: fall back to omitting it on error.
	total, err := h.svc.CountNearby(r.Context(), filter.Category)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]any{"places": places})
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"places": places, "total": total})
}

// Get returns a place detail (aggregate) by id.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	detail, err := h.svc.GetDetail(r.Context(), chi.URLParam(r, "id"), limit, offset, optionalViewerID(r))
	if errors.Is(err, ErrNotFound) {
		respond.Error(w, http.StatusNotFound, "place not found")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load place")
		return
	}
	respond.JSON(w, http.StatusOK, detail)
}

func optionalViewerID(r *http.Request) *string {
	if p, ok := authctx.From(r.Context()); ok && p.UserID != "" {
		return &p.UserID
	}
	return nil
}

func (h *Handler) listError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidInput) {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	respond.Error(w, http.StatusInternalServerError, "could not load places")
}
