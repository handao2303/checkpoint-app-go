package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// Service errors.
var (
	ErrInvalidInput    = errors.New("invalid input")
	ErrUnsupportedFile = errors.New("unsupported file type")
)

// checkinXP is the XP awarded per check-in (Phase 2 basic).
const checkinXP = 10

const (
	defaultListLimit = 20
	maxListLimit     = 100
	maxMapLimit      = 500
	defaultRadiusM   = 5000
	maxRadiusM       = 50000
	maxImages        = 3
	maxVideos        = 1
	maxMedia         = maxImages + maxVideos
	MediaTypeImage   = "image"
	MediaTypeVideo   = "video"
)

// repository is the data layer the service depends on (consumer-side interface
// for easy mocking).
type repository interface {
	Create(ctx context.Context, p CreateParams) (*CreateResult, error)
	GetByID(ctx context.Context, id string) (*Checkpoint, error)
	GetDetail(ctx context.Context, id string) (*Detail, error)
	Update(ctx context.Context, id, userID string, p UpdateParams) (*Checkpoint, error)
	ListByUser(ctx context.Context, userID string, provinceCode string, limit int) ([]*Checkpoint, error)
	ListByUserPage(ctx context.Context, userID string, provinceCode string, limit int, cursor *CheckpointCursor) ([]*Checkpoint, error)
	ListUnassignedByUser(ctx context.Context, userID string, limit int, cursor *CheckpointCursor) ([]*Checkpoint, error)
	ListInBounds(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit int) ([]*Checkpoint, error)
	ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Checkpoint, error)
	AddImages(ctx context.Context, checkpointID string, keys []ImageKeys, startOrder int) ([]Image, error)
	CountImages(ctx context.Context, checkpointID string) (int, error)
	Delete(ctx context.Context, id, userID string) (imageKeys []string, err error)
	IncrementView(ctx context.Context, id string) (int, error)
}

// fileStorage stores and removes image objects.
type fileStorage interface {
	Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error)
	Delete(ctx context.Context, key string) error
	// PresignUpload returns a presigned PUT URL valid for ttlSeconds, allowing
	// browsers to upload directly to R2 without routing through this server.
	PresignUpload(key string, ttlSeconds int) string
}

// journeyValidator checks that a chosen journey belongs to the user, so a
// check-in can only be attached to one of the user's own journeys.
type journeyValidator interface {
	BelongsToUser(ctx context.Context, userID, journeyID string) (bool, error)
}

// Service holds the checkpoint business logic.
type Service struct {
	repo     repository
	storage  fileStorage
	journeys journeyValidator
}

// NewService builds the checkpoint service.
func NewService(repo repository, storage fileStorage, journeys journeyValidator) *Service {
	return &Service{repo: repo, storage: storage, journeys: journeys}
}

// Input is the user-supplied data for a check-in (before validation).
type Input struct {
	Name         string
	Category     string
	Note         *string
	Rating       *int16
	Lat          float64
	Lng          float64
	Address      *string
	Province     *string
	ProvinceCode *string
	PlaceID      *string
	JourneyID    *string
	// Images are media keys already uploaded directly to R2. Required (≥1).
	Images []ImageKeys
}

// UpdateInput is the editable checkpoint content surface.
type UpdateInput struct {
	Name   string
	Note   *string
	Rating *int16
}

// CheckpointCursor is a keyset cursor for checkpoint lists ordered by newest
// first: created_at DESC, id DESC.
type CheckpointCursor struct {
	CreatedAt string
	ID        string
}

// CheckpointPage is a paginated checkpoint response.
type CheckpointPage struct {
	Checkpoints []*Checkpoint `json:"checkpoints"`
	NextCursor  *string       `json:"next_cursor,omitempty"`
}

