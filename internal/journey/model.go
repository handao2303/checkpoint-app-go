package journey

import "time"

// Journey is a user's trip — a named collection of check-ins.
type Journey struct {
	ID          string     `json:"id"`
	UserID      string     `json:"user_id"`
	Title       string     `json:"title"`
	Description *string    `json:"description,omitempty"`
	IsActive    bool       `json:"is_active"`
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
}

// UpdateInput is a partial update (PATCH). Nil fields are left unchanged.
type UpdateInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
	IsActive    *bool   `json:"is_active"`
}

// Stop is a checkpoint that belongs to a journey (timeline + map point), with
// its own photos shown on the map marker. Phase 3 J2.
type Stop struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Category        string    `json:"category"`
	Note            *string   `json:"note,omitempty"`
	Address         *string   `json:"address,omitempty"`
	Lat             float64   `json:"lat"`
	Lng             float64   `json:"lng"`
	PlaceID         *string   `json:"place_id,omitempty"`
	Images          []string  `json:"images"`
	ThumbnailImages []string  `json:"thumbnail_images"`
	CreatedAt       time.Time `json:"created_at"`
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
