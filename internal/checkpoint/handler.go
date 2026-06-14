package checkpoint

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

const maxUploadBytes = 20 << 20 // 20 MiB per request

// Handler exposes the checkpoint domain over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler builds a checkpoint HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Register mounts checkpoint routes under /checkpoints (all require auth).
// checkinRL is applied only to POST / (the create-checkpoint route).
func (h *Handler) Register(r chi.Router, auth func(http.Handler) http.Handler, checkinRL func(http.Handler) http.Handler) {
	r.Route("/checkpoints", func(r chi.Router) {
		r.Use(auth)
		r.With(checkinRL).Post("/", h.Create)
		r.Get("/", h.ListInBounds) // ?bbox=minLng,minLat,maxLng,maxLat (for the map)
		r.Get("/nearby", h.ListNearby)
		r.Get("/me", h.ListMine)
		r.Get("/by-user", h.ListByUser) // ?user_id={id} — public profile gallery
		// Static route must be registered before /{id} so chi doesn't treat
		// "upload-slots" as a checkpoint ID.
		r.Post("/upload-slots", h.PrepareSlots)
		r.Get("/{id}", h.Get)
		r.Post("/{id}/view", h.View)
		r.Post("/{id}/images", h.UploadImages)
		r.Post("/{id}/upload-urls", h.GetUploadURLs)
		r.Post("/{id}/images/link", h.LinkImages)
		r.Delete("/{id}", h.Delete)
	})
}

