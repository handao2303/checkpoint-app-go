package checkpoint

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/quanluon/checkpoint-app/internal/place"
	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// ErrNotFound is returned when a checkpoint doesn't exist (or isn't the
// caller's, for owner-scoped operations).
var ErrNotFound = errors.New("checkpoint not found")

// placeResolver matches/creates a place within a transaction (implemented by
// internal/place). Kept as an interface for decoupling and testing.
type placeResolver interface {
	ResolveInTx(ctx context.Context, tx pgx.Tx, p place.ResolveParams) (string, error)
}

// Repository is the Postgres data layer for checkpoints.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read
	places    placeResolver
}

// NewRepository builds a checkpoint repository over a pgx pool. mediaBase is the
// public base URL for image keys; places resolves a check-in to a Place.
func NewRepository(db *pgxpool.Pool, mediaBase string, places placeResolver) *Repository {
	return &Repository{db: db, mediaBase: mediaBase, places: places}
}

const cpColumns = `id, user_id, name, category, note, rating, lat, lng, address,
	place_id, xp_awarded, created_at, updated_at`

func scanCheckpoint(row pgx.Row) (*Checkpoint, error) {
	var c Checkpoint
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Category, &c.Note, &c.Rating, &c.Lat, &c.Lng,
		&c.Address, &c.PlaceID, &c.XPAwarded, &c.CreatedAt, &c.UpdatedAt,
	)
	return &c, err
}

