package checkpoint

import (
	"time"

	"github.com/quanluon/checkpoint-app/internal/gamification"
)

// Checkpoint is a check-in at a GPS location.
type Checkpoint struct {
	ID           string     `json:"id"`
	UserID       string     `json:"user_id"`
	Name         string     `json:"name"`
	Category     string     `json:"category"`
	Note         *string    `json:"note,omitempty"`
	Rating       *int16     `json:"rating,omitempty"`
	Lat          float64    `json:"lat"`
	Lng          float64    `json:"lng"`
	Address      *string    `json:"address,omitempty"`
	PlaceID      *string    `json:"place_id,omitempty"`
	JourneyID    *string    `json:"journey_id,omitempty"`
	ImageURL     *string    `json:"image_url,omitempty"` // first photo (populated in list)
	ThumbnailURL *string    `json:"thumbnail_url,omitempty"`
	MediaType    *string    `json:"media_type,omitempty"` // type of the cover media in list responses
	XPAwarded    int        `json:"xp_awarded"`
	ViewCount    int        `json:"view_count"`
	TakenAt      *time.Time `json:"taken_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// CreateParams are the validated inputs for creating a checkpoint.
type CreateParams struct {
	UserID       string
	Name         string
	Category     string
	Note         *string
	Rating       *int16
	Lat          float64
	Lng          float64
	Address      *string
	Province     *string    // resolved province name for the passport (best-effort)
	ProvinceCode *string    // ISO code (e.g. "VN-SG") for optimized grouping
	PlaceID      *string    // optional: check-in to a specific place (else resolved)
	JourneyID    *string    // optional: attach to a journey (else active journey)
	TakenAt      *time.Time // optional source-photo capture timestamp
	XPReward     int
	RequestHash  string
	Images       []ImageKeys // pre-uploaded media to link in the same tx (required ≥1)
}

// CreateResult is the outcome of a check-in: the new checkpoint plus the user's
// updated XP/level (computed atomically with the insert).
type CreateResult struct {
	Checkpoint *Checkpoint          `json:"checkpoint"`
	XPAwarded  int                  `json:"xp_awarded"`
	TotalXP    int                  `json:"total_xp"`
	Level      int                  `json:"level"`
	Duplicate  bool                 `json:"duplicate,omitempty"`
	NewBadges  []gamification.Badge `json:"new_badges,omitempty"` // badges unlocked by this check-in
	Images     []Image              `json:"images,omitempty"`     // all images linked at creation time
}

// UpdateParams are the owner-scoped editable checkpoint fields.
type UpdateParams struct {
	Name         string
	Note         *string
	Rating       *int16
	ReplaceMedia bool
	Media        []UpdateMediaItem
}

// UpdateMediaItem is one entry in the final media order for a checkpoint edit.
// Existing media is identified by ID; new media is identified by pre-uploaded
// object keys.
type UpdateMediaItem struct {
	ID           string
	ObjectKey    string
	ThumbnailKey *string
	Type         string
}

// UpdateResult is the committed outcome of an owner-scoped checkpoint edit.
type UpdateResult struct {
	Checkpoint      *Checkpoint
	HasPendingMedia bool
}

// Image is a media item attached to a checkpoint. Type is "image" or "video".
type Image struct {
	ID           string  `json:"id"`
	URL          string  `json:"url"`
	ThumbnailURL *string `json:"thumbnail_url,omitempty"`
	Type         string  `json:"type"`
	SortOrder    int     `json:"sort_order"`
	// ThumbWidth/ThumbHeight are the thumbnail's pixel dimensions, when measured.
	// Share pages emit them as og:image:width/height so Messenger and a
	// first-time Facebook scrape render the preview without fetching the image.
	ThumbWidth  *int `json:"thumb_width,omitempty"`
	ThumbHeight *int `json:"thumb_height,omitempty"`
	// Pending is true while the media is still being processed by the worker
	// (no thumbnail yet). Clients show a "đang xử lý" placeholder.
	Pending bool `json:"pending,omitempty"`
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
