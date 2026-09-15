package share

import (
	"context"
	"fmt"
	"log"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/internal/identity"
	"github.com/quanluon/checkpoint-app/internal/journey"
	"github.com/quanluon/checkpoint-app/internal/place"
	"github.com/quanluon/checkpoint-app/pkg/storage"
)

type checkpointService interface {
	GetDetail(ctx context.Context, id string) (*checkpoint.Detail, error)
}

type journeyService interface {
	GetDetail(ctx context.Context, id string) (*journey.Detail, error)
}

type identityService interface {
	GetByUsername(ctx context.Context, username string) (*identity.User, error)
	GetByID(ctx context.Context, id string) (*identity.User, error)
	GetByIDOrUsername(ctx context.Context, ref string) (*identity.User, error)
}

type placeService interface {
	GetDetail(ctx context.Context, id string, limit int, offset int, viewerID *string) (*place.Detail, error)
}

// thumbMeasurer measures a checkpoint's thumbnails on demand, for media that
// predates the thumb_width/thumb_height columns or that the backfill has not
// reached yet. Optional — nil simply means the card omits the dimensions.
type thumbMeasurer interface {
	MeasureCheckpoint(ctx context.Context, checkpointID string) (map[string]checkpoint.ThumbSize, error)
}

// cardBuilder renders the 1200x630 social card for a checkpoint on demand, for
// media the card backfill has not reached. Optional — nil means share cards use
// the thumbnail, which on a portrait photo is too narrow for a large preview.
type cardBuilder interface {
	EnsureForCheckpoint(ctx context.Context, checkpointID string) (map[string]checkpoint.OGCard, error)
}

// Service provides share-specific data aggregation logic.
type Service struct {
	checkpointSvc checkpointService
	journeySvc    journeyService
	identitySvc   identityService
	placeSvc      placeService
	measurer      thumbMeasurer
	cards         cardBuilder
	mediaBase     string
}

// WithThumbMeasurer attaches the on-demand thumbnail measurer. Without it,
// share cards fall back to omitting og:image:width/height for media whose size
// was never recorded.
func (s *Service) WithThumbMeasurer(m thumbMeasurer) *Service {
	s.measurer = m
	return s
}

// WithCardBuilder attaches the on-demand social-card renderer. mediaBase is the
// public base URL cards are served from, so a freshly rendered card's key can be
// turned into the URL the meta tag advertises.
func (s *Service) WithCardBuilder(b cardBuilder, mediaBase string) *Service {
	s.cards = b
	s.mediaBase = mediaBase
	return s
}

// NewService constructs a new Share Service.
func NewService(cp checkpointService, j journeyService, id identityService, p placeService) *Service {
	return &Service{
		checkpointSvc: cp,
		journeySvc:    j,
		identitySvc:   id,
		placeSvc:      p,
	}
}

// MetaData holds the open graph data to be rendered.
type MetaData struct {
	Title       string
	Description string
	Image       string
	Type        string
	// TwitterCard is "summary_large_image" for wide photos and "summary" for
	// square art such as avatars, which crops badly in the large card.
	TwitterCard string
	// ImageWidth/ImageHeight are Image's pixel dimensions, 0 when unknown.
	// Messenger, and Facebook on a first uncached scrape, frequently render no
	// preview image without them, because neither blocks on fetching the image.
	ImageWidth  int
	ImageHeight int
	URL         string
	RedirectURL string
}

const (
	cardLarge = "summary_large_image"
	cardSmall = "summary"

	// fallbackWide is the 1200x630 site card, sized for wide previews;
	// icon-512.png is square and only suits the compact profile card.
	fallbackWide   = "/og-image.jpg"
	fallbackSquare = "/icon-512.png"
)

// GetCheckpointMeta aggregates metadata for a checkpoint share.
func (s *Service) GetCheckpointMeta(ctx context.Context, id, frontendURL string) (*MetaData, error) {
	detail, err := s.checkpointSvc.GetDetail(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get checkpoint detail: %w", err)
	}

	title := detail.Checkpoint.Name
	if detail.Author != nil && detail.Author.DisplayName != nil {
		title = fmt.Sprintf("Checkpoint tại %s bởi %s", detail.Checkpoint.Name, *detail.Author.DisplayName)
	} else if detail.Author != nil {
		title = fmt.Sprintf("Checkpoint tại %s bởi @%s", detail.Checkpoint.Name, detail.Author.Username)
	}

	description := "Khám phá địa điểm du lịch cùng Checkpoint App."
	if detail.Checkpoint.Note != nil && *detail.Checkpoint.Note != "" {
		description = *detail.Checkpoint.Note
	}

	// A purpose-built 1200x630 card beats the feed thumbnail whenever one
	// exists: thumbnails are capped at a 640px longest edge, so portrait photos
	// end up too narrow for Messenger to render a large preview.
	if card, w, h, ok := s.socialCard(ctx, detail); ok {
		return s.checkpointMeta(detail, id, frontendURL, title, description, card, w, h), nil
	}

	image := frontendURL + fallbackWide
	if detail.Checkpoint.ThumbnailURL != nil && *detail.Checkpoint.ThumbnailURL != "" {
		image = *detail.Checkpoint.ThumbnailURL
	} else if detail.Checkpoint.ImageURL != nil && *detail.Checkpoint.ImageURL != "" {
		image = *detail.Checkpoint.ImageURL
	} else if len(detail.Images) > 0 {
		if detail.Images[0].ThumbnailURL != nil && *detail.Images[0].ThumbnailURL != "" {
			image = *detail.Images[0].ThumbnailURL
		} else {
			image = detail.Images[0].URL
		}
	}

	width, height := s.thumbSize(ctx, detail, image)

	return s.checkpointMeta(detail, id, frontendURL, title, description, image, width, height), nil
}