// Create inserts a checkpoint and awards XP to the user atomically. The user row
// is locked with SELECT ... FOR UPDATE so concurrent check-ins can't lose an XP
// increment, and the new level is computed from the locked total.
func (r *Repository) Create(ctx context.Context, p CreateParams) (result *CreateResult, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	// Rollback is a no-op once Commit has succeeded.
	defer func() { _ = tx.Rollback(ctx) }()

	// Resolve the place (match a nearby one with a similar name, or create it),
	// unless the client checked in to a specific place.
	placeID := p.PlaceID
	if placeID == nil {
		id, err := r.places.ResolveInTx(ctx, tx, place.ResolveParams{
			Name: p.Name, Category: p.Category, Lat: p.Lat, Lng: p.Lng, Address: p.Address,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve place: %w", err)
		}
		placeID = &id
	}

	cp, err := scanCheckpoint(tx.QueryRow(ctx,
		`INSERT INTO checkpoints (user_id, name, category, note, rating, lat, lng, address, place_id, xp_awarded)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		 RETURNING `+cpColumns,
		p.UserID, p.Name, p.Category, p.Note, p.Rating, p.Lat, p.Lng, p.Address, placeID, p.XPReward))
	if err != nil {
		return nil, fmt.Errorf("insert checkpoint: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE places SET checkin_count = checkin_count + 1, updated_at = NOW() WHERE id = $1`,
		placeID); err != nil {
		return nil, fmt.Errorf("bump place checkin_count: %w", err)
	}

	var totalXP int
	if err := tx.QueryRow(ctx,
		`SELECT total_xp FROM users WHERE id = $1 FOR UPDATE`, p.UserID).Scan(&totalXP); err != nil {
		return nil, fmt.Errorf("lock user xp: %w", err)
	}
	totalXP += p.XPReward
	level := levelForXP(totalXP)

	if _, err := tx.Exec(ctx,
		`UPDATE users SET total_xp = $2, level = $3,
		   checkpoint_count = checkpoint_count + 1, updated_at = NOW()
		 WHERE id = $1`,
		p.UserID, totalXP, level); err != nil {
		return nil, fmt.Errorf("award xp: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit checkpoint: %w", err)
	}
	return &CreateResult{Checkpoint: cp, XPAwarded: p.XPReward, TotalXP: totalXP, Level: level}, nil
}

// GetByID returns a checkpoint by id.
func (r *Repository) GetByID(ctx context.Context, id string) (*Checkpoint, error) {
	c, err := scanCheckpoint(r.db.QueryRow(ctx,
		`SELECT `+cpColumns+` FROM checkpoints WHERE id = $1 AND deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get checkpoint: %w", err)
	}
	return c, nil
}

// ListByUser returns a user's checkpoints, newest first.
func (r *Repository) ListByUser(ctx context.Context, userID string, limit int) ([]*Checkpoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cpColumns+` FROM checkpoints
		 WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY created_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	return collectCheckpoints(rows)
}

// ListInBounds returns checkpoints whose location falls inside the given
// bounding box (for the map viewport). Uses the GIST index via the && operator.
func (r *Repository) ListInBounds(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit int) ([]*Checkpoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cpColumns+` FROM checkpoints
		 WHERE deleted_at IS NULL
		   AND location && ST_MakeEnvelope($1, $2, $3, $4, 4326)::geography
		 ORDER BY created_at DESC LIMIT $5`,
		minLng, minLat, maxLng, maxLat, limit)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints in bounds: %w", err)
	}
	return collectCheckpoints(rows)
}

// ListNearby returns checkpoints within radiusM metres of (lng, lat), nearest
// first (ST_DWithin + KNN ordering, both index-backed).
func (r *Repository) ListNearby(ctx context.Context, lng, lat float64, radiusM float64, limit int) ([]*Checkpoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cpColumns+` FROM checkpoints
		 WHERE deleted_at IS NULL
		   AND ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
		 ORDER BY location <-> ST_MakePoint($1, $2)::geography LIMIT $4`,
		lng, lat, radiusM, limit)
	if err != nil {
		return nil, fmt.Errorf("list nearby checkpoints: %w", err)
	}
	return collectCheckpoints(rows)
}

func collectCheckpoints(rows pgx.Rows) ([]*Checkpoint, error) {
	defer rows.Close()
	var out []*Checkpoint
	for rows.Next() {
		c, err := scanCheckpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scan checkpoint: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetDetail returns a checkpoint with its author (joined from users) and images.
func (r *Repository) GetDetail(ctx context.Context, id string) (*Detail, error) {
	cp, err := r.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	var author Author
	if err := r.db.QueryRow(ctx,
		`SELECT username, display_name, avatar_key FROM users WHERE id = $1`, cp.UserID).
		Scan(&author.Username, &author.DisplayName, &author.AvatarURL); err != nil {
		return nil, fmt.Errorf("get checkpoint author: %w", err)
	}
	if author.AvatarURL != nil {
		url := storage.PublicURL(r.mediaBase, *author.AvatarURL)
		author.AvatarURL = &url
	}

	images, err := r.ListImages(ctx, id)
	if err != nil {
		return nil, err
	}
	return &Detail{Checkpoint: cp, Images: images, Author: &author}, nil
}

// ListImages returns a checkpoint's images ordered by sort_order.
func (r *Repository) ListImages(ctx context.Context, checkpointID string) ([]Image, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, object_key, sort_order FROM checkpoint_images
		 WHERE checkpoint_id = $1 ORDER BY sort_order, created_at`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	var out []Image
	for rows.Next() {
		var img Image
		if err := rows.Scan(&img.ID, &img.URL, &img.SortOrder); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		img.URL = storage.PublicURL(r.mediaBase, img.URL)
		out = append(out, img)
	}
	return out, rows.Err()
}

// AddImages inserts image rows (by object key) for a checkpoint and returns them
// with full public URLs.
func (r *Repository) AddImages(ctx context.Context, checkpointID string, keys []string, startOrder int) ([]Image, error) {
	out := make([]Image, 0, len(keys))
	for i, key := range keys {
		var img Image
		if err := r.db.QueryRow(ctx,
			`INSERT INTO checkpoint_images (checkpoint_id, object_key, sort_order)
			 VALUES ($1, $2, $3) RETURNING id, object_key, sort_order`,
			checkpointID, key, startOrder+i).Scan(&img.ID, &img.URL, &img.SortOrder); err != nil {
			return nil, fmt.Errorf("insert image: %w", err)
		}
		img.URL = storage.PublicURL(r.mediaBase, img.URL)
		out = append(out, img)
	}
	return out, nil
}

// CountImages returns how many images a checkpoint already has.
func (r *Repository) CountImages(ctx context.Context, checkpointID string) (int, error) {
	var n int
	if err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM checkpoint_images WHERE checkpoint_id = $1`, checkpointID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count images: %w", err)
	}
	return n, nil
}

// Delete soft-deletes a checkpoint owned by userID. Returns ErrNotFound if it
// doesn't exist or belongs to someone else.
func (r *Repository) Delete(ctx context.Context, id, userID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE checkpoints SET deleted_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