// Create validates input and creates a checkpoint, awarding XP.
func (s *Service) Create(ctx context.Context, userID string, in Input) (*CreateResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if in.Lat < -90 || in.Lat > 90 || in.Lng < -180 || in.Lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		return nil, fmt.Errorf("%w: rating must be 1..5", ErrInvalidInput)
	}
	if err := validateMediaKeys(in.Images); err != nil {
		return nil, err
	}
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = "other"
	}

	// Attach to the journey the user picked (the client chooses one of their
	// active journeys). Validate ownership so a check-in can't be slipped into
	// someone else's journey; if it isn't theirs, just leave it unattached.
	journeyID := in.JourneyID
	if journeyID != nil && s.journeys != nil {
		if ok, err := s.journeys.BelongsToUser(ctx, userID, *journeyID); err != nil || !ok {
			journeyID = nil
		}
	}

	// Address and province are resolved by the client (lib/bigdatacloud.ts) before
	// submit, so the server just sanitizes and stores what it receives.
	address := in.Address
	if address != nil {
		trimmed := strings.TrimSpace(*address)
		if trimmed == "" {
			address = nil
		} else {
			address = &trimmed
		}
	}

	var province *string
	if in.Province != nil {
		if p := strings.TrimSpace(*in.Province); p != "" {
			province = &p
		}
	}

	var provinceCode *string
	if in.ProvinceCode != nil {
		if p := strings.TrimSpace(*in.ProvinceCode); p != "" {
			provinceCode = &p
		}
	}

	return s.repo.Create(ctx, CreateParams{
		UserID:       userID,
		Name:         name,
		Category:     category,
		Note:         in.Note,
		Rating:       in.Rating,
		Lat:          in.Lat,
		Lng:          in.Lng,
		Address:      address,
		Province:     province,
		ProvinceCode: provinceCode,
		PlaceID:      in.PlaceID,
		JourneyID:    journeyID,
		XPReward:     checkinXP,
		Images:       in.Images,
	})
}

// GetDetail returns a checkpoint with images and author.
func (s *Service) GetDetail(ctx context.Context, id string) (*Detail, error) {
	return s.repo.GetDetail(ctx, id)
}

// Update edits a caller-owned checkpoint's content. It deliberately does not
// touch location, media, place, journey, XP, or counters.
func (s *Service) Update(ctx context.Context, id, userID string, in UpdateInput) (*Checkpoint, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if in.Rating != nil && (*in.Rating < 1 || *in.Rating > 5) {
		return nil, fmt.Errorf("%w: rating must be 1..5", ErrInvalidInput)
	}
	var note *string
	if in.Note != nil {
		if trimmed := strings.TrimSpace(*in.Note); trimmed != "" {
			note = &trimmed
		}
	}
	return s.repo.Update(ctx, id, userID, UpdateParams{Name: name, Note: note, Rating: in.Rating})
}

// IncrementView records one view of a checkpoint and returns the new total.
func (s *Service) IncrementView(ctx context.Context, id string) (int, error) {
	return s.repo.IncrementView(ctx, id)
}

// ListInBounds returns checkpoints inside a map viewport bounding box.
func (s *Service) ListInBounds(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit int) ([]*Checkpoint, error) {
	if minLng > maxLng || minLat > maxLat {
		return nil, fmt.Errorf("%w: bbox min must be <= max", ErrInvalidInput)
	}
	if limit <= 0 || limit > maxMapLimit {
		limit = maxMapLimit
	}
	return s.repo.ListInBounds(ctx, minLng, minLat, maxLng, maxLat, limit)
}

// ListNearby returns checkpoints near a point (radius clamped).
func (s *Service) ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Checkpoint, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	if radiusM <= 0 {
		radiusM = defaultRadiusM
	}
	if radiusM > maxRadiusM {
		radiusM = maxRadiusM
	}
	if limit <= 0 || limit > maxMapLimit {
		limit = maxMapLimit
	}
	return s.repo.ListNearby(ctx, lng, lat, radiusM, limit)
}

// ImageUpload is one image to attach to a checkpoint.
type ImageUpload struct {
	ContentType          string
	Body                 io.Reader
	ThumbnailContentType string
	ThumbnailBody        io.Reader
}

// ImageKeys are the stored original and optional thumbnail for one media item.
type ImageKeys struct {
	ObjectKey    string
	ThumbnailKey *string
	Type         string
}

func normalizeMediaType(t string) (string, error) {
	switch strings.TrimSpace(t) {
	case "", MediaTypeImage:
		return MediaTypeImage, nil
	case MediaTypeVideo:
		return MediaTypeVideo, nil
	default:
		return "", fmt.Errorf("%w: invalid media type", ErrInvalidInput)
	}
}

