package place

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrInvalidInput is returned for failed validation.
var ErrInvalidInput = errors.New("invalid input")

const (
	defaultLimit   = 20
	maxLimit       = 50
	defaultRadiusM = 5000
	maxRadiusM     = 50000
	popularRadiusM = 50000
	popularTTL     = 60 * time.Second
	countTTL       = 60 * time.Second
)

// readRepository is the data layer the service depends on (consumer-side
// interface for easy mocking).
type readRepository interface {
	Search(ctx context.Context, q string, near *Point, limit int, viewerID *string) ([]*Place, error)
	ListNearby(ctx context.Context, lng, lat float64, limit int, offset int, viewerID *string, f NearbyFilter) ([]*Place, error)
	CountPlaces(ctx context.Context, category string) (int, error)
	GetDetail(ctx context.Context, id string, limit int, offset int, viewerID *string) (*Detail, error)
	Popular(ctx context.Context, near *Point, radiusM float64, limit int, viewerID *string) ([]*Place, error)
	Feed(ctx context.Context, lng, lat float64, cursor string, limit int) (*Feed, error)
	FeedFollowing(ctx context.Context, followerID string, lng, lat float64, cursor string, limit int) (*Feed, error)
}

// Service holds the place (discovery) read logic.
type Service struct {
	repo    readRepository
	popular *popularCache
	counts  *countCache
	sf      singleflight.Group
}

// NewService builds the place service.
func NewService(repo readRepository) *Service {
	return &Service{repo: repo, popular: newPopularCache(popularTTL), counts: newCountCache(countTTL)}
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// Search finds places by name. q is required; near optionally biases by distance.
func (s *Service) Search(ctx context.Context, q string, near *Point, limit int, viewerID *string) ([]*Place, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, fmt.Errorf("%w: q is required", ErrInvalidInput)
	}
	return s.repo.Search(ctx, q, near, clampLimit(limit), viewerID)
}

// NearbyFilter narrows the Discover nearby list. An empty/"all" Category means no
// category filter; Sort "popular" orders by check-in count instead of distance.
type NearbyFilter struct {
	Category string
	Sort     string // "" | "distance" | "popular"
}

// ListNearby returns places near a point with offset pagination, narrowed and
// ordered by the optional filter.
func (s *Service) ListNearby(ctx context.Context, lng, lat float64, limit, offset int, viewerID *string, f NearbyFilter) ([]*Place, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.ListNearby(ctx, lng, lat, clampLimit(limit), offset, viewerID, f)
}

// CountNearby returns the total number of places for the Discover list, narrowed
// by category. Cached (TTL) + single-flighted so the COUNT(*) runs at most once
// per category per window even under a burst of paging requests.
func (s *Service) CountNearby(ctx context.Context, category string) (int, error) {
	if category == "" {
		category = "all"
	}
	if cached, ok := s.counts.get(category); ok {
		return cached, nil
	}
	v, err, _ := s.sf.Do("count:"+category, func() (any, error) {
		if cached, ok := s.counts.get(category); ok {
			return cached, nil
		}
		n, err := s.repo.CountPlaces(ctx, category)
		if err != nil {
			return 0, err
		}
		s.counts.set(category, n)
		return n, nil
	})
	if err != nil {
		return 0, err
	}
	return v.(int), nil
}

// GetDetail returns a place with aggregated community data.
func (s *Service) GetDetail(ctx context.Context, id string, limit int, offset int, viewerID *string) (*Detail, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return s.repo.GetDetail(ctx, id, limit, offset, viewerID)
}

// Feed returns community check-ins ordered newest → oldest (keyset cursor).
func (s *Service) Feed(ctx context.Context, lat, lng float64, cursor string, limit int) (*Feed, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	return s.repo.Feed(ctx, lng, lat, cursor, clampLimit(limit))
}

// FeedFollowing returns the feed restricted to users the caller follows.
func (s *Service) FeedFollowing(ctx context.Context, followerID string, lat, lng float64, cursor string, limit int) (*Feed, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	return s.repo.FeedFollowing(ctx, followerID, lng, lat, cursor, clampLimit(limit))
}

// Popular returns top places by check-in count, cached (TTL) and single-flighted
// so a burst of identical requests hits the DB once.
func (s *Service) Popular(ctx context.Context, near *Point, limit int, viewerID *string) ([]*Place, error) {
	limit = clampLimit(limit)
	if viewerID != nil {
		return s.repo.Popular(ctx, near, popularRadiusM, limit, viewerID)
	}
	key := popularKey(near, limit)

	if cached, ok := s.popular.get(key); ok {
		return cached, nil
	}
	v, err, _ := s.sf.Do(key, func() (any, error) {
		if cached, ok := s.popular.get(key); ok {
			return cached, nil
		}
		log.Printf("popular: cache miss key=%s", key)
		places, err := s.repo.Popular(ctx, near, popularRadiusM, limit, nil)
		if err != nil {
			return nil, err
		}
		s.popular.set(key, places)
		return places, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]*Place), nil
}

func popularKey(near *Point, limit int) string {
	if near == nil {
		return fmt.Sprintf("global:%d", limit)
	}
	// Round to ~0.1° (~11km) so nearby users share a cache entry.
	return fmt.Sprintf("%.1f:%.1f:%d", math.Round(near.Lat*10)/10, math.Round(near.Lng*10)/10, limit)
}

// popularCache is a tiny TTL cache for the popular-places query.
type popularCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]popularEntry
}

type popularEntry struct {
	places  []*Place
	expires time.Time
}

func newPopularCache(ttl time.Duration) *popularCache {
	return &popularCache{ttl: ttl, entries: make(map[string]popularEntry)}
}

func (c *popularCache) get(key string) ([]*Place, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.places, true
}

func (c *popularCache) set(key string, places []*Place) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = popularEntry{places: places, expires: time.Now().Add(c.ttl)}
}

// countCache is a tiny TTL cache for per-category place counts.
type countCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries map[string]countEntry
}

type countEntry struct {
	count   int
	expires time.Time
}

func newCountCache(ttl time.Duration) *countCache {
	return &countCache{ttl: ttl, entries: make(map[string]countEntry)}
}

func (c *countCache) get(key string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		return 0, false
	}
	return e.count, true
}

func (c *countCache) set(key string, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = countEntry{count: count, expires: time.Now().Add(c.ttl)}
}
