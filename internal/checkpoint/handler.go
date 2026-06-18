package checkpoint

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/quanluon/checkpoint-app/internal/cloudsync"
	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

const maxUploadBytes = 20 << 20 // 20 MiB per request

// MediaPublisher enqueues a media-processing job for a checkpoint (Pub/Sub in
// production). Nil when Pub/Sub isn't configured (local dev → inline fallback).
type MediaPublisher interface {
	PublishMedia(ctx context.Context, checkpointID, userID string) error
}

// Handler exposes the checkpoint domain over HTTP.
type Handler struct {
	svc       *Service
	syncer    *cloudsync.Syncer // nil when no cloud sync is configured
	publisher MediaPublisher    // nil when Pub/Sub isn't configured
	processor *Processor        // inline media processing for local-dev fallback
}

// NewHandler builds a checkpoint HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// WithMedia attaches the async media pipeline: a publisher (Pub/Sub) and a
// processor used as a local-dev fallback when no publisher is configured.
func (h *Handler) WithMedia(publisher MediaPublisher, processor *Processor) *Handler {
	h.publisher = publisher
	h.processor = processor
	return h
}

// dispatchMedia kicks off processing of a checkpoint's pending media. Prefers
// Pub/Sub (worker has guaranteed CPU); falls back to inline goroutine processing
// when no publisher is configured or publishing fails.
func (h *Handler) dispatchMedia(ctx context.Context, checkpointID, userID string) {
	if h.publisher != nil {
		if err := h.publisher.PublishMedia(ctx, checkpointID, userID); err == nil {
			return
		} else {
			log.Printf("publish media job for %s: %v", checkpointID, err)
		}
	}
	if h.processor != nil {
		go h.processInline(checkpointID)
	}
}

func (h *Handler) processInline(checkpointID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := h.processor.ProcessCheckpoint(ctx, checkpointID); err != nil {
		log.Printf("inline media processing %s: %v", checkpointID, err)
	}
}

// WithDriveSyncer is kept for compatibility; use WithCloudSyncer going forward.
// Deprecated: wire via WithCloudSyncer instead.

// WithCloudSyncer attaches the fan-out cloud syncer to the handler.
func (h *Handler) WithCloudSyncer(s *cloudsync.Syncer) *Handler {
	h.syncer = s
	return h
}

// Register mounts checkpoint routes under /checkpoints. Most require auth;
// the share-page reads ({id}, by-user, {id}/view) use optionalAuth so anonymous
// visitors can view shared checkpoints. checkinRL is applied only to POST /.
func (h *Handler) Register(r chi.Router, auth, optionalAuth func(http.Handler) http.Handler, checkinRL, uploadSlotsRL func(http.Handler) http.Handler) {
	r.Route("/checkpoints", func(r chi.Router) {
		// Public (optional auth) — shared checkpoint pages.
		r.With(optionalAuth).Get("/by-user", h.ListByUser) // ?user_id={id} — profile gallery
		r.With(optionalAuth).Get("/{id}", h.Get)
		r.With(optionalAuth).Post("/{id}/view", h.View)

		// Private (require auth).
		r.With(auth, checkinRL).Post("/", h.Create)
		r.With(auth).Get("/", h.ListInBounds) // ?bbox=minLng,minLat,maxLng,maxLat (map)
		r.With(auth).Get("/nearby", h.ListNearby)
		r.With(auth).Get("/me", h.ListMine)
		r.With(auth, uploadSlotsRL).Post("/upload-slots", h.PrepareSlots)
		r.With(auth).Post("/{id}/images", h.UploadImages)
		r.With(auth).Post("/{id}/upload-urls", h.GetUploadURLs)
		r.With(auth).Post("/{id}/images/link", h.LinkImages)
		r.With(auth).Delete("/{id}", h.Delete)
	})
}

