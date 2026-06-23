package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/quanluon/checkpoint-app/internal/gamification"
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

// gamificationTx applies XP + badge side effects inside the check-in
// transaction (implemented by internal/gamification). Plain return types keep
// checkpoint decoupled from the gamification package.
type gamificationTx interface {
	AwardXPTx(ctx context.Context, tx pgx.Tx, userID string, amount int, eventType, refID string) (totalXP, level int, err error)
	EvaluateBadgesTx(ctx context.Context, tx pgx.Tx, userID string) (awarded []gamification.Badge, err error)
}

// Repository is the Postgres data layer for checkpoints.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read
	places    placeResolver
	xp        gamificationTx
	redis     *redis.Client // optional hot counter for view_count; nil disables it
}

// NewRepository builds a checkpoint repository over a pgx pool. mediaBase is the
// public base URL for image keys; places resolves a check-in to a Place; xp
// applies the XP award, event log, and badge evaluation within the check-in tx;
// redis (may be nil) is the hot view counter.
func NewRepository(db *pgxpool.Pool, mediaBase string, places placeResolver, xp gamificationTx, redisClient *redis.Client) *Repository {
	return &Repository{db: db, mediaBase: mediaBase, places: places, xp: xp, redis: redisClient}
}

const cpColumns = `id, user_id, name, category, note, rating, lat, lng, address,
	place_id, journey_id, xp_awarded, view_count, taken_at, created_at, updated_at`

// cpListColumns extends cpColumns with denormalized cover media fields.
const cpListColumns = cpColumns + `, image_key, thumbnail_key,
	CASE WHEN image_key ILIKE '%.mp4' THEN 'video' WHEN image_key IS NOT NULL THEN 'image' ELSE NULL END AS media_type`

// viewTTL keeps a checkpoint's hot view counter warm in Redis.
const viewTTL = 7 * 24 * time.Hour

func viewKey(id string) string { return "views:" + id }

func mediaTypeOrDefault(t string) string {
	if t == MediaTypeVideo {
		return MediaTypeVideo
	}
	return MediaTypeImage
}

func mediaTypeOrDefaultPtr(t *string) string {
	if t == nil {
		return MediaTypeImage
	}
	return mediaTypeOrDefault(*t)
}

func (r *Repository) viewCount(ctx context.Context, id string) <-chan *int {
	result := make(chan *int, 1)
	go func() {
		n, err := r.redis.Get(ctx, viewKey(id)).Int()
		if err != nil {
			result <- nil
			return
		}
		result <- &n
	}()
	return result
}

func scanCheckpoint(row pgx.Row) (*Checkpoint, error) {
	var c Checkpoint
	err := row.Scan(
		&c.ID, &c.UserID, &c.Name, &c.Category, &c.Note, &c.Rating, &c.Lat, &c.Lng,
		&c.Address, &c.PlaceID, &c.JourneyID, &c.XPAwarded, &c.ViewCount, &c.TakenAt, &c.CreatedAt, &c.UpdatedAt,
	)
	return &c, err
}