func normalizeMediaTypes(count int, types []string) ([]string, error) {
	if count < 1 || count > maxMedia {
		return nil, fmt.Errorf("%w: count must be 1–%d", ErrInvalidInput, maxMedia)
	}
	if len(types) > count {
		return nil, fmt.Errorf("%w: media types exceed count", ErrInvalidInput)
	}
	mediaTypes := make([]string, count)
	for i := range mediaTypes {
		t := MediaTypeImage
		if i < len(types) {
			var err error
			t, err = normalizeMediaType(types[i])
			if err != nil {
				return nil, err
			}
		}
		mediaTypes[i] = t
	}
	if err := validateMediaTypeCounts(mediaTypes); err != nil {
		return nil, err
	}
	return mediaTypes, nil
}

func validateMediaTypeCounts(types []string) error {
	var images, videos int
	for _, t := range types {
		switch t {
		case MediaTypeImage:
			images++
		case MediaTypeVideo:
			videos++
		default:
			return fmt.Errorf("%w: invalid media type", ErrInvalidInput)
		}
	}
	if images > maxImages {
		return fmt.Errorf("%w: tối đa %d ảnh/checkpoint", ErrInvalidInput, maxImages)
	}
	if videos > maxVideos {
		return fmt.Errorf("%w: tối đa %d video/checkpoint", ErrInvalidInput, maxVideos)
	}
	if images+videos == 0 {
		return fmt.Errorf("%w: ít nhất 1 ảnh hoặc video", ErrInvalidInput)
	}
	return nil
}

func validateMediaKeys(keys []ImageKeys) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: ít nhất 1 ảnh hoặc video", ErrInvalidInput)
	}
	if len(keys) > maxMedia {
		return fmt.Errorf("%w: tối đa %d media/checkpoint", ErrInvalidInput, maxMedia)
	}
	types := make([]string, len(keys))
	for i, item := range keys {
		t, err := normalizeMediaType(item.Type)
		if err != nil {
			return err
		}
		keys[i].Type = t
		types[i] = t
		if !strings.HasPrefix(item.ObjectKey, "checkpoints/") {
			return fmt.Errorf("%w: invalid media key", ErrInvalidInput)
		}
		lowerKey := strings.ToLower(item.ObjectKey)
		if t == MediaTypeImage && !strings.HasSuffix(lowerKey, ".jpg") && !strings.HasSuffix(lowerKey, ".jpeg") {
			return fmt.Errorf("%w: image key must be jpg", ErrInvalidInput)
		}
		if t == MediaTypeVideo && !strings.HasSuffix(lowerKey, ".mp4") {
			return fmt.Errorf("%w: video key must be mp4", ErrInvalidInput)
		}
		if item.ThumbnailKey != nil {
			thumb := strings.ToLower(*item.ThumbnailKey)
			if !strings.HasPrefix(*item.ThumbnailKey, "checkpoints/") {
				return fmt.Errorf("%w: invalid thumbnail key", ErrInvalidInput)
			}
			if !strings.HasSuffix(thumb, ".jpg") && !strings.HasSuffix(thumb, ".jpeg") {
				return fmt.Errorf("%w: thumbnail key must be jpg", ErrInvalidInput)
			}
		}
	}
	return validateMediaTypeCounts(types)
}

