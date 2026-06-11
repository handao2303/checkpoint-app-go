package gamification

import "time"

// XP event types (the `type` column on xp_events).
const (
	EventCheckin        = "checkin"
	EventCheckinDeleted = "checkin_deleted" // negative-XP reversal on hard delete
)

// XPEvent is an append-only record of an XP award (audit + future analytics).
type XPEvent struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Type      string    `json:"type"`
	XP        int       `json:"xp"`
	RefID     *string   `json:"ref_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// PassportStamp is one province the user has visited, for the digital passport.
type PassportStamp struct {
	Province     string    `json:"province"`
	CheckinCount int       `json:"checkin_count"`
	FirstAt      time.Time `json:"first_at"`
	LastAt       time.Time `json:"last_at"`
	CoverURL     *string   `json:"cover_url,omitempty"`
}

// Passport is the user's collection of province stamps.
type Passport struct {
	ProvinceCount int             `json:"province_count"`
	Stamps        []PassportStamp `json:"stamps"`
}

// LeaderboardEntry is one ranked user.
type LeaderboardEntry struct {
	Rank        int     `json:"rank"`
	UserID      string  `json:"user_id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"display_name,omitempty"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	TotalXP     int     `json:"total_xp"`
	Level       int     `json:"level"`
}

// Leaderboard is the top ranked users plus the caller's own rank (which may sit
// outside the returned top slice).
type Leaderboard struct {
	Entries []LeaderboardEntry `json:"entries"`
	Me      *LeaderboardEntry  `json:"me,omitempty"`
}

// Badge is an achievement definition (catalog entry).
type Badge struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

// UserBadge is a catalog badge annotated with whether the user has earned it.
type UserBadge struct {
	Badge
	Awarded   bool       `json:"awarded"`
	AwardedAt *time.Time `json:"awarded_at,omitempty"`
}