// Create inserts a checkpoint and awards XP to the user atomically. The user row
// is locked with SELECT ... FOR UPDATE so concurrent check-ins can't lose an XP
// increment, and the new level is computed from the locked total.
func (r *Repository) Create(ctx context.Context, p CreateParams) (result *CreateResult, err error) {
	if p.RequestHash != "" {
		result, err := r.createResultByRequestHash(ctx, p.UserID, p.RequestHash, true)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

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
		`INSERT INTO checkpoints (user_id, name, category, note, rating, lat, lng, address, province, province_code, place_id, journey_id, taken_at, xp_awarded, request_hash)
	         VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	         RETURNING `+cpColumns,
		p.UserID, p.Name, p.Category, p.Note, p.Rating, p.Lat, p.Lng, p.Address, p.Province, p.ProvinceCode, placeID, p.JourneyID, p.TakenAt, p.XPReward, p.RequestHash))
	if err != nil {
		if isRequestHashUniqueViolation(err) && p.RequestHash != "" {
			_ = tx.Rollback(ctx)
			return r.createResultByRequestHash(ctx, p.UserID, p.RequestHash, true)
		}
		return nil, fmt.Errorf("insert checkpoint: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE places SET checkin_count = checkin_count + 1, updated_at = NOW() WHERE id = $1`,
		placeID); err != nil {
		return nil, fmt.Errorf("bump place checkin_count: %w", err)
	}

	// Award XP + append the xp_event (gamification owns the user XP/level columns
	// and the event log); the row lock it takes is held until this tx commits.
	totalXP, level, err := r.xp.AwardXPTx(ctx, tx, p.UserID, p.XPReward, "checkin", cp.ID)
	if err != nil {
		return nil, fmt.Errorf("award xp: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE users SET checkpoint_count = checkpoint_count + 1, updated_at = NOW()
		 WHERE id = $1`,
		p.UserID); err != nil {
		return nil, fmt.Errorf("bump checkpoint_count: %w", err)
	}

	// Evaluate achievements now that counts/province are up to date.
	newBadges, err := r.xp.EvaluateBadgesTx(ctx, tx, p.UserID)
	if err != nil {
		return nil, fmt.Errorf("evaluate badges: %w", err)
	}

	if err := updateStreakTx(ctx, tx, p.UserID); err != nil {
		return nil, fmt.Errorf("update streak: %w", err)
	}

	if err := insertImagesTx(ctx, tx, cp.ID, p.Images); err != nil {
		return nil, fmt.Errorf("insert images: %w", err)
	}

	if p.JourneyID != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE journeys SET checkpoint_count = checkpoint_count + 1, total_xp = total_xp + $2
			 WHERE id = $1`,
			p.JourneyID, p.XPReward); err != nil {
			return nil, fmt.Errorf("bump journey stats: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit checkpoint: %w", err)
	}

	// Build the image list from the keys that were just inserted (public URLs).
	images := make([]Image, len(p.Images))
	for i, key := range p.Images {
		images[i] = Image{
			URL:       storage.PublicURL(r.mediaBase, key.ObjectKey),
			Type:      mediaTypeOrDefault(key.Type),
			SortOrder: i,
		}
		if key.ThumbnailKey != nil {
			u := storage.PublicURL(r.mediaBase, *key.ThumbnailKey)
			images[i].ThumbnailURL = &u
		} else {
			images[i].Pending = true // awaiting async processing (worker sets thumbnail)
		}
	}

	return &CreateResult{
		Checkpoint: cp, XPAwarded: p.XPReward, TotalXP: totalXP, Level: level, NewBadges: newBadges,
		Images: images,
	}, nil
}

func (r *Repository) createResultByRequestHash(ctx context.Context, userID, requestHash string, duplicate bool) (*CreateResult, error) {
	cp, err := scanCheckpoint(r.db.QueryRow(ctx,
		`SELECT `+cpColumns+` FROM checkpoints
		  WHERE user_id = $1 AND request_hash = $2 AND deleted_at IS NULL
		  ORDER BY created_at DESC
		  LIMIT 1`,
		userID, requestHash))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get checkpoint by request hash: %w", err)
	}
	images, err := r.ListImages(ctx, cp.ID)
	if err != nil {
		return nil, err
	}
	return &CreateResult{
		Checkpoint: cp,
		Duplicate:  duplicate,
		Images:     images,
	}, nil
}

func isRequestHashUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idx_checkpoints_user_request_hash"
}

