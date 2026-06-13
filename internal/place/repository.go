package place

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/quanluon/checkpoint-app/pkg/storage"
)

// ErrNotFound is returned when a place doesn't exist.
var ErrNotFound = errors.New("place not found")

// matchRadiusM and nameSimilarity tune place resolution: a check-in matches an
// existing place within this many metres whose name is at least this similar.
const (
	matchRadiusM   = 75.0
	nameSimilarity = 0.3
	searchMinSim   = 0.2 // looser threshold for explicit name search
)

const placeColumns = `id, name, category, lat, lng, address, checkin_count, created_at, updated_at`

func scanPlace(row pgx.Row) (*Place, error) {
	var p Place
	err := row.Scan(&p.ID, &p.Name, &p.Category, &p.Lat, &p.Lng, &p.Address,
		&p.CheckinCount, &p.CreatedAt, &p.UpdatedAt)
	return &p, err
}

func collectPlaces(rows pgx.Rows) ([]*Place, error) {
	defer rows.Close()
	var out []*Place
	for rows.Next() {
		p, err := scanPlace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan place: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Repository is the Postgres data layer for places.
type Repository struct {
	db        *pgxpool.Pool
	mediaBase string // R2 public base URL, prepended to image keys on read (Discovery)
}

// NewRepository builds a place repository over a pgx pool.
func NewRepository(db *pgxpool.Pool, mediaBase string) *Repository {
	return &Repository{db: db, mediaBase: mediaBase}
}

// ResolveInTx matches a check-in to an existing nearby place with a similar
// name, or creates a new one — all within the caller's transaction so it stays
// atomic with the checkpoint insert. Returns the place id.
func (r *Repository) ResolveInTx(ctx context.Context, tx pgx.Tx, p ResolveParams) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id FROM places
		 WHERE ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
		   AND similarity(name, $4) > $5
		 ORDER BY similarity(name, $4) DESC, location <-> ST_MakePoint($1, $2)::geography
		 LIMIT 1`,
		p.Lng, p.Lat, matchRadiusM, p.Name, nameSimilarity).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("match place: %w", err)
	}

	if err := tx.QueryRow(ctx,
		`INSERT INTO places (name, category, lat, lng, address)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		p.Name, p.Category, p.Lat, p.Lng, p.Address).Scan(&id); err != nil {
		return "", fmt.Errorf("create place: %w", err)
	}
	return id, nil
}

// Search finds places by fuzzy name (pg_trgm), optionally biased toward `near`.
func (r *Repository) Search(ctx context.Context, q string, near *Point, limit int) ([]*Place, error) {
	if near != nil {
		rows, err := r.db.Query(ctx,
			`SELECT `+placeColumns+` FROM places
			 WHERE similarity(name, $1) > $2
			 ORDER BY similarity(name, $1) DESC, location <-> ST_MakePoint($3, $4)::geography
			 LIMIT $5`,
			q, searchMinSim, near.Lng, near.Lat, limit)
		if err != nil {
			return nil, fmt.Errorf("search places (near): %w", err)
		}
		return collectPlaces(rows)
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+placeColumns+` FROM places
		 WHERE similarity(name, $1) > $2
		 ORDER BY similarity(name, $1) DESC, checkin_count DESC
		 LIMIT $3`,
		q, searchMinSim, limit)
	if err != nil {
		return nil, fmt.Errorf("search places: %w", err)
	}
	return collectPlaces(rows)
}

// ListNearby returns places within radiusM metres of (lng, lat), nearest first.
func (r *Repository) ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Place, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+placeColumns+` FROM places
		 WHERE ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
		 ORDER BY location <-> ST_MakePoint($1, $2)::geography
		 LIMIT $4`,
		lng, lat, radiusM, limit)
	if err != nil {
		return nil, fmt.Errorf("list nearby places: %w", err)
	}
	return collectPlaces(rows)
}