// checkpointMeta assembles the card once the image and its size are settled.
func (s *Service) checkpointMeta(_ *checkpoint.Detail, id, frontendURL, title, description, image string, width, height int) *MetaData {
	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		ImageWidth:  width,
		ImageHeight: height,
		Type:        "article",
		TwitterCard: cardLarge,
		URL:         fmt.Sprintf("%s/c/%s", frontendURL, id),
		RedirectURL: fmt.Sprintf("%s/c/%s", frontendURL, id),
	}
}

// GetJourneyMeta aggregates metadata for a journey share.
func (s *Service) GetJourneyMeta(ctx context.Context, id, frontendURL string) (*MetaData, error) {
	detail, err := s.journeySvc.GetDetail(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get journey detail: %w", err)
	}

	authorName := "Người dùng"
	u, err := s.identitySvc.GetByID(ctx, detail.Journey.UserID)
	if err == nil && u != nil {
		if u.DisplayName != nil && *u.DisplayName != "" {
			authorName = *u.DisplayName
		} else {
			authorName = "@" + u.Username
		}
	}

	title := fmt.Sprintf("%s - Hành trình của %s", detail.Journey.Title, authorName)

	description := fmt.Sprintf("Khám phá hành trình gồm %d địa điểm, tích lũy %d XP, tổng quãng đường di chuyển %.1f km trên Checkpoint.",
		detail.Stats.CheckpointCount, detail.Stats.TotalXP, detail.Stats.DistanceKm)
	if detail.Journey.Description != nil && *detail.Journey.Description != "" {
		description = *detail.Journey.Description
	}

	image := frontendURL + fallbackWide
	for _, cp := range detail.Checkpoints {
		if cp.ThumbnailURL != nil && *cp.ThumbnailURL != "" {
			image = *cp.ThumbnailURL
			break
		} else if cp.ImageURL != nil && *cp.ImageURL != "" {
			image = *cp.ImageURL
			break
		}
	}

	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		Type:        "website",
		TwitterCard: cardLarge,
		URL:         fmt.Sprintf("%s/j/%s", frontendURL, id),
		RedirectURL: fmt.Sprintf("%s/j/%s", frontendURL, id),
	}, nil
}

// GetUserMeta aggregates metadata for a user profile share. ref is a user id
// (preferred, stable) or a username (legacy links).
func (s *Service) GetUserMeta(ctx context.Context, ref, frontendURL string) (*MetaData, error) {
	u, err := s.identitySvc.GetByIDOrUsername(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("get user profile: %w", err)
	}

	title := fmt.Sprintf("@%s trên Checkpoint", u.Username)
	if u.DisplayName != nil && *u.DisplayName != "" {
		title = fmt.Sprintf("%s (@%s) trên Checkpoint", *u.DisplayName, u.Username)
	}

	// Headline the traveller's stats (flags planted, journeys) so the share card
	// carries social proof, falling back to the bio as the lead-in when present.
	stats := fmt.Sprintf("%d cờ · %d hành trình · %d XP", u.CheckpointCount, u.JourneyCount, u.TotalXP)
	description := fmt.Sprintf("Xem hộ chiếu du lịch số của @%s trên Checkpoint — %s.", u.Username, stats)
	if u.Bio != nil && *u.Bio != "" {
		description = fmt.Sprintf("%s · %s", *u.Bio, stats)
	}

	// Avatars (and the icon fallback) are square, so the wide card would crop
	// the face off; use the compact card for profiles.
	image := frontendURL + fallbackSquare
	card := cardSmall
	if u.AvatarURL != nil && *u.AvatarURL != "" {
		image = *u.AvatarURL
	}

	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		Type:        "profile",
		TwitterCard: card,
		URL:         fmt.Sprintf("%s/u/%s", frontendURL, u.ID),
		RedirectURL: fmt.Sprintf("%s/u/%s", frontendURL, u.ID),
	}, nil
}

