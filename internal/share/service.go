package share

import (
	"context"
	"fmt"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/internal/identity"
	"github.com/quanluon/checkpoint-app/internal/journey"
	"github.com/quanluon/checkpoint-app/internal/place"
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

// Service provides share-specific data aggregation logic.
type Service struct {
	checkpointSvc checkpointService
	journeySvc    journeyService
	identitySvc   identityService
	placeSvc      placeService
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
	URL         string
	RedirectURL string
}

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

	image := frontendURL + "/icon-512.png"
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

	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		Type:        "place",
		URL:         fmt.Sprintf("%s/c/%s", frontendURL, id),
		RedirectURL: fmt.Sprintf("%s/c/%s", frontendURL, id),
	}, nil
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

	image := frontendURL + "/icon-512.png"
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

	image := frontendURL + "/icon-512.png"
	if u.AvatarURL != nil && *u.AvatarURL != "" {
		image = *u.AvatarURL
	}

	return &MetaData{
		Title:       title,
		Description: description,
		Image:       image,
		Type:        "profile",
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

	image := frontendURL + "/icon-512.png"
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
		URL:         fmt.Sprintf("%s/p/%s", frontendURL, id),
		RedirectURL: fmt.Sprintf("%s/p/%s", frontendURL, id),
	}, nil
}
