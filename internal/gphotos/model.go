package gphotos

import "time"

// Tokens holds a user's Google Photos OAuth tokens.
type Tokens struct {
	UserID       string
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time
	AlbumID      *string // cached "Checkpoint App" album ID
	ConnectedAt  time.Time
}

// CheckpointMeta is the metadata synced alongside each image.
type CheckpointMeta struct {
	ID        string
	Name      string
	Lat, Lng  float64
	Address   *string
	Rating    *int16
	Note      *string
	Category  string
	CreatedAt time.Time
	ImageURLs []string
}
