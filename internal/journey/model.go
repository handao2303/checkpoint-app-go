package journey

import "time"

// Visibility controls who can see a journey in a user's profile list. Note that
// the journey detail endpoint stays link-shareable regardless of this value.
const (
	VisibilityOnlyMe    = "only_me"
	VisibilityFollowers = "followers"
	VisibilityPublic    = "public"
)

// validVisibility reports whether v is one of the allowed visibility values.
func validVisibility(v string) bool {
	switch v {
	case VisibilityOnlyMe, VisibilityFollowers, VisibilityPublic:
		return true
	default:
		return false
	}
}

// Journey is a user's trip — a named collection of check-ins.
type Journey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	IsActive    bool       `json:"is_active"`
	Visibility  string     `json:"visibility"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// CreateParams are the validated inputs for creating a journey.
type CreateParams struct {
	UserID      string
	Title       string
	Description *string
	Visibility  string
}

// UpdateInput is a partial update (PATCH). Nil fields are left unchanged.
type UpdateInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	IsActive    *bool   `json:"is_active"`
	Visibility  *string `json:"visibility"`
}

// DeleteMode controls what happens to checkpoints attached to a deleted journey.
type DeleteMode string

const (
	DeleteDetachCheckpoints DeleteMode = "detach"
	DeleteHardCheckpoints   DeleteMode = "delete"
)

// DeleteResult captures side effects that should happen after the DB
// transaction commits.
type DeleteResult struct {
	MediaKeys []string
}

// Stop is a checkpoint that belongs to a journey (timeline + map point).
type Stop struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	Note         *string   `json:"note,omitempty"`
	Address      *string   `json:"address,omitempty"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	PlaceID      *string   `json:"place_id,omitempty"`
	ImageURL     *string   `json:"image_url,omitempty"`
	ThumbnailURL *string   `json:"thumbnail_url,omitempty"`
	MediaType    *string   `json:"media_type,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Stats are aggregated figures for a journey (J2).
type Stats struct {
	CheckpointCount int     `json:"checkpoint_count"`
	TotalXP         int     `json:"total_xp"`
	DistanceKm      float64 `json:"distance_km"`
}

// Detail is a journey with its stats and ordered stops (each stop carries its
// own photos). J2.
type Detail struct {
	Journey     *Journey `json:"journey"`
	Stats       Stats    `json:"stats"`
	Checkpoints []Stop   `json:"checkpoints"`
}