// updateStreakTx increments or resets the user's daily check-in streak inside the
// check-in transaction. The single UPDATE is safe under concurrent check-ins
// because AwardXPTx already holds a FOR UPDATE lock on the user row.
func updateStreakTx(ctx context.Context, tx pgx.Tx, userID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE users SET
			checkin_streak = CASE
				WHEN last_checkin_date = CURRENT_DATE              THEN checkin_streak
				WHEN last_checkin_date = CURRENT_DATE - INTERVAL '1 day' THEN checkin_streak + 1
				ELSE 1
			END,
			last_checkin_date = GREATEST(COALESCE(last_checkin_date, '1970-01-01'::date), CURRENT_DATE)
		WHERE id = $1`, userID)
	return err
}

// insertImagesTx inserts checkpoint_images rows and updates denormalized cover fields.
func insertImagesTx(ctx context.Context, tx pgx.Tx, checkpointID string, keys []ImageKeys) error {
	for i, key := range keys {
		if _, err := tx.Exec(ctx,
			`INSERT INTO checkpoint_images (checkpoint_id, object_key, thumbnail_key, media_type, sort_order)
			 VALUES ($1, $2, $3, $4, $5)`,
			checkpointID, key.ObjectKey, key.ThumbnailKey, mediaTypeOrDefault(key.Type), i); err != nil {
			return fmt.Errorf("insert image row: %w", err)
		}
	}
	// Only set the cover from the first media if it already has a thumbnail (i.e.
	// it's not awaiting async processing). For raw media that the worker will
	// process (thumbnail_key NULL), the worker sets the cover via RefreshCover —
	// otherwise the cover would point at an undisplayable raw key while pending.
	if len(keys) > 0 && keys[0].ThumbnailKey != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE checkpoints SET image_key = $2, thumbnail_key = $3 WHERE id = $1 AND image_key IS NULL`,
			checkpointID, keys[0].ObjectKey, *keys[0].ThumbnailKey); err != nil {
			return fmt.Errorf("set cover key: %w", err)
		}
	}
	return nil
}

// pendingMedia is one checkpoint media item awaiting async processing
// (thumbnail_key IS NULL).
type pendingMedia struct {
	ID        string
	ObjectKey string
	Type      string
}

// PendingMedia returns the checkpoint's media rows that still need processing
// (thumbnail_key IS NULL), in display order.
func (r *Repository) PendingMedia(ctx context.Context, checkpointID string) ([]pendingMedia, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, object_key, media_type FROM checkpoint_images
		 WHERE checkpoint_id = $1 AND thumbnail_key IS NULL
		 ORDER BY sort_order, created_at`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list pending media: %w", err)
	}
	defer rows.Close()
	var out []pendingMedia
	for rows.Next() {
		var m pendingMedia
		if err := rows.Scan(&m.ID, &m.ObjectKey, &m.Type); err != nil {
			return nil, fmt.Errorf("scan pending media: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMediaThumbnail records the processed thumbnail key for a media row. Setting
// thumbnail_key is the "done" marker — the row is no longer considered pending.
func (r *Repository) SetMediaThumbnail(ctx context.Context, imageID, thumbnailKey string) error {
	if _, err := r.db.Exec(ctx,
		`UPDATE checkpoint_images SET thumbnail_key = $2 WHERE id = $1`, imageID, thumbnailKey); err != nil {
		return fmt.Errorf("set media thumbnail: %w", err)
	}
	return nil
}

// RefreshCover sets the checkpoint's denormalized cover from its first media row,
// once that row has been processed (thumbnail_key set). No-op while the first row
// is still pending.
func (r *Repository) RefreshCover(ctx context.Context, checkpointID string) error {
	if _, err := r.db.Exec(ctx, `
		UPDATE checkpoints c
		SET image_key = ci.object_key, thumbnail_key = ci.thumbnail_key
		FROM (
			SELECT object_key, thumbnail_key FROM checkpoint_images
			WHERE checkpoint_id = $1 ORDER BY sort_order, created_at LIMIT 1
		) ci
		WHERE c.id = $1 AND ci.thumbnail_key IS NOT NULL`, checkpointID); err != nil {
		return fmt.Errorf("refresh cover: %w", err)
	}
	return nil
}

type mediaRow struct {
	ID           string
	ObjectKey    string
	ThumbnailKey *string
	Type         string
}

func (r *Repository) replaceMediaTx(
	ctx context.Context,
	tx pgx.Tx,
	checkpointID string,
	items []UpdateMediaItem,
) (bool, error) {
	existing, err := r.mediaRowsForUpdate(ctx, tx, checkpointID)
	if err != nil {
		return false, err
	}
	existingByID := make(map[string]mediaRow, len(existing))
	for _, row := range existing {
		existingByID[row.ID] = row
	}

	final := make([]ImageKeys, 0, len(items))
	hasPendingMedia := false
	for _, item := range items {
		if item.ID != "" {
			row, ok := existingByID[item.ID]
			if !ok {
				return false, ErrNotFound
			}
			if row.Type != mediaTypeOrDefault(item.Type) {
				return false, fmt.Errorf("%w: media type mismatch", ErrInvalidInput)
			}
			if row.ThumbnailKey == nil {
				hasPendingMedia = true
			}
			final = append(final, ImageKeys{
				ObjectKey:    row.ObjectKey,
				ThumbnailKey: row.ThumbnailKey,
				Type:         row.Type,
			})
			continue
		}

		if item.ThumbnailKey == nil {
			hasPendingMedia = true
		}
		final = append(final, ImageKeys{
			ObjectKey:    item.ObjectKey,
			ThumbnailKey: item.ThumbnailKey,
			Type:         mediaTypeOrDefault(item.Type),
		})
	}

	if _, err := tx.Exec(ctx, `DELETE FROM checkpoint_images WHERE checkpoint_id = $1`, checkpointID); err != nil {
		return false, fmt.Errorf("delete replacement media rows: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE checkpoints SET image_key = NULL, thumbnail_key = NULL WHERE id = $1`,
		checkpointID); err != nil {
		return false, fmt.Errorf("clear cover key: %w", err)
	}
	if err := insertImagesTx(ctx, tx, checkpointID, final); err != nil {
		return false, err
	}
	if err := refreshCoverTx(ctx, tx, checkpointID); err != nil {
		return false, err
	}
	return hasPendingMedia, nil
}