// GetDetail returns a place plus aggregated community data (avg rating, recent
// photos, recent reviews) from its check-ins.
func (r *Repository) GetDetail(ctx context.Context, id string) (*Detail, error) {
	p, err := scanPlace(r.db.QueryRow(ctx, `SELECT `+placeColumns+` FROM places WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get place: %w", err)
	}

	var avg *float64
	if err := r.db.QueryRow(ctx,
		`SELECT AVG(rating)::float8 FROM checkpoints WHERE place_id = $1 AND deleted_at IS NULL`,
		id).Scan(&avg); err != nil {
		return nil, fmt.Errorf("place avg rating: %w", err)
	}

	images, err := r.recentImages(ctx, id, 6)
	if err != nil {
		return nil, err
	}
	reviews, err := r.recentReviews(ctx, id, 5)
	if err != nil {
		return nil, err
	}
	return &Detail{Place: p, AvgRating: avg, Images: images, Reviews: reviews}, nil
}

func (r *Repository) recentImages(ctx context.Context, placeID string, limit int) ([]DetailImage, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ci.id, ci.object_key, COALESCE(ci.thumbnail_key, ci.object_key) FROM checkpoint_images ci
		 JOIN checkpoints c ON c.id = ci.checkpoint_id
		 WHERE c.place_id = $1 AND c.deleted_at IS NULL
		 ORDER BY ci.created_at DESC LIMIT $2`, placeID, limit)
	if err != nil {
		return nil, fmt.Errorf("place images: %w", err)
	}
	defer rows.Close()
	var out []DetailImage
	for rows.Next() {
		var img DetailImage
		var thumbnailKey string
		if err := rows.Scan(&img.ID, &img.URL, &thumbnailKey); err != nil {
			return nil, fmt.Errorf("scan place image: %w", err)
		}
		img.URL = storage.PublicURL(r.mediaBase, img.URL)
		thumbnailURL := storage.PublicURL(r.mediaBase, thumbnailKey)
		img.ThumbnailURL = &thumbnailURL
		out = append(out, img)
	}
	return out, rows.Err()
}

// Popular returns the most checked-in places, optionally within radiusM of near.
func (r *Repository) Popular(ctx context.Context, near *Point, radiusM float64, limit int) ([]*Place, error) {
	if near != nil {
		rows, err := r.db.Query(ctx,
			`SELECT `+placeColumns+` FROM places
			 WHERE ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
			 ORDER BY checkin_count DESC, created_at DESC LIMIT $4`,
			near.Lng, near.Lat, radiusM, limit)
		if err != nil {
			return nil, fmt.Errorf("popular places (near): %w", err)
		}
		return collectPlaces(rows)
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+placeColumns+` FROM places ORDER BY checkin_count DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("popular places: %w", err)
	}
	return collectPlaces(rows)
}

// Feed returns community check-ins ordered newest → oldest,
// keyset-paginated on (created_at, id). cursor = "<unix_microseconds>:<id>" of the
// previous page's last item ("" for the first page). No radius limit.
func (r *Repository) Feed(ctx context.Context, lng, lat float64, cursor string, limit int) (*Feed, error) {
	var cursorTime *time.Time
	var cursorID *string
	if parts := strings.SplitN(cursor, ":", 2); cursor != "" && len(parts) == 2 {
		if us, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			t := time.UnixMicro(us).UTC()
			cursorTime = &t
			cursorID = &parts[1]
		}
	}

	rows, err := r.db.Query(ctx,
		`SELECT c.id, c.name, c.category, c.note, c.rating, c.lat, c.lng, c.address,
		        c.place_id, c.xp_awarded, c.created_at,
		        u.username, u.display_name, u.avatar_key,
		        (SELECT object_key FROM checkpoint_images ci
		         WHERE ci.checkpoint_id = c.id ORDER BY sort_order, created_at LIMIT 1),
		        (SELECT COALESCE(thumbnail_key, object_key) FROM checkpoint_images ci
		         WHERE ci.checkpoint_id = c.id ORDER BY sort_order, created_at LIMIT 1),
		        ST_Distance(c.location, ST_MakePoint($1, $2)::geography) AS dist
		 FROM checkpoints c JOIN users u ON u.id = c.user_id
		 WHERE c.deleted_at IS NULL
		   AND ($3::timestamptz IS NULL
		        OR c.created_at < $3
		        OR (c.created_at = $3 AND c.id < $4::uuid))
		 ORDER BY c.created_at DESC, c.id DESC LIMIT $5`,
		lng, lat, cursorTime, cursorID, limit)
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	defer rows.Close()

	items := make([]FeedItem, 0, limit)
	for rows.Next() {
		var it FeedItem
		var avatarKey, imageKey, thumbnailKey *string
		if err := rows.Scan(
			&it.ID, &it.Name, &it.Category, &it.Note, &it.Rating, &it.Lat, &it.Lng, &it.Address,
			&it.PlaceID, &it.XPAwarded, &it.CreatedAt,
			&it.Author.Username, &it.Author.DisplayName, &avatarKey, &imageKey, &thumbnailKey, &it.DistanceM,
		); err != nil {
			return nil, fmt.Errorf("scan feed item: %w", err)
		}
		if avatarKey != nil {
			u := storage.PublicURL(r.mediaBase, *avatarKey)
			it.Author.AvatarURL = &u
		}
		if imageKey != nil {
			u := storage.PublicURL(r.mediaBase, *imageKey)
			it.ImageURL = &u
		}
		if thumbnailKey != nil {
			u := storage.PublicURL(r.mediaBase, *thumbnailKey)
			it.ThumbnailURL = &u
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("feed rows: %w", err)
	}

	feed := &Feed{Items: items}
	if limit > 0 && len(items) == limit {
		last := items[len(items)-1]
		next := strconv.FormatInt(last.CreatedAt.UnixMicro(), 10) + ":" + last.ID
		feed.NextCursor = &next
	}
	return feed, nil
}

func (r *Repository) recentReviews(ctx context.Context, placeID string, limit int) ([]Review, error) {
	rows, err := r.db.Query(ctx,
		`SELECT c.note, c.rating, c.created_at, u.username, u.display_name, u.avatar_key
		 FROM checkpoints c JOIN users u ON u.id = c.user_id
		 WHERE c.place_id = $1 AND c.deleted_at IS NULL AND c.note IS NOT NULL
		 ORDER BY c.created_at DESC LIMIT $2`, placeID, limit)
	if err != nil {
		return nil, fmt.Errorf("place reviews: %w", err)
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var rv Review
		if err := rows.Scan(&rv.Note, &rv.Rating, &rv.CreatedAt,
			&rv.Author.Username, &rv.Author.DisplayName, &rv.Author.AvatarURL); err != nil {
			return nil, fmt.Errorf("scan review: %w", err)
		}
		if rv.Author.AvatarURL != nil {
			url := storage.PublicURL(r.mediaBase, *rv.Author.AvatarURL)
			rv.Author.AvatarURL = &url
		}
		out = append(out, rv)
	}
	return out, rows.Err()
}
