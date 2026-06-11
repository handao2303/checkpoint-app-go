package gamification

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	leaderboardSize = 100
	leaderboardTTL  = 60 * time.Second
)

// reader is the data the service needs (consumer-side interface for testing).
type reader interface {
	Passport(ctx context.Context, userID string) (*Passport, error)
	TopByXP(ctx context.Context, limit int) ([]LeaderboardEntry, error)
	RankOf(ctx context.Context, userID string) (*LeaderboardEntry, error)
	Badges(ctx context.Context, userID string) ([]UserBadge, error)
}

// Service holds gamification read-model business logic.
type Service struct {
	repo reader
	lb   *lbCache
	sf   singleflight.Group
}

// NewService builds the gamification service.
func NewService(repo reader) *Service {
	return &Service{repo: repo, lb: newLBCache(leaderboardTTL)}
}

// Passport returns the user's province stamps.
func (s *Service) Passport(ctx context.Context, userID string) (*Passport, error) {
	return s.repo.Passport(ctx, userID)
}

// Badges returns the badge catalog annotated with the user's unlock state.
func (s *Service) Badges(ctx context.Context, userID string) ([]UserBadge, error) {
	return s.repo.Badges(ctx, userID)
}

// Leaderboard returns the global top users (cached + single-flighted, since the
// list is identical for everyone) plus the caller's own rank.
func (s *Service) Leaderboard(ctx context.Context, userID string) (*Leaderboard, error) {
	top, ok := s.lb.get()
	if !ok {
		v, err, _ := s.sf.Do("leaderboard:top", func() (any, error) {
			if cached, ok := s.lb.get(); ok {
				return cached, nil
			}
			entries, err := s.repo.TopByXP(ctx, leaderboardSize)
			if err != nil {
				return nil, err
			}
			s.lb.set(entries)
			return entries, nil
		})
		if err != nil {
			return nil, err
		}
		top = v.([]LeaderboardEntry)
	}

	me, err := s.repo.RankOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Leaderboard{Entries: top, Me: me}, nil
}

// lbCache is a single-entry TTL cache for the global leaderboard top list.
type lbCache struct {
	mu      sync.RWMutex
	ttl     time.Duration
	entries []LeaderboardEntry
	expires time.Time
	loaded  bool
}

func newLBCache(ttl time.Duration) *lbCache {
	return &lbCache{ttl: ttl}
}

func (c *lbCache) get() ([]LeaderboardEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded || time.Now().After(c.expires) {
		return nil, false
	}
	return c.entries, true
}

func (c *lbCache) set(entries []LeaderboardEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = entries
	c.expires = time.Now().Add(c.ttl)
	c.loaded = true
}
