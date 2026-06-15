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
)

// readRepository is the data layer the service depends on (consumer-side
// interface for easy mocking).
type readRepository interface {
	Search(ctx context.Context, q string, near *Point, limit int) ([]*Place, error)
	ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Place, error)
	GetDetail(ctx context.Context, id string) (*Detail, error)
	Popular(ctx context.Context, near *Point, radiusM float64, limit int) ([]*Place, error)
	Feed(ctx context.Context, lng, lat float64, cursor string, limit int) (*Feed, error)
	FeedFollowing(ctx context.Context, followerID string, lng, lat float64, cursor string, limit int) (*Feed, error)
}

// Service holds the place (discovery) read logic.
type Service struct {
	repo    readRepository
	popular *popularCache
	sf      singleflight.Group
}

// NewService builds the place service.
func NewService(repo readRepository) *Service {
	return &Service{repo: repo, popular: newPopularCache(popularTTL)}
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
func (s *Service) Search(ctx context.Context, q string, near *Point, limit int) ([]*Place, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, fmt.Errorf("%w: q is required", ErrInvalidInput)
	}
	return s.repo.Search(ctx, q, near, clampLimit(limit))
}

// ListNearby returns places near a point (radius clamped).
func (s *Service) ListNearby(ctx context.Context, lng, lat, radiusM float64, limit int) ([]*Place, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, fmt.Errorf("%w: lat/lng out of range", ErrInvalidInput)
	}
	if radiusM <= 0 {
		radiusM = defaultRadiusM
	}
	if radiusM > maxRadiusM {
		radiusM = maxRadiusM
	}
	return s.repo.ListNearby(ctx, lng, lat, radiusM, clampLimit(limit))
}

// GetDetail returns a place with aggregated community data.
func (s *Service) GetDetail(ctx context.Context, id string) (*Detail, error) {
	return s.repo.GetDetail(ctx, id)
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
func (s *Service) Popular(ctx context.Context, near *Point, limit int) ([]*Place, error) {
	limit = clampLimit(limit)
	key := popularKey(near, limit)

	if cached, ok := s.popular.get(key); ok {
		return cached, nil
	}
	v, err, _ := s.sf.Do(key, func() (any, error) {
		if cached, ok := s.popular.get(key); ok {
			return cached, nil
		}
		log.Printf("popular: cache miss key=%s", key)
		places, err := s.repo.Popular(ctx, near, popularRadiusM, limit)
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
