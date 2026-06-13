package place

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

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

// Register mounts place routes under /places (all require auth).
func (h *Handler) Register(r chi.Router, auth func(http.Handler) http.Handler) {
	r.Route("/places", func(r chi.Router) {
		r.Use(auth)
		r.Get("/search", h.Search)
		r.Get("/nearby", h.Nearby)
		r.Get("/popular", h.Popular)
		r.Get("/{id}", h.Get)
	})
	r.With(auth).Get("/discover/feed", h.Feed)
}

// Popular returns top places by check-ins (?lat&lng&limit).
func (h *Handler) Popular(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	var near *Point
	if lat, errLat := strconv.ParseFloat(q.Get("lat"), 64); errLat == nil {
		if lng, errLng := strconv.ParseFloat(q.Get("lng"), 64); errLng == nil {
			near = &Point{Lat: lat, Lng: lng}
		}
	}
	places, err := h.svc.Popular(r.Context(), near, limit)
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

// Search finds places by name (?q=&lat=&lng=&limit=).
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	var near *Point
	if lat, errLat := strconv.ParseFloat(q.Get("lat"), 64); errLat == nil {
		if lng, errLng := strconv.ParseFloat(q.Get("lng"), 64); errLng == nil {
			near = &Point{Lat: lat, Lng: lng}
		}
	}
	places, err := h.svc.Search(r.Context(), q.Get("q"), near, limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"places": places})
}

// Nearby returns places near a point (?lat&lng&radius&limit).
func (h *Handler) Nearby(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	radius, _ := strconv.ParseFloat(q.Get("radius"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	places, err := h.svc.ListNearby(r.Context(), lng, lat, radius, limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"places": places})
}

// Get returns a place detail (aggregate) by id.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.svc.GetDetail(r.Context(), chi.URLParam(r, "id"))
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

func (h *Handler) listError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidInput) {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	respond.Error(w, http.StatusInternalServerError, "could not load places")
}
