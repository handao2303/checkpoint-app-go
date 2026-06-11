package gamification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// Repository is the Postgres data layer for gamification. It serves read models
// (passport, leaderboard, badges) and applies XP awards inside a caller's tx.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read
}

// NewRepository builds a gamification repository over a pgx pool. mediaBase is
// the public base URL used to turn stored image keys into full URLs.
func NewRepository(db *pgxpool.Pool, mediaBase string) *Repository {
	return &Repository{db: db, mediaBase: mediaBase}
}

// AwardXPTx adds XP to a user within an existing transaction, recomputes their
// level, and appends an xp_event — all under a SELECT ... FOR UPDATE row lock so
// concurrent awards can't lose an increment. Returns the new total and level.
//
// It deliberately does NOT touch action-specific counters (e.g.
// checkpoint_count); the caller owns those.
func (r *Repository) AwardXPTx(
	ctx context.Context, tx pgx.Tx, userID string, amount int, eventType, refID string,
) (totalXP, level int, err error) {
	if err = tx.QueryRow(ctx,
		`SELECT total_xp FROM users WHERE id = $1 FOR UPDATE`, userID).Scan(&totalXP); err != nil {
		return 0, 0, fmt.Errorf("lock user xp: %w", err)
	}
	totalXP += amount
	level = LevelForXP(totalXP)

	if _, err = tx.Exec(ctx,
		`UPDATE users SET total_xp = $2, level = $3, updated_at = NOW() WHERE id = $1`,
		userID, totalXP, level); err != nil {
		return 0, 0, fmt.Errorf("update xp: %w", err)
	}

	var ref *string
	if refID != "" {
		ref = &refID
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO xp_events (user_id, type, xp, ref_id) VALUES ($1, $2, $3, $4)`,
		userID, eventType, amount, ref); err != nil {
		return 0, 0, fmt.Errorf("insert xp_event: %w", err)
	}
	return totalXP, level, nil
}

// Passport returns the user's province "stamps" — one per province they've
// checked in to — newest visit first, each with a representative cover photo.
func (r *Repository) Passport(ctx context.Context, userID string) (*Passport, error) {
	rows, err := r.db.Query(ctx,
		`SELECT g.province, g.checkin_count, g.first_at, g.last_at,
		        (SELECT ci.object_key
		           FROM checkpoints c2
		           JOIN checkpoint_images ci ON ci.checkpoint_id = c2.id
		          WHERE c2.user_id = $1 AND c2.deleted_at IS NULL AND c2.province = g.province
		          ORDER BY c2.created_at DESC, ci.sort_order LIMIT 1) AS cover_key
		   FROM (
		     SELECT province, COUNT(*) AS checkin_count,
		            MIN(created_at) AS first_at, MAX(created_at) AS last_at
		       FROM checkpoints
		      WHERE user_id = $1 AND deleted_at IS NULL AND province IS NOT NULL
		      GROUP BY province
		   ) g
		  ORDER BY g.last_at DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("query passport: %w", err)
	}
	defer rows.Close()

	passport := &Passport{Stamps: []PassportStamp{}}
	for rows.Next() {
		var s PassportStamp
		var coverKey *string
		if err := rows.Scan(&s.Province, &s.CheckinCount, &s.FirstAt, &s.LastAt, &coverKey); err != nil {
			return nil, fmt.Errorf("scan passport stamp: %w", err)
		}
		if coverKey != nil {
			url := storage.PublicURL(r.mediaBase, *coverKey)
			s.CoverURL = &url
		}
		passport.Stamps = append(passport.Stamps, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate passport: %w", err)
	}
	passport.ProvinceCount = len(passport.Stamps)
	return passport, nil
}

const leaderboardCols = `id, username, display_name, avatar_key, total_xp, level`

func (r *Repository) scanEntry(row pgx.Row) (LeaderboardEntry, error) {
	var e LeaderboardEntry
	var avatarKey *string
	if err := row.Scan(&e.UserID, &e.Username, &e.DisplayName, &avatarKey, &e.TotalXP, &e.Level, &e.Rank); err != nil {
		return e, err
	}
	if avatarKey != nil {
		url := storage.PublicURL(r.mediaBase, *avatarKey)
		e.AvatarURL = &url
	}
	return e, nil
}

// TopByXP returns the highest-XP users (ties share a rank), best first.
func (r *Repository) TopByXP(ctx context.Context, limit int) ([]LeaderboardEntry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+leaderboardCols+`, RANK() OVER (ORDER BY total_xp DESC) AS rank
		   FROM users
		  WHERE deleted_at IS NULL AND total_xp > 0
		  ORDER BY total_xp DESC, id
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query leaderboard: %w", err)
	}
	defer rows.Close()

	entries := []LeaderboardEntry{}
	for rows.Next() {
		e, err := r.scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan leaderboard entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate leaderboard: %w", err)
	}
	return entries, nil
}

// EvaluateBadgesTx computes the user's badge stats inside the check-in tx and
// awards any newly-satisfied badges (idempotently). Returns the keys awarded by
// THIS call, so the caller can surface them (e.g. a toast).
func (r *Repository) EvaluateBadgesTx(ctx context.Context, tx pgx.Tx, userID string) ([]string, error) {
	var s badgeStats
	if err := tx.QueryRow(ctx,
		`SELECT
		   u.checkpoint_count,
		   u.journey_count,
		   (SELECT COUNT(DISTINCT province) FROM checkpoints c
		     WHERE c.user_id = u.id AND c.deleted_at IS NULL AND c.province IS NOT NULL),
		   EXTRACT(HOUR FROM (NOW() AT TIME ZONE 'Asia/Ho_Chi_Minh'))::int
		 FROM users u WHERE u.id = $1`,
		userID).Scan(&s.CheckpointCount, &s.JourneyCount, &s.ProvinceCount, &s.Hour); err != nil {
		return nil, fmt.Errorf("load badge stats: %w", err)
	}

	satisfied := satisfiedBadges(s)
	if len(satisfied) == 0 {
		return nil, nil
	}

	rows, err := tx.Query(ctx,
		`INSERT INTO user_badges (user_id, badge_key)
		 SELECT $1, k FROM unnest($2::text[]) AS k
		 ON CONFLICT DO NOTHING
		 RETURNING badge_key`,
		userID, satisfied)
	if err != nil {
		return nil, fmt.Errorf("award badges: %w", err)
	}
	defer rows.Close()

	var awarded []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan awarded badge: %w", err)
		}
		awarded = append(awarded, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate awarded badges: %w", err)
	}
	return awarded, nil
}

// Badges returns the full catalog annotated with the user's unlock state.
func (r *Repository) Badges(ctx context.Context, userID string) ([]UserBadge, error) {
	rows, err := r.db.Query(ctx,
		`SELECT badge_key, awarded_at FROM user_badges WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query user badges: %w", err)
	}
	defer rows.Close()

	type award struct {
		at time.Time
	}
	awarded := make(map[string]award)
	for rows.Next() {
		var key string
		var at time.Time
		if err := rows.Scan(&key, &at); err != nil {
			return nil, fmt.Errorf("scan user badge: %w", err)
		}
		awarded[key] = award{at: at}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user badges: %w", err)
	}

	out := make([]UserBadge, 0, len(badgeCatalog))
	for _, def := range badgeCatalog {
		ub := UserBadge{Badge: def.Badge}
		if a, ok := awarded[def.Key]; ok {
			ub.Awarded = true
			at := a.at
			ub.AwardedAt = &at
		}
		out = append(out, ub)
	}
	return out, nil
}

// RankOf returns a user's global rank, or nil if they have no XP yet (unranked).
func (r *Repository) RankOf(ctx context.Context, userID string) (*LeaderboardEntry, error) {
	row := r.db.QueryRow(ctx,
		`SELECT `+leaderboardCols+`, rank FROM (
		   SELECT `+leaderboardCols+`, RANK() OVER (ORDER BY total_xp DESC) AS rank
		     FROM users
		    WHERE deleted_at IS NULL AND total_xp > 0
		 ) ranked WHERE id = $1`, userID)
	e, err := r.scanEntry(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query rank: %w", err)
	}
	return &e, nil
}
