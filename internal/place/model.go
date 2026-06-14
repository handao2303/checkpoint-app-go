package place

import "time"

// Place is a canonical location aggregated from community check-ins.
type Place struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	Address      *string   `json:"address,omitempty"`
	CheckinCount int       `json:"checkin_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ResolveParams carries the data used to match or create a place during a
// check-in (inherited from the checkpoint).
type ResolveParams struct {
	Name     string
	Category string
	Lat      float64
	Lng      float64
	Address  *string
}

// Point is an optional location bias for search.
type Point struct {
	Lat float64
	Lng float64
}

// Detail aggregates a place with community data for the place screen.
type Detail struct {
	Place     *Place        `json:"place"`
	AvgRating *float64      `json:"avg_rating,omitempty"`
	Images    []DetailImage `json:"images"`
	Reviews   []Review      `json:"reviews"`
}

// DetailImage is a recent photo across the place's check-ins.
type DetailImage struct {
	ID           string  `json:"id"`
	URL          string  `json:"url"`
	ThumbnailURL *string `json:"thumbnail_url,omitempty"`
}

// Review is a recent check-in note + its author.
type Review struct {
	Note      string    `json:"note"`
	Rating    *int16    `json:"rating,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Author    Author    `json:"author"`
}

// Author is the minimal public identity of a reviewer.
type Author struct {
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// FeedItem is one community check-in in the discover feed.
type FeedItem struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"` // author id, for the FE follow button
	Name         string    `json:"name"`
	Category     string    `json:"category"`
	Note         *string   `json:"note,omitempty"`
	Rating       *int16    `json:"rating,omitempty"`
	Lat          float64   `json:"lat"`
	Lng          float64   `json:"lng"`
	Address      *string   `json:"address,omitempty"`
	PlaceID      *string   `json:"place_id,omitempty"`
	XPAwarded    int       `json:"xp_awarded"`
	CreatedAt    time.Time `json:"created_at"`
	Author       Author    `json:"author"`
	ImageURL     *string   `json:"image_url,omitempty"`
	ThumbnailURL *string   `json:"thumbnail_url,omitempty"`
	DistanceM    float64   `json:"distance_m"`
}

// Feed is a page of feed items with an optional keyset cursor for the next page.
type Feed struct {
	Items      []FeedItem `json:"items"`
	NextCursor *string    `json:"next_cursor,omitempty"`
}
