package journey

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// ErrNotFound is returned when a journey doesn't exist (or isn't the caller's).
var ErrNotFound = errors.New("journey not found")

// Repository is the Postgres data layer for journeys.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read
}

// NewRepository builds a journey repository over a pgx pool.
func NewRepository(db *pgxpool.Pool, mediaBase string) *Repository {
	return &Repository{db: db, mediaBase: mediaBase}
}

const journeyColumns = `id, user_id, title, description, is_active, started_at, ended_at, created_at, updated_at`

func scanJourney(row pgx.Row) (*Journey, error) {
	var j Journey
	err := row.Scan(&j.ID, &j.UserID, &j.Title, &j.Description, &j.IsActive,
		&j.StartedAt, &j.EndedAt, &j.CreatedAt, &j.UpdatedAt)
	return &j, err
}

func getJourney(row pgx.Row, op string) (*Journey, error) {
	j, err := scanJourney(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	return j, nil
}

// Create inserts a journey and bumps the user's journey_count atomically.
func (r *Repository) Create(ctx context.Context, p CreateParams) (j *Journey, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	j, err = getJourney(tx.QueryRow(ctx,
		`INSERT INTO journeys (user_id, title, description) VALUES ($1, $2, $3)
		 RETURNING `+journeyColumns, p.UserID, p.Title, p.Description), "create journey")
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET journey_count = journey_count + 1, updated_at = NOW() WHERE id = $1`,
		p.UserID); err != nil {
		return nil, fmt.Errorf("bump journey_count: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit journey: %w", err)
	}
	return j, nil
}

// ListByUser returns a user's journeys, newest first.
func (r *Repository) ListByUser(ctx context.Context, userID string) ([]*Journey, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+journeyColumns+` FROM journeys
		 WHERE user_id = $1 AND deleted_at IS NULL ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list journeys: %w", err)
	}
	defer rows.Close()
	var out []*Journey
	for rows.Next() {
		j, err := scanJourney(rows)
		if err != nil {
			return nil, fmt.Errorf("scan journey: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetByID returns a journey by id.
func (r *Repository) GetByID(ctx context.Context, id string) (*Journey, error) {
	return getJourney(r.db.QueryRow(ctx,
		`SELECT `+journeyColumns+` FROM journeys WHERE id = $1 AND deleted_at IS NULL`, id),
		"get journey")
}

// Update sets title/description (COALESCE) for an owned journey.
func (r *Repository) Update(ctx context.Context, id, userID string, title, description *string) (*Journey, error) {
	return getJourney(r.db.QueryRow(ctx,
		`UPDATE journeys SET title = COALESCE($3, title), description = COALESCE($4, description),
		   updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		 RETURNING `+journeyColumns, id, userID, title, description), "update journey")
}

// SetActive activates (deactivating any other active journey first, to respect
// the one-active unique index) or ends a journey.
func (r *Repository) SetActive(ctx context.Context, id, userID string, active bool) (j *Journey, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if active {
		if _, err := tx.Exec(ctx,
			`UPDATE journeys SET is_active = false, updated_at = NOW()
			 WHERE user_id = $1 AND is_active AND deleted_at IS NULL`, userID); err != nil {
			return nil, fmt.Errorf("clear active journey: %w", err)
		}
		j, err = getJourney(tx.QueryRow(ctx,
			`UPDATE journeys SET is_active = true, ended_at = NULL,
			   started_at = COALESCE(started_at, NOW()), updated_at = NOW()
			 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
			 RETURNING `+journeyColumns, id, userID), "activate journey")
	} else {
		j, err = getJourney(tx.QueryRow(ctx,
			`UPDATE journeys SET is_active = false, ended_at = NOW(), updated_at = NOW()
			 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
			 RETURNING `+journeyColumns, id, userID), "deactivate journey")
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit set active: %w", err)
	}
	return j, nil
}

// Delete soft-deletes an owned journey, detaches its check-ins, and decrements
// the user's journey_count.
func (r *Repository) Delete(ctx context.Context, id, userID string) (err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE journeys SET deleted_at = NOW(), is_active = false, updated_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		return fmt.Errorf("delete journey: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `UPDATE checkpoints SET journey_id = NULL WHERE journey_id = $1`, id); err != nil {
		return fmt.Errorf("detach checkpoints: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET journey_count = GREATEST(journey_count - 1, 0), updated_at = NOW() WHERE id = $1`,
		userID); err != nil {
		return fmt.Errorf("decrement journey_count: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete: %w", err)
	}
	return nil
}

// ActiveJourneyID returns the user's active journey id, or nil if none.
func (r *Repository) ActiveJourneyID(ctx context.Context, userID string) (*string, error) {
	var id string
	err := r.db.QueryRow(ctx,
		`SELECT id FROM journeys WHERE user_id = $1 AND is_active AND deleted_at IS NULL`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("active journey: %w", err)
	}
	return &id, nil
}

// AssignCheckpoint attaches a checkpoint to a journey, both owned by userID.
func (r *Repository) AssignCheckpoint(ctx context.Context, journeyID, checkpointID, userID string) error {
	var ok bool
	err := r.db.QueryRow(ctx,
		`SELECT true FROM journeys WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		journeyID, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check journey owner: %w", err)
	}
	tag, err := r.db.Exec(ctx,
		`UPDATE checkpoints SET journey_id = $1, updated_at = NOW()
		 WHERE id = $2 AND user_id = $3 AND deleted_at IS NULL`, journeyID, checkpointID, userID)
	if err != nil {
		return fmt.Errorf("assign checkpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetDetail returns a journey with stats (count, XP, distance via PostGIS) and
// its ordered stops (J2).
func (r *Repository) GetDetail(ctx context.Context, id string) (*Detail, error) {
	j, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	var stats Stats
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(xp_awarded), 0),
		        COALESCE(ST_Length(ST_MakeLine(location::geometry ORDER BY created_at)::geography) / 1000, 0)
		 FROM checkpoints WHERE journey_id = $1 AND deleted_at IS NULL`, id).
		Scan(&stats.CheckpointCount, &stats.TotalXP, &stats.DistanceKm); err != nil {
		return nil, fmt.Errorf("journey stats: %w", err)
	}

	rows, err := r.db.Query(ctx,
		`SELECT c.id, c.name, c.category, c.note, c.address, c.lat, c.lng, c.place_id, c.created_at,
		        COALESCE((
		          SELECT array_agg(ci.object_key ORDER BY ci.sort_order, ci.created_at)
		          FROM checkpoint_images ci WHERE ci.checkpoint_id = c.id
		        ), '{}')
		 FROM checkpoints c
		 WHERE c.journey_id = $1 AND c.deleted_at IS NULL ORDER BY c.created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("journey stops: %w", err)
	}
	defer rows.Close()
	stops := []Stop{}
	for rows.Next() {
		var s Stop
		var keys []string
		if err := rows.Scan(&s.ID, &s.Name, &s.Category, &s.Note, &s.Address,
			&s.Lat, &s.Lng, &s.PlaceID, &s.CreatedAt, &keys); err != nil {
			return nil, fmt.Errorf("scan stop: %w", err)
		}
		s.Images = make([]string, 0, len(keys))
		for _, k := range keys {
			s.Images = append(s.Images, storage.PublicURL(r.mediaBase, k))
		}
		stops = append(stops, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &Detail{Journey: j, Stats: stats, Checkpoints: stops}, nil
}