func (r *Repository) mediaRowsForUpdate(ctx context.Context, tx pgx.Tx, checkpointID string) ([]mediaRow, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, object_key, thumbnail_key, media_type
		   FROM checkpoint_images
		  WHERE checkpoint_id = $1
		  ORDER BY sort_order, created_at
		  FOR UPDATE`,
		checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list media for replacement: %w", err)
	}
	defer rows.Close()

	out := []mediaRow{}
	for rows.Next() {
		var row mediaRow
		if err := rows.Scan(&row.ID, &row.ObjectKey, &row.ThumbnailKey, &row.Type); err != nil {
			return nil, fmt.Errorf("scan media for replacement: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate media for replacement: %w", err)
	}
	return out, nil
}

func refreshCoverTx(ctx context.Context, tx pgx.Tx, checkpointID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE checkpoints c
		SET image_key = ci.object_key, thumbnail_key = ci.thumbnail_key
		FROM (
			SELECT object_key, thumbnail_key FROM checkpoint_images
			WHERE checkpoint_id = $1 ORDER BY sort_order, created_at LIMIT 1
		) ci
		WHERE c.id = $1 AND ci.thumbnail_key IS NOT NULL`, checkpointID); err != nil {
		return fmt.Errorf("refresh cover: %w", err)
	}
	return nil
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

// Update edits content fields for a checkpoint owned by userID.
func (r *Repository) Update(ctx context.Context, id, userID string, p UpdateParams) (*UpdateResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	c, err := scanCheckpoint(tx.QueryRow(ctx,
		`UPDATE checkpoints
		    SET name = $3, note = $4, rating = $5, updated_at = NOW()
		  WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		  RETURNING `+cpColumns,
		id, userID, p.Name, p.Note, p.Rating))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update checkpoint: %w", err)
	}

	result := &UpdateResult{Checkpoint: c}
	if p.ReplaceMedia {
		hasPendingMedia, err := r.replaceMediaTx(ctx, tx, id, p.Media)
		if err != nil {
			return nil, err
		}
		result.HasPendingMedia = hasPendingMedia
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update checkpoint: %w", err)
	}
	return result, nil
}

// ListByUser returns a user's checkpoints (newest first) with cover image URLs.
func (r *Repository) ListByUser(ctx context.Context, userID string, provinceCode string, limit int) ([]*Checkpoint, error) {
	return r.ListByUserPage(ctx, userID, provinceCode, limit, nil)
}