// GetPlaceMeta aggregates metadata for a place share.
func (s *Service) GetPlaceMeta(ctx context.Context, id, frontendURL string) (*MetaData, error) {
	detail, err := s.placeSvc.GetDetail(ctx, id, 1, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("get place detail: %w", err)
	}

	title := detail.Place.Name
	description := fmt.Sprintf("Khám phá địa điểm du lịch %s cùng Checkpoint App. Đã có %d lượt cắm cờ check-in tại đây.", detail.Place.Name, detail.Place.CheckinCount)
	if detail.Place.Address != nil && *detail.Place.Address != "" {
		description = fmt.Sprintf("Địa chỉ: %s. %s", *detail.Place.Address, description)
	}

	image := frontendURL + fallbackWide
	if detail.Place.CoverImageURL != nil && *detail.Place.CoverImageURL != "" {
		image = *detail.Place.CoverImageURL
	} else if len(detail.Images) > 0 {
		image = detail.Images[0].URL
	}

	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		Type:        "website",
		TwitterCard: cardLarge,
		URL:         fmt.Sprintf("%s/p/%s", frontendURL, id),
		RedirectURL: fmt.Sprintf("%s/p/%s", frontendURL, id),
	}, nil
}

// thumbSize resolves the pixel dimensions of the image the card will advertise.
//
// It matches the chosen URL back to the media row it came from, since the
// checkpoint's denormalized cover is a copy of one of its images' thumbnails.
// When that row has no recorded size — media older than the columns, or an
// image thumbnail, which clients upload directly so the server never sees the
// bytes — it measures once and persists, so only the first share of a given
// checkpoint pays for it.
//
// Returns 0, 0 when the size cannot be determined; callers omit the tags rather
// than guessing, since a wrong size is worse than none.
func (s *Service) thumbSize(ctx context.Context, detail *checkpoint.Detail, image string) (width, height int) {
	img := imageForURL(detail, image)
	if img == nil {
		return 0, 0 // the static fallback card, or the full-size original
	}
	if img.ThumbWidth != nil && img.ThumbHeight != nil {
		return *img.ThumbWidth, *img.ThumbHeight
	}
	if s.measurer == nil {
		return 0, 0
	}

	sizes, err := s.measurer.MeasureCheckpoint(ctx, detail.Checkpoint.ID)
	if err != nil {
		log.Printf("share: measure thumbnails for %s: %v", detail.Checkpoint.ID, err)
		return 0, 0
	}
	if size, ok := sizes[img.ID]; ok {
		return size.Width, size.Height
	}
	return 0, 0
}

// imageForURL finds the media row whose thumbnail is the given URL.
func imageForURL(detail *checkpoint.Detail, url string) *checkpoint.Image {
	for i := range detail.Images {
		img := &detail.Images[i]
		if img.ThumbnailURL != nil && *img.ThumbnailURL == url {
			return img
		}
	}
	return nil
}

// socialCard resolves the checkpoint's 1200x630 preview card, rendering one on
// demand if the backfill has not reached this media yet.
//
// Returns ok=false when there is no card and none can be made — an original too
// small to crop from, a render failure, or no builder configured — in which case
// the caller falls back to the thumbnail.
func (s *Service) socialCard(ctx context.Context, detail *checkpoint.Detail) (url string, width, height int, ok bool) {
	cover := coverImage(detail)
	if cover == nil {
		return "", 0, 0, false
	}

	if cover.OGImageURL != nil && *cover.OGImageURL != "" {
		return *cover.OGImageURL, intOrZero(cover.OGWidth), intOrZero(cover.OGHeight), true
	}
	if s.cards == nil {
		return "", 0, 0, false
	}

	built, err := s.cards.EnsureForCheckpoint(ctx, detail.Checkpoint.ID)
	if err != nil {
		log.Printf("share: build social card for %s: %v", detail.Checkpoint.ID, err)
		return "", 0, 0, false
	}
	card, made := built[cover.ID]
	if !made {
		return "", 0, 0, false
	}
	return storage.PublicURL(s.mediaBase, card.Key), card.Width, card.Height, true
}

// coverImage is the media row the card should come from: the one the
// checkpoint's denormalized cover points at, else the first image.
func coverImage(detail *checkpoint.Detail) *checkpoint.Image {
	if len(detail.Images) == 0 {
		return nil
	}
	if detail.Checkpoint.ThumbnailURL != nil {
		if img := imageForURL(detail, *detail.Checkpoint.ThumbnailURL); img != nil {
			return img
		}
	}
	return &detail.Images[0]
}

func intOrZero(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
