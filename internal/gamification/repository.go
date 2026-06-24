package gamification

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// Repository is the Postgres data layer for gamification. It serves read models
// (passport, leaderboard, badges) and applies XP awards inside a caller's tx.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read
	redis     redis.Cmdable
}

// NewRepository builds a gamification repository over a pgx pool. mediaBase is
// the public base URL used to turn stored image keys into full URLs.
func NewRepository(db *pgxpool.Pool, mediaBase string, redisClient redis.Cmdable) *Repository {
	return &Repository{db: db, mediaBase: mediaBase, redis: usableRedis(redisClient)}
}

// AwardXPTx adds XP to a user within an existing transaction, recomputes their
// level, and appends an xp_event. amount may be negative (a reversal, e.g.
// deleting a check-in); the stored total never drops below zero. Returns the
// new total and level.
//
// Level is derived from XP via the same formula as LevelForXP but expressed in
// SQL so the whole thing is a single atomic UPDATE — no SELECT FOR UPDATE needed.
//
// It deliberately does NOT touch action-specific counters (e.g.
// checkpoint_count); the caller owns those.
func (r *Repository) AwardXPTx(
	ctx context.Context, tx pgx.Tx, userID string, amount int, eventType, refID string,
) (totalXP, level int, err error) {
	// One round-trip: atomically increment XP and derive level in SQL.
	// Both SET expressions reference the OLD total_xp (PostgreSQL evaluates all
	// right-hand sides before applying any update), so the computed values are
	// consistent with each other.
	if err = tx.QueryRow(ctx,
		`UPDATE users
		 SET total_xp   = GREATEST(total_xp + $2, 0),
		     level      = LEAST(FLOOR(SQRT(GREATEST(total_xp + $2, 0) / 100.0))::int + 1, 50),
		     updated_at = NOW()
		 WHERE id = $1
		 RETURNING total_xp, level`,
		userID, amount).Scan(&totalXP, &level); err != nil {
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
	r.invalidateLeaderboard(ctx)
	return totalXP, level, nil
}

// ReverseCheckinDeletesTx reverses XP for a batch of deleted check-ins in one
// user update and one bulk xp_events insert. checkpointIDs and xpAwards must be
// the same length and positionally aligned.
func (r *Repository) ReverseCheckinDeletesTx(
	ctx context.Context, tx pgx.Tx, userID string, checkpointIDs []string, xpAwards []int,
) error {
	if len(checkpointIDs) == 0 {
		return nil
	}
	if len(checkpointIDs) != len(xpAwards) {
		return fmt.Errorf("checkpointIDs/xpAwards length mismatch")
	}
	total := 0
	for _, xp := range xpAwards {
		total += xp
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users
		 SET total_xp   = GREATEST(total_xp - $2, 0),
		     level      = LEAST(FLOOR(SQRT(GREATEST(total_xp - $2, 0) / 100.0))::int + 1, 50),
		     updated_at = NOW()
		 WHERE id = $1`,
		userID, total); err != nil {
		return fmt.Errorf("update xp: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO xp_events (user_id, type, xp, ref_id)
		   SELECT $1, 'checkin_deleted', -d.xp, d.ref_id::uuid
		     FROM unnest($2::text[], $3::int[]) AS d(ref_id, xp)`,
		userID, checkpointIDs, xpAwards); err != nil {
		return fmt.Errorf("insert xp_events: %w", err)
	}
	r.invalidateLeaderboard(ctx)
	return nil
}

func (r *Repository) invalidateLeaderboard(ctx context.Context) {
	if r.redis == nil {
		return
	}
	_ = r.redis.Del(ctx, leaderboardTopCacheKey).Err()
}

// Passport returns the user's province "stamps" — one per province they've
// checked in to — newest visit first, each with a representative cover photo.
// It groups by province_code (ISO) when available, falling back to the name
// for legacy rows.
func (r *Repository) Passport(ctx context.Context, userID string) (*Passport, error) {
	rows, err := r.db.Query(ctx,
		`SELECT g.province_key, 
                COALESCE(g.display_name, '') AS display_name, 
                COALESCE(g.province_code, '') AS province_code, 
                g.checkin_count, g.first_at, g.last_at,
                (SELECT c2.thumbnail_key
                   FROM checkpoints c2
                  WHERE c2.user_id = $1 AND c2.deleted_at IS NULL
                    AND COALESCE(c2.province_code, c2.province) = g.province_key 
                    AND c2.thumbnail_key IS NOT NULL
                  ORDER BY c2.created_at DESC LIMIT 1) AS cover_key
           FROM (
             SELECT COALESCE(province_code, province) AS province_key,
                    MAX(province) AS display_name,
                    MAX(province_code) AS province_code,
                    COUNT(*) AS checkin_count,
                    MIN(created_at) AS first_at, MAX(created_at) AS last_at
               FROM checkpoints
              WHERE user_id = $1 AND deleted_at IS NULL AND (province IS NOT NULL OR province_code IS NOT NULL)
              GROUP BY COALESCE(province_code, province)
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
		var provinceKey string // we group by this but don't return it to UI
		var coverKey *string
		if err := rows.Scan(&provinceKey, &s.Province, &s.ProvinceCode, &s.CheckinCount, &s.FirstAt, &s.LastAt, &coverKey); err != nil {
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
// awards any newly-satisfied badges (idempotently). Returns the badges awarded
// by THIS call (with display metadata), so the caller can surface them.
func (r *Repository) EvaluateBadgesTx(ctx context.Context, tx pgx.Tx, userID string) ([]Badge, error) {
	s, err := loadBadgeStats(ctx, tx, userID)
	if err != nil {
		return nil, err
	}

	return insertBadgeKeys(ctx, tx, userID, satisfiedBadges(s), true)
}

type badgeStatsQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadBadgeStats(ctx context.Context, q badgeStatsQuerier, userID string) (badgeStats, error) {
	var s badgeStats
	if err := q.QueryRow(ctx,
		`SELECT
	           u.checkpoint_count,
	           u.journey_count,
	           (SELECT COUNT(DISTINCT COALESCE(province_code, province)) FROM checkpoints c
	             WHERE c.user_id = u.id AND c.deleted_at IS NULL AND (c.province IS NOT NULL OR c.province_code IS NOT NULL)),
	           EXTRACT(HOUR FROM (NOW() AT TIME ZONE 'Asia/Ho_Chi_Minh'))::int,
	           (SELECT COUNT(*) FROM checkpoints c
	             WHERE c.user_id = u.id AND c.deleted_at IS NULL AND c.category = 'cafe'),
	           (SELECT COUNT(*) FROM checkpoint_reactions cr
	              JOIN checkpoints c ON c.id = cr.checkpoint_id
	             WHERE c.user_id = u.id AND c.deleted_at IS NULL AND cr.user_id <> u.id),
	           (SELECT COUNT(DISTINCT to_char(c.created_at AT TIME ZONE 'Asia/Ho_Chi_Minh', 'IYYY-IW')) FROM checkpoints c
	             WHERE c.user_id = u.id AND c.deleted_at IS NULL
	               AND EXTRACT(ISODOW FROM (c.created_at AT TIME ZONE 'Asia/Ho_Chi_Minh')) IN (6, 7)),
	           (SELECT COUNT(*) FROM checkpoint_images ci
	              JOIN checkpoints c ON c.id = ci.checkpoint_id
	             WHERE c.user_id = u.id AND c.deleted_at IS NULL AND COALESCE(ci.media_type, 'image') = 'image'),
	           (SELECT COUNT(*) FROM places p WHERE p.created_by = u.id)
	         FROM users u WHERE u.id = $1`,
		userID).Scan(
		&s.CheckpointCount,
		&s.JourneyCount,
		&s.ProvinceCount,
		&s.Hour,
		&s.CafeCheckpointCount,
		&s.ReactionReceivedCount,
		&s.WeekendCount,
		&s.ImageCount,
		&s.CreatedPlaceCount,
	); err != nil {
		return badgeStats{}, fmt.Errorf("load badge stats: %w", err)
	}
	return s, nil
}

func insertBadgeKeys(ctx context.Context, tx pgx.Tx, userID string, keys []string, sent bool) ([]Badge, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	rows, err := tx.Query(ctx,
		`INSERT INTO user_badges (user_id, badge_key, sent)
		 SELECT $1, k, $3 FROM unnest($2::text[]) AS k
		 ON CONFLICT DO NOTHING
		 RETURNING badge_key`,
		userID, keys, sent)
	if err != nil {
		return nil, fmt.Errorf("award badges: %w", err)
	}
	defer rows.Close()

	var awarded []Badge
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan awarded badge: %w", err)
		}
		if b, ok := badgeByKey(key); ok {
			awarded = append(awarded, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate awarded badges: %w", err)
	}
	return awarded, nil
}

// ClaimUnsentBadges syncs any eligible-but-missing badges as unsent, returns all
// unsent badges, and marks them sent in the same transaction.
func (r *Repository) ClaimUnsentBadges(ctx context.Context, userID string) ([]Badge, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim unsent badges: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	s, err := loadBadgeStats(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	if _, err := insertBadgeKeys(ctx, tx, userID, satisfiedBadges(s), false); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx,
		`WITH claimed AS (
		    SELECT badge_key, awarded_at
		      FROM user_badges
		     WHERE user_id = $1 AND sent = FALSE
		     ORDER BY awarded_at
		  ), updated AS (
		    UPDATE user_badges ub
		       SET sent = TRUE
		      FROM claimed c
		     WHERE ub.user_id = $1 AND ub.badge_key = c.badge_key
		     RETURNING ub.badge_key, c.awarded_at
		  )
		  SELECT badge_key FROM updated ORDER BY awarded_at`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("claim unsent badges: %w", err)
	}
	defer rows.Close()

	var badges []Badge
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan claimed badge: %w", err)
		}
		if b, ok := badgeByKey(key); ok {
			badges = append(badges, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed badges: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim unsent badges: %w", err)
	}
	return badges, nil
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

// UserIDByUsername returns the user ID for an active user with the given username.
func (r *Repository) UserIDByUsername(ctx context.Context, username string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx, `SELECT id FROM users WHERE username = $1 AND deleted_at IS NULL`, username).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}
