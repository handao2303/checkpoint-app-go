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

// SetActive activates or ends a journey. Multiple journeys can be active at once
// (a check-in then picks which one to record under), so this no longer clears
// other active journeys.
func (r *Repository) SetActive(ctx context.Context, id, userID string, active bool) (*Journey, error) {
	var query string
	if active {
		query = `UPDATE journeys SET is_active = true, ended_at = NULL,
		           started_at = COALESCE(started_at, NOW()), updated_at = NOW()
		         WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		         RETURNING ` + journeyColumns
	} else {
		query = `UPDATE journeys SET is_active = false, ended_at = NOW(), updated_at = NOW()
		         WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		         RETURNING ` + journeyColumns
	}
	return getJourney(r.db.QueryRow(ctx, query, id, userID), "set journey active")
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
	if _, err := tx.Exec(ctx, `UPDATE checkpoints SET journey_id = NULL, journey_order = NULL WHERE journey_id = $1`, id); err != nil {
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

// BelongsToUser reports whether the journey exists and is owned by userID — used
// to validate a check-in's chosen journey_id before attaching it.
func (r *Repository) BelongsToUser(ctx context.Context, userID, journeyID string) (bool, error) {
	var ok bool
	err := r.db.QueryRow(ctx,
		`SELECT true FROM journeys WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		journeyID, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check journey ownership: %w", err)
	}
	return ok, nil
}

// AssignCheckpoint attaches a checkpoint to a journey, both owned by userID.
func (r *Repository) AssignCheckpoint(ctx context.Context, journeyID, checkpointID, userID string) error {
	return r.assignCheckpoints(ctx, journeyID, []string{checkpointID}, userID, true)
}

// AssignCheckpoints attaches checkpoints to a journey in the submitted order.
func (r *Repository) AssignCheckpoints(ctx context.Context, journeyID string, checkpointIDs []string, userID string) error {
	return r.assignCheckpoints(ctx, journeyID, checkpointIDs, userID, false)
}

func (r *Repository) assignCheckpoints(ctx context.Context, journeyID string, checkpointIDs []string, userID string, appendOrder bool) (err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin assign checkpoints: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ok bool
	err = tx.QueryRow(ctx,
		`SELECT true FROM journeys WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		journeyID, userID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check journey owner: %w", err)
	}

	baseOrder := 0
	if appendOrder {
		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(MAX(journey_order) + 1, 0)
			 FROM checkpoints WHERE journey_id = $1 AND deleted_at IS NULL`,
			journeyID).Scan(&baseOrder); err != nil {
			return fmt.Errorf("next journey order: %w", err)
		}
	}

	for i, checkpointID := range checkpointIDs {
		tag, err := tx.Exec(ctx,
			`UPDATE checkpoints SET journey_id = $1, journey_order = $2, updated_at = NOW()
			 WHERE id = $3 AND user_id = $4 AND deleted_at IS NULL
			   AND (journey_id IS NULL OR journey_id = $1)`,
			journeyID, baseOrder+i, checkpointID, userID)
		if err != nil {
			return fmt.Errorf("assign checkpoint: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}

	if err := r.recalculateStatsTx(ctx, tx, journeyID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit assign checkpoints: %w", err)
	}
	return nil
}

func (r *Repository) recalculateStatsTx(ctx context.Context, tx pgx.Tx, journeyID string) error {
	_, err := tx.Exec(ctx,
		`UPDATE journeys
		    SET checkpoint_count = stats.checkpoint_count,
		        total_xp = stats.total_xp,
		        started_at = CASE
		          WHEN journeys.is_active THEN journeys.started_at
		          ELSE stats.started_at
		        END,
		        ended_at = CASE
		          WHEN journeys.is_active THEN NULL
		          ELSE stats.ended_at
		        END,
		        updated_at = NOW()
		   FROM (
		     SELECT COUNT(*)::int AS checkpoint_count,
		            COALESCE(SUM(xp_awarded), 0)::int AS total_xp,
		            MIN(created_at) AS started_at,
		            MAX(created_at) AS ended_at
		       FROM checkpoints
		      WHERE journey_id = $1 AND deleted_at IS NULL
		   ) stats
		  WHERE journeys.id = $1`,
		journeyID)
	if err != nil {
		return fmt.Errorf("recalculate journey stats: %w", err)
	}
	return nil
}

// GetDetail returns a journey with stats (count, XP, distance via PostGIS) and
// its ordered stops (J2).
func (r *Repository) GetDetail(ctx context.Context, id string) (*Detail, error) {
	// Fetch journey + denormalized stats in one query.
	var j Journey
	var stats Stats
	if err := r.db.QueryRow(ctx,
		`SELECT `+journeyColumns+`, checkpoint_count, total_xp
		 FROM journeys WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&j.ID, &j.UserID, &j.Title, &j.Description, &j.IsActive,
			&j.StartedAt, &j.EndedAt, &j.CreatedAt, &j.UpdatedAt,
			&stats.CheckpointCount, &stats.TotalXP); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get journey: %w", err)
	}

	// Distance still requires a spatial aggregate.
	if err := r.db.QueryRow(ctx,
		`SELECT COALESCE(ST_Length(ST_MakeLine(location::geometry ORDER BY journey_order ASC NULLS LAST, created_at)::geography) / 1000, 0)
		 FROM checkpoints WHERE journey_id = $1 AND deleted_at IS NULL`, id).
		Scan(&stats.DistanceKm); err != nil {
		return nil, fmt.Errorf("journey distance: %w", err)
	}

	rows, err := r.db.Query(ctx,
		`SELECT c.id, c.name, c.category, c.note, c.address, c.lat, c.lng,
		        c.place_id, c.created_at, c.image_key, c.thumbnail_key,
		        CASE WHEN c.image_key ILIKE '%.mp4' THEN 'video' WHEN c.image_key IS NOT NULL THEN 'image' ELSE NULL END
		 FROM checkpoints c
		 WHERE c.journey_id = $1 AND c.deleted_at IS NULL
		 ORDER BY c.journey_order ASC NULLS LAST, c.created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("journey stops: %w", err)
	}
	defer rows.Close()
	stops := []Stop{}
	for rows.Next() {
		var s Stop
		var imageKey, thumbnailKey *string
		if err := rows.Scan(&s.ID, &s.Name, &s.Category, &s.Note, &s.Address,
			&s.Lat, &s.Lng, &s.PlaceID, &s.CreatedAt, &imageKey, &thumbnailKey, &s.MediaType); err != nil {
			return nil, fmt.Errorf("scan stop: %w", err)
		}
		if imageKey != nil {
			u := storage.PublicURL(r.mediaBase, *imageKey)
			s.ImageURL = &u
		}
		if thumbnailKey != nil {
			u := storage.PublicURL(r.mediaBase, *thumbnailKey)
			s.ThumbnailURL = &u
		}
		stops = append(stops, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &Detail{Journey: &j, Stats: stats, Checkpoints: stops}, nil
}
