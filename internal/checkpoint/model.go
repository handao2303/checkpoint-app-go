package checkpoint

import (
	"time"

	"github.com/quanluon/checkpoint-app/internal/gamification"
)

// Checkpoint is a check-in at a GPS location.
type Checkpoint struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	Note         *string   `json:"note,omitempty"`
	Rating       *int16    `json:"rating,omitempty"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	Address      *string   `json:"address,omitempty"`
	PlaceID      *string   `json:"place_id,omitempty"`
	JourneyID    *string   `json:"journey_id,omitempty"`
	ImageURL     *string   `json:"image_url,omitempty"` // first photo (populated in list)
	ThumbnailURL *string   `json:"thumbnail_url,omitempty"`
	XPAwarded    int       `json:"xp_awarded"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreateParams are the validated inputs for creating a checkpoint.
type CreateParams struct {
	UserID    string
	Name      string
	Category  string
	Note      *string
	Rating    *int16
	Lat       float64
	Lng       float64
	Address   *string
	Province  *string // resolved province for the passport (best-effort)
	PlaceID   *string // optional: check-in to a specific place (else resolved)
	JourneyID *string // optional: attach to a journey (else active journey)
	XPReward  int
}

// CreateResult is the outcome of a check-in: the new checkpoint plus the user's
// updated XP/level (computed atomically with the insert).
type CreateResult struct {
	Checkpoint *Checkpoint          `json:"checkpoint"`
	XPAwarded  int                  `json:"xp_awarded"`
	TotalXP    int                  `json:"total_xp"`
	Level      int                  `json:"level"`
	NewBadges  []gamification.Badge `json:"new_badges,omitempty"` // badges unlocked by this check-in
}

// Image is a photo attached to a checkpoint.
type Image struct {
	ID           string  `json:"id"`
	URL          string  `json:"url"`
	ThumbnailURL *string `json:"thumbnail_url,omitempty"`
	SortOrder    int     `json:"sort_order"`
}

// Author is the minimal public identity of a checkpoint's owner.
type Author struct {
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// Detail is a checkpoint with its images and author, for the detail screen.
type Detail struct {
	Checkpoint *Checkpoint `json:"checkpoint"`
	Images     []Image     `json:"images"`
	Author     *Author     `json:"author"`
}
