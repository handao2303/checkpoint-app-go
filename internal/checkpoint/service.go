package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
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
	maxImages        = 10
)

// repository is the data layer the service depends on (consumer-side interface
// for easy mocking).
type repository interface {
	Create(ctx context.Context, p CreateParams) (*CreateResult, error)
	GetByID(ctx context.Context, id string) (*Checkpoint, error)
	GetDetail(ctx context.Context, id string) (*Detail, error)
	ListByUser(ctx context.Context, userID string, limit int) ([]*Checkpoint, error)
	ListInBounds(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit int) ([]*Checkpoint, error)
	ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Checkpoint, error)
	AddImages(ctx context.Context, checkpointID string, urls []string, startOrder int) ([]Image, error)
	CountImages(ctx context.Context, checkpointID string) (int, error)
	Delete(ctx context.Context, id, userID string) error
}

// fileStorage uploads an object and returns its public URL.
type fileStorage interface {
	Upload(ctx context.Context, key, contentType string, body io.Reader) (string, error)
}

type reverseGeocoder interface {
	Reverse(ctx context.Context, lat, lng float64) (string, error)
}

// Service holds the checkpoint business logic.
type Service struct {
	repo     repository
	storage  fileStorage
	geocoder reverseGeocoder
}

// NewService builds the checkpoint service.
func NewService(repo repository, storage fileStorage, geocoder reverseGeocoder) *Service {
	return &Service{repo: repo, storage: storage, geocoder: geocoder}
}

// Input is the user-supplied data for a check-in (before validation).
type Input struct {
	Name     string
	Category string
	Note     *string
	Rating   *int16
	Lat      float64
	Lng      float64
	Address  *string
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
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = "other"
	}
	address := in.Address
	if address != nil {
		trimmed := strings.TrimSpace(*address)
		if trimmed == "" {
			address = nil
		} else {
			address = &trimmed
		}
	}
	if address == nil && s.geocoder != nil {
		if resolved, err := s.geocoder.Reverse(ctx, in.Lat, in.Lng); err == nil {
			if resolved = strings.TrimSpace(resolved); resolved != "" {
				address = &resolved
			}
		}
	}

	return s.repo.Create(ctx, CreateParams{
		UserID:   userID,
		Name:     name,
		Category: category,
		Note:     in.Note,
		Rating:   in.Rating,
		Lat:      in.Lat,
		Lng:      in.Lng,
		Address:  address,
		XPReward: checkinXP,
	})
}

// GetDetail returns a checkpoint with images and author.
func (s *Service) GetDetail(ctx context.Context, id string) (*Detail, error) {
	return s.repo.GetDetail(ctx, id)
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
	ContentType string
	Body        io.Reader
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

	keys := make([]string, 0, len(uploads))
	for _, u := range uploads {
		ext := imageExt(u.ContentType)
		if ext == "" {
			return nil, ErrUnsupportedFile
		}
		key := fmt.Sprintf("checkpoints/%s/%s%s", checkpointID, randHex(8), ext)
		storedKey, err := s.storage.Upload(ctx, key, u.ContentType, u.Body)
		if err != nil {
			return nil, fmt.Errorf("upload image: %w", err)
		}
		keys = append(keys, storedKey)
	}
	return s.repo.AddImages(ctx, checkpointID, keys, existing)
}

// ListMine returns the caller's checkpoints (limit clamped to [1, maxListLimit]).
func (s *Service) ListMine(ctx context.Context, userID string, limit int) ([]*Checkpoint, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	return s.repo.ListByUser(ctx, userID, limit)
}

// Delete soft-deletes a checkpoint the caller owns.
func (s *Service) Delete(ctx context.Context, id, userID string) error {
	return s.repo.Delete(ctx, id, userID)
}
