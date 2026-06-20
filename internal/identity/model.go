package identity

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// User is a registered account. Stats fields are denormalized so profile and
// leaderboard reads stay a single row lookup.
type User struct {
	ID              string    `json:"id"`
	SupabaseID      string    `json:"-"`
	Email           *string   `json:"email,omitempty"` // nil for anonymous users
	Username        string    `json:"username"`
	DisplayName     *string   `json:"display_name,omitempty"`
	AvatarURL       *string   `json:"avatar_url,omitempty"`
	Bio             *string   `json:"bio,omitempty"`
	Role            string    `json:"role"`
	IsAnonymous     bool      `json:"is_anonymous"`
	TotalXP         int       `json:"total_xp"`
	Level           int       `json:"level"`
	CheckpointCount int       `json:"checkpoint_count"`
	JourneyCount    int       `json:"journey_count"`
	CheckinStreak   int       `json:"checkin_streak"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`

	// Derived (not stored): progress toward the next level, computed on read.
	XPToNext      int     `json:"xp_to_next"`
	LevelProgress float64 `json:"level_progress"`
}

// PublicUserSummary is the compact user card used by social profile lists.
type PublicUserSummary struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
}

// Claims is the payload of our own (backend-issued) access token.
type Claims struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// TokenPair is returned by /auth/exchange and /auth/refresh.
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// RefreshToken is a stored, revocable refresh-token record. The raw token is
// never persisted — only its SHA-256 hash.
type RefreshToken struct {
	UserID    string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// CreateUserParams carries the fields needed to provision a new user.
type CreateUserParams struct {
	SupabaseID  string
	Email       *string // nil for anonymous users
	Username    string
	Role        string
	IsAnonymous bool
}