type createRequest struct {
	Name      string  `json:"name"`
	Category  string  `json:"category"`
	Note      *string `json:"note"`
	Rating    *int16  `json:"rating"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Address   *string `json:"address"`
	Province  *string `json:"province"`
	PlaceID   *string `json:"place_id"`
	JourneyID *string `json:"journey_id"`
	Images    []struct {
		ObjectKey    string  `json:"object_key"`
		ThumbnailKey *string `json:"thumbnail_key"`
	} `json:"images"`
}

// Create handles a check-in. The request body must include pre-uploaded image keys.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Images) == 0 {
		respond.Error(w, http.StatusBadRequest, "ít nhất 1 ảnh")
		return
	}
	images := make([]ImageKeys, len(req.Images))
	for i, img := range req.Images {
		images[i] = ImageKeys{ObjectKey: img.ObjectKey, ThumbnailKey: img.ThumbnailKey}
	}
	result, err := h.svc.Create(r.Context(), p.UserID, Input{
		Name:      req.Name,
		Category:  req.Category,
		Note:      req.Note,
		Rating:    req.Rating,
		Lat:       req.Lat,
		Lng:       req.Lng,
		Address:   req.Address,
		Province:  req.Province,
		PlaceID:   req.PlaceID,
		JourneyID: req.JourneyID,
		Images:    images,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidInput) {
			respond.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not create checkpoint")
		return
	}
	respond.JSON(w, http.StatusCreated, result)
}

// PrepareSlots issues presigned R2 PUT URLs for the client to upload images
// before creating the checkpoint. Body: { "count": N } (1 ≤ N ≤ 10).
func (h *Handler) PrepareSlots(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Count < 1 || req.Count > maxImages {
		respond.Error(w, http.StatusBadRequest, "count must be 1–10")
		return
	}
	slots, err := h.svc.PrepareSlots(r.Context(), p.UserID, req.Count)
	if err != nil {
		if errors.Is(err, ErrInvalidInput) {
			respond.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not prepare slots")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"slots": slots})
}

// ListMine returns the caller's checkpoints.
func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.svc.ListMine(r.Context(), p.UserID, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not list checkpoints")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"checkpoints": list})
}

// ListByUser returns a user's checkpoints by id (?user_id=...). Public — used
// by the profile gallery when viewing someone else.
func (h *Handler) ListByUser(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		respond.Error(w, http.StatusBadRequest, "user_id is required")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.svc.ListByUser(r.Context(), userID, limit)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not list checkpoints")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"checkpoints": list})
}

// ListInBounds returns checkpoints inside a map viewport (?bbox=...).
func (h *Handler) ListInBounds(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Query().Get("bbox"), ",")
	if len(parts) != 4 {
		respond.Error(w, http.StatusBadRequest, "bbox must be minLng,minLat,maxLng,maxLat")
		return
	}
	b, ok := parseFloats(parts)
	if !ok {
		respond.Error(w, http.StatusBadRequest, "bbox must be 4 numbers")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.svc.ListInBounds(r.Context(), b[0], b[1], b[2], b[3], limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"checkpoints": list})
}

// ListNearby returns checkpoints near a point (?lat&lng&radius).
func (h *Handler) ListNearby(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, errLat := strconv.ParseFloat(q.Get("lat"), 64)
	lng, errLng := strconv.ParseFloat(q.Get("lng"), 64)
	if errLat != nil || errLng != nil {
		respond.Error(w, http.StatusBadRequest, "lat and lng are required numbers")
		return
	}
	radius, _ := strconv.ParseFloat(q.Get("radius"), 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	list, err := h.svc.ListNearby(r.Context(), lng, lat, radius, limit)
	if err != nil {
		h.listError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"checkpoints": list})
}

// Get returns a checkpoint detail (with images + author) by id.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	detail, err := h.svc.GetDetail(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, ErrNotFound) {
		respond.Error(w, http.StatusNotFound, "checkpoint not found")
		return
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "could not load checkpoint")
		return
	}
	respond.JSON(w, http.StatusOK, detail)
}

// View records one view of a checkpoint and returns the new total.
func (h *Handler) View(w http.ResponseWriter, r *http.Request) {
	count, err := h.svc.IncrementView(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			respond.Error(w, http.StatusNotFound, "checkpoint not found")
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not record view")
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"view_count": count})
}

// UploadImages attaches multipart "images" files to a checkpoint the caller owns.
func (h *Handler) UploadImages(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	files := r.MultipartForm.File["images"]
	thumbnails := r.MultipartForm.File["thumbnails"]
	if len(files) == 0 {
		respond.Error(w, http.StatusBadRequest, "at least one 'images' file is required")
		return
	}
	if len(thumbnails) > 0 && len(thumbnails) != len(files) {
		respond.Error(w, http.StatusBadRequest, "'thumbnails' must match 'images' count")
		return
	}

	uploads := make([]ImageUpload, 0, len(files))
	for i, fh := range files {
		f, err := fh.Open()
		if err != nil {
			respond.Error(w, http.StatusBadRequest, "could not read uploaded file")
			return
		}
		defer func() { _ = f.Close() }()
		upload := ImageUpload{ContentType: fh.Header.Get("Content-Type"), Body: f}
		if len(thumbnails) > 0 {
			thumb, err := thumbnails[i].Open()
			if err != nil {
				respond.Error(w, http.StatusBadRequest, "could not read uploaded thumbnail")
				return
			}
			defer func() { _ = thumb.Close() }()
			upload.ThumbnailContentType = thumbnails[i].Header.Get("Content-Type")
			upload.ThumbnailBody = thumb
		}
		uploads = append(uploads, upload)
	}

	images, err := h.svc.AddImages(r.Context(), chi.URLParam(r, "id"), p.UserID, uploads)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			respond.Error(w, http.StatusNotFound, "checkpoint not found")
		case errors.Is(err, ErrUnsupportedFile):
			respond.Error(w, http.StatusUnsupportedMediaType, "ảnh phải là jpeg, png, webp hoặc gif")
		case errors.Is(err, ErrInvalidInput):
			respond.Error(w, http.StatusBadRequest, err.Error())
		default:
			respond.Error(w, http.StatusInternalServerError, "could not upload images")
		}
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{"images": images})
}

// GetUploadURLs issues presigned R2 PUT URLs so the client can upload images
// directly to storage. Body: { "count": N } (1 ≤ N ≤ 3).
func (h *Handler) GetUploadURLs(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Count < 1 || req.Count > maxImages {
		respond.Error(w, http.StatusBadRequest, "count must be 1–10")
		return
	}
	slots, err := h.svc.PrepareUploads(r.Context(), chi.URLParam(r, "id"), p.UserID, req.Count)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			respond.Error(w, http.StatusNotFound, "checkpoint not found")
		case errors.Is(err, ErrInvalidInput):
			respond.Error(w, http.StatusBadRequest, err.Error())
		default:
			respond.Error(w, http.StatusInternalServerError, "could not prepare uploads")
		}
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"slots": slots})
}

// LinkImages records R2 keys that the client uploaded directly. No file bytes
// are received here — only the key strings returned by GetUploadURLs.
// Body: { "images": [{ "object_key": "...", "thumbnail_key": "..." }] }
func (h *Handler) LinkImages(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Images []struct {
			ObjectKey    string  `json:"object_key"`
			ThumbnailKey *string `json:"thumbnail_key"`
		} `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Images) == 0 {
		respond.Error(w, http.StatusBadRequest, "images required")
		return
	}
	keys := make([]ImageKeys, len(req.Images))
	for i, img := range req.Images {
		keys[i] = ImageKeys{ObjectKey: img.ObjectKey, ThumbnailKey: img.ThumbnailKey}
	}
	images, err := h.svc.LinkImages(r.Context(), chi.URLParam(r, "id"), p.UserID, keys)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			respond.Error(w, http.StatusNotFound, "checkpoint not found")
		case errors.Is(err, ErrInvalidInput):
			respond.Error(w, http.StatusBadRequest, err.Error())
		default:
			respond.Error(w, http.StatusInternalServerError, "could not link images")
		}
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{"images": images})
}

func (h *Handler) listError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidInput) {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	respond.Error(w, http.StatusInternalServerError, "could not list checkpoints")
}

func parseFloats(parts []string) ([]float64, bool) {
	out := make([]float64, len(parts))
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

// Delete hard-deletes the caller's checkpoint (images, XP and place side
// effects reversed; an orphaned place is removed too).
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	if err := h.svc.Delete(r.Context(), chi.URLParam(r, "id"), p.UserID); err != nil {
		if errors.Is(err, ErrNotFound) {
			respond.Error(w, http.StatusNotFound, "checkpoint not found")
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not delete checkpoint")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}