// ListByUserPage returns a user's checkpoints using keyset pagination.
func (r *Repository) ListByUserPage(ctx context.Context, userID string, provinceCode string, limit int, cursor *CheckpointCursor) ([]*Checkpoint, error) {
	if provinceCode != "" {
		query := `SELECT ` + cpListColumns + ` FROM checkpoints
			 WHERE user_id = $1 AND deleted_at IS NULL AND province_code = $2`
		args := []any{userID, provinceCode, limit}
		if cursor != nil {
			query += ` AND (created_at, id) < ($4::timestamptz, $5::uuid)`
			args = append(args, cursor.CreatedAt, cursor.ID)
		}
		query += ` ORDER BY created_at DESC, id DESC LIMIT $3`
		rows, err := r.db.Query(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list checkpoints by province: %w", err)
		}
		return r.collectCheckpoints(rows)
	}

	query := `SELECT ` + cpListColumns + ` FROM checkpoints
		 WHERE user_id = $1 AND deleted_at IS NULL`
	args := []any{userID, limit}
	if cursor != nil {
		query += ` AND (created_at, id) < ($3::timestamptz, $4::uuid)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $2`
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	return r.collectCheckpoints(rows)
}

// ListUnassignedByUser returns caller-owned checkpoints that are not in any
// journey, newest first.
func (r *Repository) ListUnassignedByUser(ctx context.Context, userID string, limit int, cursor *CheckpointCursor) ([]*Checkpoint, error) {
	query := `SELECT ` + cpListColumns + ` FROM checkpoints
		 WHERE user_id = $1 AND deleted_at IS NULL AND journey_id IS NULL`
	args := []any{userID, limit}
	if cursor != nil {
		query += ` AND (created_at, id) < ($3::timestamptz, $4::uuid)`
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT $2`
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list unassigned checkpoints: %w", err)
	}
	return r.collectCheckpoints(rows)
}

// ListInBounds returns checkpoints whose location falls inside the given
// bounding box (for the map viewport). Uses the GIST index via the && operator.
// The optional filter narrows by category and/or ownership (mine/following);
// a mine/following mode with no viewer yields no rows.
func (r *Repository) ListInBounds(ctx context.Context, minLng, minLat, maxLng, maxLat float64, limit int, f BoundsFilter) ([]*Checkpoint, error) {
	args := []any{minLng, minLat, maxLng, maxLat}
	where := []string{
		"deleted_at IS NULL",
		"location && ST_MakeEnvelope($1, $2, $3, $4, 4326)::geography",
	}
	if f.Category != "" && f.Category != "all" {
		args = append(args, f.Category)
		where = append(where, fmt.Sprintf("category = $%d", len(args)))
	}
	switch f.Mode {
	case "mine":
		if f.ViewerID == nil {
			return []*Checkpoint{}, nil
		}
		args = append(args, *f.ViewerID)
		where = append(where, fmt.Sprintf("user_id = $%d", len(args)))
	case "following":
		if f.ViewerID == nil {
			return []*Checkpoint{}, nil
		}
		args = append(args, *f.ViewerID)
		where = append(where, fmt.Sprintf("user_id IN (SELECT followee_id FROM follows WHERE follower_id = $%d)", len(args)))
	}
	args = append(args, limit)
	query := `SELECT ` + cpListColumns + ` FROM checkpoints WHERE ` +
		strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints in bounds: %w", err)
	}
	return r.collectCheckpoints(rows)
}

// ListGlobal returns the newest checkpoints for world-level map views without a
// bbox. The same category/ownership filters apply.
func (r *Repository) ListGlobal(ctx context.Context, limit int, f BoundsFilter) ([]*Checkpoint, error) {
	args := []any{}
	where := []string{"deleted_at IS NULL"}
	if f.Category != "" && f.Category != "all" {
		args = append(args, f.Category)
		where = append(where, fmt.Sprintf("category = $%d", len(args)))
	}
	switch f.Mode {
	case "mine":
		if f.ViewerID == nil {
			return []*Checkpoint{}, nil
		}
		args = append(args, *f.ViewerID)
		where = append(where, fmt.Sprintf("user_id = $%d", len(args)))
	case "following":
		if f.ViewerID == nil {
			return []*Checkpoint{}, nil
		}
		args = append(args, *f.ViewerID)
		where = append(where, fmt.Sprintf("user_id IN (SELECT followee_id FROM follows WHERE follower_id = $%d)", len(args)))
	}
	args = append(args, limit)
	query := `SELECT ` + cpListColumns + ` FROM checkpoints WHERE ` +
		strings.Join(where, " AND ") +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list global checkpoints: %w", err)
	}
	return r.collectCheckpoints(rows)
}

// ListNearby returns checkpoints within radiusM metres of (lng, lat), nearest
// first (ST_DWithin + KNN ordering, both index-backed).
func (r *Repository) ListNearby(ctx context.Context, lng, lat float64, radiusM float64, limit int) ([]*Checkpoint, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+cpListColumns+` FROM checkpoints
		 WHERE deleted_at IS NULL
		   AND ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
		 ORDER BY location <-> ST_MakePoint($1, $2)::geography LIMIT $4`,
		lng, lat, radiusM, limit)
	if err != nil {
		return nil, fmt.Errorf("list nearby checkpoints: %w", err)
	}
	return r.collectCheckpoints(rows)
}

func (r *Repository) collectCheckpoints(rows pgx.Rows) ([]*Checkpoint, error) {
	defer rows.Close()
	var out []*Checkpoint
	for rows.Next() {
		var c Checkpoint
		var imageKey, thumbnailKey, mediaType *string
		if err := rows.Scan(
			&c.ID, &c.UserID, &c.Name, &c.Category, &c.Note, &c.Rating, &c.Lat, &c.Lng,
			&c.Address, &c.PlaceID, &c.JourneyID, &c.XPAwarded, &c.ViewCount, &c.TakenAt, &c.CreatedAt, &c.UpdatedAt,
			&imageKey, &thumbnailKey, &mediaType,
		); err != nil {
			return nil, fmt.Errorf("scan checkpoint: %w", err)
		}
		if imageKey != nil {
			u := storage.PublicURL(r.mediaBase, *imageKey)
			c.ImageURL = &u
		}
		if thumbnailKey != nil {
			u := storage.PublicURL(r.mediaBase, *thumbnailKey)
			c.ThumbnailURL = &u
		}
		c.MediaType = mediaType
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GetDetail returns a checkpoint with its author (joined from users) and images.
func (r *Repository) GetDetail(ctx context.Context, id string) (*Detail, error) {
	var hotViewCount <-chan *int
	if r.redis != nil {
		hotViewCount = r.viewCount(ctx, id)
	}

	rows, err := r.db.Query(ctx,
		`SELECT c.id, c.user_id, c.name, c.category, c.note, c.rating, c.lat, c.lng, c.address,
		        c.place_id, c.journey_id, c.xp_awarded, c.view_count, c.taken_at, c.created_at, c.updated_at,
		        c.image_key, c.thumbnail_key,
		        u.username, u.display_name, u.avatar_key,
		        ci.id, ci.object_key, ci.thumbnail_key, ci.media_type, ci.sort_order
		 FROM checkpoints c
		 JOIN users u ON u.id = c.user_id
		 LEFT JOIN checkpoint_images ci ON ci.checkpoint_id = c.id
		 WHERE c.id = $1 AND c.deleted_at IS NULL
		 ORDER BY ci.sort_order, ci.created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("get checkpoint detail: %w", err)
	}
	defer rows.Close()

	var detail Detail
	for rows.Next() {
		var cp Checkpoint
		var author Author
		var imageID, objectKey, thumbnailKey *string
		var cpImageKey, cpThumbnailKey *string
		var mediaType *string
		var sortOrder *int
		if err := rows.Scan(
			&cp.ID, &cp.UserID, &cp.Name, &cp.Category, &cp.Note, &cp.Rating, &cp.Lat, &cp.Lng,
			&cp.Address, &cp.PlaceID, &cp.JourneyID, &cp.XPAwarded, &cp.ViewCount, &cp.TakenAt, &cp.CreatedAt, &cp.UpdatedAt,
			&cpImageKey, &cpThumbnailKey,
			&author.Username, &author.DisplayName, &author.AvatarURL,
			&imageID, &objectKey, &thumbnailKey, &mediaType, &sortOrder,
		); err != nil {
			return nil, fmt.Errorf("scan checkpoint detail: %w", err)
		}

		if detail.Checkpoint == nil {
			if author.AvatarURL != nil {
				url := storage.PublicURL(r.mediaBase, *author.AvatarURL)
				author.AvatarURL = &url
			}
			if cpImageKey != nil {
				url := storage.PublicURL(r.mediaBase, *cpImageKey)
				cp.ImageURL = &url
			}
			if cpThumbnailKey != nil {
				url := storage.PublicURL(r.mediaBase, *cpThumbnailKey)
				cp.ThumbnailURL = &url
			}
			detail.Checkpoint = &cp
			detail.Author = &author
		}

		if imageID != nil {
			img := Image{ID: *imageID, URL: *objectKey, Type: mediaTypeOrDefaultPtr(mediaType), SortOrder: *sortOrder}
			img.URL = storage.PublicURL(r.mediaBase, img.URL)
			if thumbnailKey != nil {
				thumbnailURL := storage.PublicURL(r.mediaBase, *thumbnailKey)
				img.ThumbnailURL = &thumbnailURL
			} else {
				// No thumbnail yet → still being processed by the media worker.
				img.Pending = true
			}
			detail.Images = append(detail.Images, img)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkpoint detail: %w", err)
	}
	if detail.Checkpoint == nil {
		return nil, ErrNotFound
	}

	// Overlay the hot Redis counter when present — it leads the lazily-persisted
	// DB column. The lookup runs alongside the DB query to avoid another
	// sequential remote round trip.
	if hotViewCount != nil {
		if n := <-hotViewCount; n != nil {
			detail.Checkpoint.ViewCount = *n
		}
	}

	return &detail, nil
}

// ListImages returns a checkpoint's images ordered by sort_order.
func (r *Repository) ListImages(ctx context.Context, checkpointID string) ([]Image, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, object_key, COALESCE(thumbnail_key, object_key), media_type, sort_order FROM checkpoint_images
		 WHERE checkpoint_id = $1 ORDER BY sort_order, created_at`, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("list images: %w", err)
	}
	defer rows.Close()

	var out []Image
	for rows.Next() {
		var img Image
		var thumbnailKey string
		if err := rows.Scan(&img.ID, &img.URL, &thumbnailKey, &img.Type, &img.SortOrder); err != nil {
			return nil, fmt.Errorf("scan image: %w", err)
		}
		img.URL = storage.PublicURL(r.mediaBase, img.URL)
		thumbnailURL := storage.PublicURL(r.mediaBase, thumbnailKey)
		img.ThumbnailURL = &thumbnailURL
		out = append(out, img)
	}
	return out, rows.Err()
}

// AddImages inserts image rows (by object key) for a checkpoint and returns them
// with full public URLs. Also maintains the denormalized cover fields.
func (r *Repository) AddImages(ctx context.Context, checkpointID string, keys []ImageKeys, startOrder int) ([]Image, error) {
	out := make([]Image, 0, len(keys))
	for i, key := range keys {
		var img Image
		var thumbnailKey *string
		if err := r.db.QueryRow(ctx,
			`INSERT INTO checkpoint_images (checkpoint_id, object_key, thumbnail_key, media_type, sort_order)
			 VALUES ($1, $2, $3, $4, $5) RETURNING id, object_key, thumbnail_key, media_type, sort_order`,
			checkpointID, key.ObjectKey, key.ThumbnailKey, mediaTypeOrDefault(key.Type), startOrder+i).
			Scan(&img.ID, &img.URL, &thumbnailKey, &img.Type, &img.SortOrder); err != nil {
			return nil, fmt.Errorf("insert image: %w", err)
		}
		img.URL = storage.PublicURL(r.mediaBase, img.URL)
		thumbnailURL := img.URL
		if thumbnailKey != nil {
			thumbnailURL = storage.PublicURL(r.mediaBase, *thumbnailKey)
		}
		img.ThumbnailURL = &thumbnailURL
		out = append(out, img)
	}
	// Set denorm cover if not already populated (only first image matters)
	if len(keys) > 0 && startOrder == 0 {
		coverThumb := keys[0].ObjectKey
		if keys[0].ThumbnailKey != nil {
			coverThumb = *keys[0].ThumbnailKey
		}
		if _, err := r.db.Exec(ctx,
			`UPDATE checkpoints SET image_key = $2, thumbnail_key = $3 WHERE id = $1 AND image_key IS NULL`,
			checkpointID, keys[0].ObjectKey, coverThumb); err != nil {
			return nil, fmt.Errorf("set cover key: %w", err)
		}
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

// Delete HARD-deletes a checkpoint owned by userID, atomically reversing its
// side effects: XP/level (negative xp_event), checkpoint_count, the place's
// checkin_count — and the place itself if it becomes orphaned.
func (r *Repository) Delete(ctx context.Context, id, userID string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Atomically delete and return the side-effect values in one statement —
	// no SELECT FOR UPDATE needed. A concurrent delete of the same row blocks
	// on the implicit write lock until this tx commits, then sees 0 rows and
	// gets ErrNotFound; double XP reversal is impossible.
	var placeID, journeyID *string
	var xpAwarded int
	err = tx.QueryRow(ctx,
		`DELETE FROM checkpoints WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
		 RETURNING place_id, xp_awarded, journey_id`,
		id, userID).Scan(&placeID, &xpAwarded, &journeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete checkpoint: %w", err)
	}

	// Reverse the XP award and the checkpoint counter.
	if _, _, err := r.xp.AwardXPTx(ctx, tx, userID, -xpAwarded, "checkin_deleted", id); err != nil {
		return fmt.Errorf("reverse xp: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET checkpoint_count = GREATEST(checkpoint_count - 1, 0), updated_at = NOW()
		 WHERE id = $1`, userID); err != nil {
		return fmt.Errorf("drop checkpoint_count: %w", err)
	}

	if journeyID != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE journeys SET checkpoint_count = GREATEST(checkpoint_count - 1, 0),
			                     total_xp = GREATEST(total_xp - $2, 0)
			 WHERE id = $1`, journeyID, xpAwarded); err != nil {
			return fmt.Errorf("decrement journey stats: %w", err)
		}
	}

	// Un-count the place; if no checkpoint references it anymore it's orphaned —
	// hard-delete it too.
	if placeID != nil {
		if _, err := tx.Exec(ctx,
			`UPDATE places SET checkin_count = GREATEST(checkin_count - 1, 0), updated_at = NOW()
			 WHERE id = $1`, placeID); err != nil {
			return fmt.Errorf("drop place checkin_count: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM places p WHERE p.id = $1
			 AND NOT EXISTS (SELECT 1 FROM checkpoints c WHERE c.place_id = p.id)`,
			placeID); err != nil {
			return fmt.Errorf("delete orphaned place: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete: %w", err)
	}
	return nil
}

