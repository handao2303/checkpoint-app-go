package reaction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository is the Postgres data layer for checkpoint reactions.
type Repository struct {
	db *pgxpool.Pool
}

// NewRepository builds a reaction repository over a pgx pool.
func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// Set records (or switches) userID's reaction on a checkpoint. The composite
// primary key makes this an idempotent upsert — one reaction per user per
// checkpoint, updated in place when they pick a different one.
func (r *Repository) Set(ctx context.Context, checkpointID, userID, reaction string) error {
	if _, err := r.db.Exec(ctx,
		`INSERT INTO checkpoint_reactions (checkpoint_id, user_id, reaction)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (checkpoint_id, user_id)
		 DO UPDATE SET reaction = EXCLUDED.reaction, updated_at = NOW()`,
		checkpointID, userID, reaction); err != nil {
		return fmt.Errorf("set reaction: %w", err)
	}
	return nil
}

// AwardSocialScoutIfEligible awards the checkpoint owner after they receive at
// least ten reactions from other users.
func (r *Repository) AwardSocialScoutIfEligible(ctx context.Context, checkpointID, reactorID string) error {
	if _, err := r.db.Exec(ctx,
		`INSERT INTO user_badges (user_id, badge_key, sent)
		 SELECT c.user_id, 'social_scout', FALSE
		   FROM checkpoints c
		  WHERE c.id = $1
		    AND c.deleted_at IS NULL
		    AND c.user_id <> $2
		    AND (
		      SELECT COUNT(*)
		        FROM checkpoint_reactions cr
		        JOIN checkpoints owned ON owned.id = cr.checkpoint_id
		       WHERE owned.user_id = c.user_id
		         AND owned.deleted_at IS NULL
		         AND cr.user_id <> c.user_id
		    ) >= 10
		  ON CONFLICT DO NOTHING`,
		checkpointID, reactorID); err != nil {
		return fmt.Errorf("award social scout: %w", err)
	}
	return nil
}

// Remove deletes userID's reaction on a checkpoint (no-op if absent).
func (r *Repository) Remove(ctx context.Context, checkpointID, userID string) error {
	if _, err := r.db.Exec(ctx,
		`DELETE FROM checkpoint_reactions WHERE checkpoint_id = $1 AND user_id = $2`,
		checkpointID, userID); err != nil {
		return fmt.Errorf("remove reaction: %w", err)
	}
	return nil
}

// Summary returns the per-type counts, total, and the caller's own reaction for
// a checkpoint.
func (r *Repository) Summary(ctx context.Context, checkpointID, userID string) (*Summary, error) {
	rows, err := r.db.Query(ctx,
		`SELECT reaction, COUNT(*) FROM checkpoint_reactions
		 WHERE checkpoint_id = $1 GROUP BY reaction`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("count reactions: %w", err)
	}
	defer rows.Close()

	s := &Summary{Counts: map[string]int{}}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return nil, fmt.Errorf("scan reaction count: %w", err)
		}
		s.Counts[key] = n
		s.Total += n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reaction counts: %w", err)
	}

	// Anonymous caller (optional auth) has no userID — skip the "mine" lookup,
	// both to avoid an "invalid UUID" error and because it's always null.
	if userID == "" {
		return s, nil
	}

	var mine string
	err = r.db.QueryRow(ctx,
		`SELECT reaction FROM checkpoint_reactions WHERE checkpoint_id = $1 AND user_id = $2`,
		checkpointID, userID).Scan(&mine)
	switch {
	case err == nil:
		s.Mine = &mine
	case errors.Is(err, pgx.ErrNoRows):
		// caller hasn't reacted — Mine stays nil
	default:
		return nil, fmt.Errorf("get own reaction: %w", err)
	}
	return s, nil
}
