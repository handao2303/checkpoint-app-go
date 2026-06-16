package gdrive

import "time"

// Tokens holds a user's Google Drive OAuth2 tokens.
type Tokens struct {
	UserID       string
	Email        string // Google account email (for display)
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time
	FolderID     *string // cached "Checkpoint App" Drive folder ID
	ConnectedAt  time.Time
}

// CheckpointMeta carries the data needed to name and annotate a Drive file.
type CheckpointMeta struct {
	ID        string
	Name      string
	Lat       float64
	Lng       float64
	Address   *string
	Rating    *int16
	Note      *string
	Category  string
	CreatedAt time.Time
	Media     []Media // full public R2 URLs of the media files to upload
}

// Media is one original checkpoint media file.
type Media struct {
	URL  string
	Type string
}