// AddImages uploads images to a checkpoint the caller owns and returns them.
func (s *Service) AddImages(ctx context.Context, checkpointID, userID string, uploads []ImageUpload) ([]Image, error) {
	if len(uploads) == 0 {
		return nil, fmt.Errorf("%w: no images", ErrInvalidInput)
	}
	cp, err := s.repo.GetByID(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if cp.UserID != userID {
		return nil, ErrNotFound // don't leak existence to non-owners
	}
	existing, err := s.repo.CountImages(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if existing+len(uploads) > maxImages {
		return nil, fmt.Errorf("%w: tối đa %d ảnh/checkpoint", ErrInvalidInput, maxImages)
	}

	// Validate all content types up front (fail fast, before any upload).
	exts := make([]string, len(uploads))
	for i, u := range uploads {
		ext := imageExt(u.ContentType)
		if ext == "" {
			return nil, ErrUnsupportedFile
		}
		exts[i] = ext
	}

	// Upload to R2 in parallel — independent objects, so 3 photos take ~1 round
	// trip instead of 3. Order is preserved via the indexed keys slice.
	keys := make([]ImageKeys, len(uploads))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxImages)
	for i := range uploads {
		g.Go(func() error {
			key := fmt.Sprintf("checkpoints/%s/%s%s", checkpointID, randHex(8), exts[i])
			storedKey, err := s.storage.Upload(gctx, key, uploads[i].ContentType, uploads[i].Body)
			if err != nil {
				return fmt.Errorf("upload image: %w", err)
			}
			keys[i].ObjectKey = storedKey
			keys[i].Type = MediaTypeImage
			if uploads[i].ThumbnailBody != nil {
				if thumbExt := imageExt(uploads[i].ThumbnailContentType); thumbExt != "" {
					thumbKey := fmt.Sprintf("checkpoints/%s/thumbnails/%s%s", checkpointID, randHex(8), thumbExt)
					storedThumb, thumbErr := s.storage.Upload(gctx, thumbKey, uploads[i].ThumbnailContentType, uploads[i].ThumbnailBody)
					if thumbErr != nil {
						log.Printf("upload checkpoint thumbnail: %v", thumbErr)
					} else {
						keys[i].ThumbnailKey = &storedThumb
					}
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return s.repo.AddImages(ctx, checkpointID, keys, existing)
}

// UploadSlot holds a presigned PUT URL and the key that will be stored once the
// browser finishes uploading.
type UploadSlot struct {
	UploadURL          string `json:"upload_url"`
	ObjectKey          string `json:"object_key"`
	ThumbnailUploadURL string `json:"thumbnail_upload_url"`
	ThumbnailKey       string `json:"thumbnail_key"`
}

const presignTTL = 300 // seconds

// PrepareSlots generates presigned R2 PUT URLs before the checkpoint exists.
// The client uploads files directly to R2, then includes the returned keys in
// the POST /checkpoints body — creating the checkpoint and linking images atomically.
func (s *Service) PrepareSlots(_ context.Context, _ string, count int, types []string) ([]UploadSlot, error) {
	mediaTypes, err := normalizeMediaTypes(count, types)
	if err != nil {
		return nil, err
	}
	// Use a random 16-byte prefix so keys are unguessable. The checkpoint ID
	// (assigned by the DB) will differ — the service validates "checkpoints/" prefix.
	prefix := randHex(16)
	slots := make([]UploadSlot, count)
	for i := range slots {
		ext := ".jpg"
		if mediaTypes[i] == MediaTypeVideo {
			ext = ".mp4"
		}
		objKey := fmt.Sprintf("checkpoints/%s/%s%s", prefix, randHex(8), ext)
		thumbKey := fmt.Sprintf("checkpoints/%s/thumbnails/%s.jpg", prefix, randHex(8))
		slots[i] = UploadSlot{
			UploadURL:          s.storage.PresignUpload(objKey, presignTTL),
			ObjectKey:          objKey,
			ThumbnailUploadURL: s.storage.PresignUpload(thumbKey, presignTTL),
			ThumbnailKey:       thumbKey,
		}
	}
	return slots, nil
}

// PrepareUploads generates presigned R2 PUT URLs so the client can upload
// images directly to storage without routing the file bytes through this server.
// Deprecated: use PrepareSlots + include images in POST /checkpoints body.
func (s *Service) PrepareUploads(ctx context.Context, checkpointID, userID string, count int) ([]UploadSlot, error) {
	if count < 1 {
		return nil, fmt.Errorf("%w: count must be ≥ 1", ErrInvalidInput)
	}
	cp, err := s.repo.GetByID(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if cp.UserID != userID {
		return nil, ErrNotFound
	}
	existing, err := s.repo.CountImages(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if existing+count > maxImages {
		return nil, fmt.Errorf("%w: tối đa %d ảnh/checkpoint", ErrInvalidInput, maxImages)
	}

	slots := make([]UploadSlot, count)
	for i := range slots {
		objKey := fmt.Sprintf("checkpoints/%s/%s.jpg", checkpointID, randHex(8))
		thumbKey := fmt.Sprintf("checkpoints/%s/thumbnails/%s.jpg", checkpointID, randHex(8))
		slots[i] = UploadSlot{
			UploadURL:          s.storage.PresignUpload(objKey, presignTTL),
			ObjectKey:          objKey,
			ThumbnailUploadURL: s.storage.PresignUpload(thumbKey, presignTTL),
			ThumbnailKey:       thumbKey,
		}
	}
	return slots, nil
}

// LinkImages records image keys that were uploaded directly to R2 by the client.
// Keys are validated to belong to the checkpoint's namespace.
func (s *Service) LinkImages(ctx context.Context, checkpointID, userID string, keys []ImageKeys) ([]Image, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no images", ErrInvalidInput)
	}
	cp, err := s.repo.GetByID(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if cp.UserID != userID {
		return nil, ErrNotFound
	}
	existing, err := s.repo.CountImages(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if existing+len(keys) > maxImages {
		return nil, fmt.Errorf("%w: tối đa %d ảnh/checkpoint", ErrInvalidInput, maxImages)
	}

	// Guard against linking arbitrary R2 objects from other checkpoints.
	prefix := fmt.Sprintf("checkpoints/%s/", checkpointID)
	for _, k := range keys {
		if !strings.HasPrefix(k.ObjectKey, prefix) {
			return nil, fmt.Errorf("%w: object key outside checkpoint namespace", ErrInvalidInput)
		}
		if k.ThumbnailKey != nil && !strings.HasPrefix(*k.ThumbnailKey, prefix) {
			return nil, fmt.Errorf("%w: thumbnail key outside checkpoint namespace", ErrInvalidInput)
		}
	}

	return s.repo.AddImages(ctx, checkpointID, keys, existing)
}

// ListByUser returns a user's checkpoints, newest first (limit clamped to
// [1, maxListLimit]). Checkpoints are public, so no ownership check.
func (s *Service) ListByUser(ctx context.Context, userID string, provinceCode string, limit int) ([]*Checkpoint, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return s.repo.ListByUser(ctx, userID, provinceCode, limit)
}

// ListMine returns the caller's checkpoints as a keyset-paginated page.
func (s *Service) ListMine(ctx context.Context, userID string, provinceCode string, limit int, cursor *CheckpointCursor) (*CheckpointPage, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	list, err := s.repo.ListByUserPage(ctx, userID, provinceCode, limit+1, cursor)
	if err != nil {
		return nil, err
	}
	return checkpointPage(list, limit), nil
}

// ListMineUnassigned returns caller-owned checkpoints that are not attached to a
// journey yet, newest first.
func (s *Service) ListMineUnassigned(ctx context.Context, userID string, limit int, cursor *CheckpointCursor) (*CheckpointPage, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	list, err := s.repo.ListUnassignedByUser(ctx, userID, limit+1, cursor)
	if err != nil {
		return nil, err
	}
	return checkpointPage(list, limit), nil
}

func checkpointPage(list []*Checkpoint, limit int) *CheckpointPage {
	page := &CheckpointPage{Checkpoints: list}
	if len(list) > limit {
		page.Checkpoints = list[:limit]
		last := page.Checkpoints[len(page.Checkpoints)-1]
		next := last.CreatedAt.Format(time.RFC3339Nano) + "|" + last.ID
		page.NextCursor = &next
	}
	return page
}

// Delete hard-deletes a checkpoint the caller owns (XP/stats reversed, orphaned
// place removed), then best-effort removes its images from storage — the DB is
// the source of truth and an orphaned file is harmless, so storage errors are
// only logged.
func (s *Service) Delete(ctx context.Context, id, userID string) error {
	keys, err := s.repo.Delete(ctx, id, userID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		// Legacy rows stored full URLs instead of keys — nothing to delete there.
		if key == "" || strings.HasPrefix(key, "http://") || strings.HasPrefix(key, "https://") {
			continue
		}
		if err := s.storage.Delete(ctx, key); err != nil {
			log.Printf("delete checkpoint image %q: %v", key, err)
		}
	}
	return nil
}
