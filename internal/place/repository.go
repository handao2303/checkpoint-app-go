package place

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	matchRadiusM   = 10.0
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

func (r *Repository) enrichPlaces(ctx context.Context, places []*Place, viewerID *string) error {
	if len(places) == 0 {
		return nil
	}
	ids := make([]string, 0, len(places))
	byID := make(map[string]*Place, len(places))
	for _, p := range places {
		if p == nil {
			continue
		}
		ids = append(ids, p.ID)
		byID[p.ID] = p
	}
	if len(ids) == 0 {
		return nil
	}

	viewer := ""
	if viewerID != nil {
		viewer = *viewerID
	}
	rows, err := r.db.Query(ctx,
		`SELECT input.place_id,
			(SELECT AVG(c.rating)::float8
			 FROM checkpoints c
			 WHERE c.place_id::text = input.place_id AND c.deleted_at IS NULL) AS avg_rating,
			(SELECT COUNT(*)::int
			 FROM checkpoint_images ci
			 JOIN checkpoints c ON c.id = ci.checkpoint_id
			 WHERE c.place_id::text = input.place_id AND c.deleted_at IS NULL AND ci.media_type = 'image') AS image_count,
			(SELECT ci.object_key
			 FROM checkpoint_images ci
			 JOIN checkpoints c ON c.id = ci.checkpoint_id
			 WHERE c.place_id::text = input.place_id AND c.deleted_at IS NULL AND ci.media_type = 'image'
			 ORDER BY ci.created_at DESC
			 LIMIT 1) AS cover_image_key,
			(SELECT COALESCE(ci.thumbnail_key, ci.object_key)
			 FROM checkpoint_images ci
			 JOIN checkpoints c ON c.id = ci.checkpoint_id
			 WHERE c.place_id::text = input.place_id AND c.deleted_at IS NULL AND ci.media_type = 'image'
			 ORDER BY ci.created_at DESC
			 LIMIT 1) AS cover_thumbnail_key,
			EXISTS (
			 SELECT 1
			 FROM checkpoints c
			 WHERE c.place_id::text = input.place_id AND c.deleted_at IS NULL AND c.user_id::text = $2
			) AS viewer_has_checked_in
		 FROM unnest($1::text[]) AS input(place_id)`,
		ids, viewer)
	if err != nil {
		return fmt.Errorf("place summaries: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var avg *float64
		var imageCount int
		var imageKey, thumbnailKey *string
		var viewerHasCheckedIn bool
		if err := rows.Scan(&id, &avg, &imageCount, &imageKey, &thumbnailKey, &viewerHasCheckedIn); err != nil {
			return fmt.Errorf("scan place summary: %w", err)
		}
		p := byID[id]
		if p == nil {
			continue
		}
		p.AvgRating = avg
		p.ImageCount = imageCount
		if imageKey != nil {
			u := storage.PublicURL(r.mediaBase, *imageKey)
			p.CoverImageURL = &u
		}
		if thumbnailKey != nil {
			u := storage.PublicURL(r.mediaBase, *thumbnailKey)
			p.CoverThumbnailURL = &u
		}
		p.ViewerHasCheckedIn = viewerHasCheckedIn
	}
	return rows.Err()
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
func (r *Repository) Search(ctx context.Context, q string, near *Point, limit int, viewerID *string) ([]*Place, error) {
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
		places, err := collectPlaces(rows)
		if err != nil {
			return nil, err
		}
		return places, r.enrichPlaces(ctx, places, viewerID)
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
	places, err := collectPlaces(rows)
	if err != nil {
		return nil, err
	}
	return places, r.enrichPlaces(ctx, places, viewerID)
}

// ListNearby returns places near a point (lng, lat) ordered from nearest to farthest with offset pagination.
func (r *Repository) ListNearby(ctx context.Context, lng, lat float64, limit int, offset int, viewerID *string) ([]*Place, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+placeColumns+` FROM places
		 ORDER BY location <-> ST_MakePoint($1, $2)::geography
		 LIMIT $3 OFFSET $4`,
		lng, lat, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list nearby places: %w", err)
	}
	places, err := collectPlaces(rows)
	if err != nil {
		return nil, err
	}
	return places, r.enrichPlaces(ctx, places, viewerID)
}

// GetDetail returns a place plus aggregated community data (avg rating, recent
// photos, recent reviews) from its check-ins.
func (r *Repository) GetDetail(ctx context.Context, id string, limit, offset int, viewerID *string) (*Detail, error) {
	p, err := scanPlace(r.db.QueryRow(ctx, `SELECT `+placeColumns+` FROM places WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get place: %w", err)
	}

	if err := r.enrichPlaces(ctx, []*Place{p}, viewerID); err != nil {
		return nil, err
	}

	// Recent check‑ins – same shape as FeedItem but without distance.
	checkins, err := r.recentCheckins(ctx, id, limit, offset)
	if err != nil {
		return nil, err
	}

	images, err := r.recentImages(ctx, id, 12)
	if err != nil {
		return nil, err
	}

	return &Detail{Place: p, AvgRating: p.AvgRating, Images: images, Checkins: checkins}, nil
}

// recentCheckins returns the most recent checkpoints for a place.
func (r *Repository) recentCheckins(ctx context.Context, placeID string, limit, offset int) ([]FeedItem, error) {
	rows, err := r.db.Query(ctx,
		`SELECT c.id, c.user_id, c.name, c.category, c.note, c.rating, c.lat, c.lng, c.address,
			c.place_id, c.xp_awarded, c.created_at,
			u.username, u.display_name, u.avatar_key,
			c.image_key, c.thumbnail_key,
			CASE WHEN c.image_key ILIKE '%.mp4' THEN 'video' WHEN c.image_key IS NOT NULL THEN 'image' ELSE NULL END
		 FROM checkpoints c JOIN users u ON u.id = c.user_id
		 WHERE c.place_id = $1 AND c.deleted_at IS NULL
		 ORDER BY c.created_at DESC
		 LIMIT $2 OFFSET $3`, placeID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("place recent checkins: %w", err)
	}
	defer rows.Close()

	var out []FeedItem
	for rows.Next() {
		var it FeedItem
		var avatarKey, imageKey, thumbnailKey *string
		if err := rows.Scan(
			&it.ID, &it.UserID, &it.Name, &it.Category, &it.Note, &it.Rating,
			&it.Lat, &it.Lng, &it.Address, &it.PlaceID, &it.XPAwarded, &it.CreatedAt,
			&it.Author.Username, &it.Author.DisplayName, &avatarKey,
			&imageKey, &thumbnailKey, &it.MediaType,
		); err != nil {
			return nil, fmt.Errorf("scan recent checkin: %w", err)
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
		// Distance will be computed client‑side; keep zero.
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *Repository) recentImages(ctx context.Context, placeID string, limit int) ([]DetailImage, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ci.id, ci.object_key, COALESCE(ci.thumbnail_key, ci.object_key) FROM checkpoint_images ci
		 JOIN checkpoints c ON c.id = ci.checkpoint_id
		 WHERE c.place_id = $1 AND c.deleted_at IS NULL AND ci.media_type = 'image'
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
func (r *Repository) Popular(ctx context.Context, near *Point, radiusM float64, limit int, viewerID *string) ([]*Place, error) {
	if near != nil {
		rows, err := r.db.Query(ctx,
			`SELECT `+placeColumns+` FROM places
			 WHERE ST_DWithin(location, ST_MakePoint($1, $2)::geography, $3)
			 ORDER BY checkin_count DESC, created_at DESC LIMIT $4`,
			near.Lng, near.Lat, radiusM, limit)
		if err != nil {
			return nil, fmt.Errorf("popular places (near): %w", err)
		}
		places, err := collectPlaces(rows)
		if err != nil {
			return nil, err
		}
		return places, r.enrichPlaces(ctx, places, viewerID)
	}
	rows, err := r.db.Query(ctx,
		`SELECT `+placeColumns+` FROM places ORDER BY checkin_count DESC, created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("popular places: %w", err)
	}
	places, err := collectPlaces(rows)
	if err != nil {
		return nil, err
	}
	return places, r.enrichPlaces(ctx, places, viewerID)
}

// feedSelect is the column list shared by Feed and FeedFollowing (distance is
// appended by each query as it parameterizes the reference point differently).
const feedSelect = `c.id, c.user_id, c.name, c.category, c.note, c.rating, c.lat, c.lng, c.address,
	c.place_id, c.xp_awarded, c.created_at,
	u.username, u.display_name, u.avatar_key,
	c.image_key, c.thumbnail_key,
	CASE WHEN c.image_key ILIKE '%.mp4' THEN 'video' WHEN c.image_key IS NOT NULL THEN 'image' ELSE NULL END`

const feedFirstPageQuery = `SELECT ` + feedSelect + `
	FROM checkpoints c JOIN users u ON u.id = c.user_id
	WHERE c.deleted_at IS NULL
	ORDER BY c.created_at DESC, c.id DESC LIMIT $1`

const feedCursorPageQuery = `SELECT ` + feedSelect + `
	FROM checkpoints c JOIN users u ON u.id = c.user_id
	WHERE c.deleted_at IS NULL
	  AND (c.created_at, c.id) < ($1, $2)
	ORDER BY c.created_at DESC, c.id DESC LIMIT $3`

const followingFeedFirstPageQuery = `SELECT ` + feedSelect + `
	FROM checkpoints c JOIN users u ON u.id = c.user_id
	WHERE c.deleted_at IS NULL
	  AND c.user_id IN (SELECT followee_id FROM follows WHERE follower_id = $1)
	ORDER BY c.created_at DESC, c.id DESC LIMIT $2`

const followingFeedCursorPageQuery = `SELECT ` + feedSelect + `
	FROM checkpoints c JOIN users u ON u.id = c.user_id
	WHERE c.deleted_at IS NULL
	  AND c.user_id IN (SELECT followee_id FROM follows WHERE follower_id = $3)
	  AND (c.created_at, c.id) < ($1, $2)
	ORDER BY c.created_at DESC, c.id DESC LIMIT $4`

// parseFeedCursor decodes a "<unix_microseconds>:<id>" keyset cursor.
func parseFeedCursor(cursor string) (*time.Time, *string) {
	if parts := strings.SplitN(cursor, ":", 2); cursor != "" && len(parts) == 2 {
		if us, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			t := time.UnixMicro(us).UTC()
			return &t, &parts[1]
		}
	}
	return nil, nil
}

// scanFeedRows materializes feed rows (feedSelect columns).
func (r *Repository) scanFeedRows(rows pgx.Rows, limit int, userLng, userLat float64) (*Feed, error) {
	defer rows.Close()
	items := make([]FeedItem, 0, limit)
	for rows.Next() {
		var it FeedItem
		var avatarKey, imageKey, thumbnailKey *string
		if err := rows.Scan(
			&it.ID, &it.UserID, &it.Name, &it.Category, &it.Note, &it.Rating, &it.Lat, &it.Lng, &it.Address,
			&it.PlaceID, &it.XPAwarded, &it.CreatedAt,
			&it.Author.Username, &it.Author.DisplayName, &avatarKey, &imageKey, &thumbnailKey, &it.MediaType,
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
		it.DistanceM = haversine(userLat, userLng, it.Lat, it.Lng)
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

// Feed returns community check-ins ordered newest → oldest,
// keyset-paginated on (created_at, id). cursor = "<unix_microseconds>:<id>" of the
// previous page's last item ("" for the first page). No radius limit.
func (r *Repository) Feed(ctx context.Context, lng, lat float64, cursor string, limit int) (*Feed, error) {
	cursorTime, cursorID := parseFeedCursor(cursor)
	var rows pgx.Rows
	var err error
	if cursorTime == nil {
		rows, err = r.db.Query(ctx, feedFirstPageQuery, limit)
	} else {
		rows, err = r.db.Query(ctx, feedCursorPageQuery, cursorTime, cursorID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("feed: %w", err)
	}
	return r.scanFeedRows(rows, limit, lng, lat)
}

// FeedFollowing is Feed restricted to check-ins authored by users that
// followerID follows.
func (r *Repository) FeedFollowing(ctx context.Context, followerID string, lng, lat float64, cursor string, limit int) (*Feed, error) {
	cursorTime, cursorID := parseFeedCursor(cursor)
	var rows pgx.Rows
	var err error
	if cursorTime == nil {
		rows, err = r.db.Query(ctx, followingFeedFirstPageQuery, followerID, limit)
	} else {
		rows, err = r.db.Query(ctx, followingFeedCursorPageQuery, cursorTime, cursorID, followerID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("feed following: %w", err)
	}
	return r.scanFeedRows(rows, limit, lng, lat)
}

// haversine calculates the distance between two coordinates in meters.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0 // Earth radius in meters
	phi1 := lat1 * math.Pi / 180.0
	phi2 := lat2 * math.Pi / 180.0
	dphi := (lat2 - lat1) * math.Pi / 180.0
	dlambda := (lon2 - lon1) * math.Pi / 180.0

	a := math.Sin(dphi/2.0)*math.Sin(dphi/2.0) +
		math.Cos(phi1)*math.Cos(phi2)*
			math.Sin(dlambda/2.0)*math.Sin(dlambda/2.0)
	c := 2.0 * math.Atan2(math.Sqrt(a), math.Sqrt(1.0-a))

	return r * c
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