type createRequest struct {
	Name         string  `json:"name"`
	Category     string  `json:"category"`
	Note         *string `json:"note"`
	Rating       *int16  `json:"rating"`
	Lat          float64 `json:"lat"`
	Lng          float64 `json:"lng"`
	Address      *string `json:"address"`
	Province     *string `json:"province"`
	ProvinceCode *string `json:"province_code"`
	PlaceID      *string `json:"place_id"`
	JourneyID    *string `json:"journey_id"`
	Images       []struct {
		ObjectKey    string  `json:"object_key"`
		ThumbnailKey *string `json:"thumbnail_key"`
		Type         string  `json:"type"`
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
		respond.Error(w, http.StatusBadRequest, "ít nhất 1 ảnh hoặc video")
		return
	}
	images := make([]ImageKeys, len(req.Images))
	for i, img := range req.Images {
		images[i] = ImageKeys{ObjectKey: img.ObjectKey, ThumbnailKey: img.ThumbnailKey, Type: img.Type}
	}
	result, err := h.svc.Create(r.Context(), p.UserID, Input{
		Name:         req.Name,
		Category:     req.Category,
		Note:         req.Note,
		Rating:       req.Rating,
		Lat:          req.Lat,
		Lng:          req.Lng,
		Address:      req.Address,
		Province:     req.Province,
		ProvinceCode: req.ProvinceCode,
		PlaceID:      req.PlaceID,
		JourneyID:    req.JourneyID,
		Images:       images,
	})
	if err != nil {
		if errors.Is(err, ErrInvalidInput) {
			respond.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		respond.Error(w, http.StatusInternalServerError, "could not create checkpoint")
		return
	}

	// Kick off async media processing if any item still needs it (raw video
	// awaiting transcode). The worker (or local fallback) sets thumbnails + cover.
	mediaPending := false
	for _, img := range result.Images {
		if img.Pending {
			mediaPending = true
			break
		}
	}
	if mediaPending {
		h.dispatchMedia(r.Context(), result.Checkpoint.ID, p.UserID)
	}

	if len(result.Images) > 0 {
		media := make([]cloudsync.Media, 0, len(result.Images))
		for _, img := range result.Images {
			if img.Pending {
				continue // raw media not transcoded yet — don't sync a broken file
			}
			media = append(media, cloudsync.Media{URL: img.URL, Type: img.Type})
		}
		if h.syncer != nil && len(media) > 0 {
			cp := result.Checkpoint
			h.syncer.SyncCheckpoint(p.UserID, cloudsync.CheckpointMeta{
				ID: cp.ID, Name: cp.Name, Lat: cp.Lat, Lng: cp.Lng,
				Address: cp.Address, Rating: cp.Rating, Note: cp.Note,
				Category: cp.Category, CreatedAt: cp.CreatedAt, Media: media,
			})
		}
	}

	respond.JSON(w, http.StatusCreated, result)
}

// PrepareSlots issues presigned R2 PUT URLs for the client to upload media
// before creating the checkpoint. New body: { "media": [{"type":"image"}] }.
// Legacy body { "count": N } is treated as N images.
func (h *Handler) PrepareSlots(w http.ResponseWriter, r *http.Request) {
	p, _ := authctx.From(r.Context())
	var req struct {
		Count int `json:"count"`
		Media []struct {
			Type string `json:"type"`
		} `json:"media"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	count := req.Count
	types := make([]string, 0, len(req.Media))
	if len(req.Media) > 0 {
		count = len(req.Media)
		for _, item := range req.Media {
			types = append(types, item.Type)
		}
	}
	slots, err := h.svc.PrepareSlots(r.Context(), p.UserID, count, types)
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
	if r.URL.Query().Get("journey") == "none" {
		var cursor *CheckpointCursor
		if raw := r.URL.Query().Get("cursor"); raw != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err != nil {
				respond.Error(w, http.StatusBadRequest, "invalid cursor")
				return
			}
			parts := strings.SplitN(string(decoded), "|", 2)
			if len(parts) != 2 {
				respond.Error(w, http.StatusBadRequest, "invalid cursor")
				return
			}
			cursor = &CheckpointCursor{CreatedAt: parts[0], ID: parts[1]}
		}
		page, err := h.svc.ListMineUnassigned(r.Context(), p.UserID, limit, cursor)
		if err != nil {
			respond.Error(w, http.StatusInternalServerError, "could not list checkpoints")
			return
		}
		if page.NextCursor != nil {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(*page.NextCursor))
			page.NextCursor = &encoded
		}
		respond.JSON(w, http.StatusOK, page)
		return
	}
	provinceCode := r.URL.Query().Get("province_code")
	list, err := h.svc.ListMine(r.Context(), p.UserID, provinceCode, limit)
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
	provinceCode := r.URL.Query().Get("province_code")
	list, err := h.svc.ListByUser(r.Context(), userID, provinceCode, limit)
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
		respond.Error(w, http.StatusBadRequest, "count must be 1–3")
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
			Type         string  `json:"type"`
		} `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Images) == 0 {
		respond.Error(w, http.StatusBadRequest, "images required")
		return
	}
	keys := make([]ImageKeys, len(req.Images))
	for i, img := range req.Images {
		keys[i] = ImageKeys{ObjectKey: img.ObjectKey, ThumbnailKey: img.ThumbnailKey, Type: MediaTypeImage}
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