// IncrementView bumps a checkpoint's view count and returns the new total.
// Redis is the hot counter (seeded from the DB on a miss); the DB column is
// updated lazily in the background so it survives a cache flush. When Redis is
// unavailable it falls back to a synchronous DB increment.
func (r *Repository) IncrementView(ctx context.Context, id string) (int, error) {
	if r.redis != nil {
		key := viewKey(id)
		// Seed the counter from the DB the first time we see this checkpoint, so
		// the Redis value reflects the real historical total rather than starting
		// at 1.
		if exists, err := r.redis.Exists(ctx, key).Result(); err == nil && exists == 0 {
			var base int
			derr := r.db.QueryRow(ctx,
				`SELECT view_count FROM checkpoints WHERE id = $1 AND deleted_at IS NULL`, id).Scan(&base)
			if errors.Is(derr, pgx.ErrNoRows) {
				return 0, ErrNotFound
			}
			if derr != nil {
				return 0, fmt.Errorf("seed view count: %w", derr)
			}
			r.redis.SetNX(ctx, key, base, viewTTL)
		}
		if n, err := r.redis.Incr(ctx, key).Result(); err == nil {
			r.redis.Expire(ctx, key, viewTTL)
			total := int(n)
			// Persist toward the truth without blocking the request. GREATEST keeps
			// the column monotonic even if writes arrive out of order.
			go func() {
				bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = r.db.Exec(bg,
					`UPDATE checkpoints SET view_count = GREATEST(view_count, $2) WHERE id = $1`, id, total)
			}()
			return total, nil
		}
	}

	// Redis unavailable → increment the DB directly.
	var count int
	err := r.db.QueryRow(ctx,
		`UPDATE checkpoints SET view_count = view_count + 1 WHERE id = $1 AND deleted_at IS NULL
		 RETURNING view_count`, id).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("increment view count: %w", err)
	}
	return count, nil
}
