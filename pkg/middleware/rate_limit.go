package middleware

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/quanluon/checkpoint-app/pkg/authctx"
	"github.com/quanluon/checkpoint-app/pkg/respond"
)

const (
	defaultRateLimitEntryTTL        = 10 * time.Minute
	defaultRateLimitCleanupInterval = time.Minute
)

// RateLimitConfig controls an in-memory token-bucket limiter.
type RateLimitConfig struct {
	Enabled           bool
	RequestsPerMinute int
	Burst             int
}

type rateLimitEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimiter holds independent token buckets keyed by IP or authenticated user.
// Its limits apply per process, so Cloud Run instances do not share quota.
type RateLimiter struct {
	enabled         bool
	limit           rate.Limit
	burst           int
	retryAfter      string
	entryTTL        time.Duration
	cleanupInterval time.Duration
	now             func() time.Time

	mu          sync.Mutex
	entries     map[string]*rateLimitEntry
	lastCleanup time.Time
}

// NewRateLimiter builds a concurrency-safe in-memory limiter.
func NewRateLimiter(cfg RateLimitConfig) *RateLimiter {
	requestsPerMinute := max(cfg.RequestsPerMinute, 1)
	burst := max(cfg.Burst, 1)
	retrySeconds := max(int(math.Ceil(60/float64(requestsPerMinute))), 1)
	now := time.Now

	return &RateLimiter{
		enabled:         cfg.Enabled,
		limit:           rate.Limit(float64(requestsPerMinute) / 60),
		burst:           burst,
		retryAfter:      strconv.Itoa(retrySeconds),
		entryTTL:        defaultRateLimitEntryTTL,
		cleanupInterval: defaultRateLimitCleanupInterval,
		now:             now,
		entries:         make(map[string]*rateLimitEntry),
		lastCleanup:     now(),
	}
}

// ByIP rate-limits requests by the first valid X-Forwarded-For address,
// falling back to RemoteAddr.
func (l *RateLimiter) ByIP(next http.Handler) http.Handler {
	return l.middleware(clientIP, next)
}

// ByUser rate-limits authenticated requests by user ID. Requests without an
// authenticated principal pass through and should be protected by ByIP.
func (l *RateLimiter) ByUser(next http.Handler) http.Handler {
	return l.middleware(func(r *http.Request) string {
		principal, ok := authctx.From(r.Context())
		if !ok {
			return ""
		}
		return principal.UserID
	}, next)
}

func (l *RateLimiter) middleware(keyFn func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.enabled || exemptFromRateLimit(r) {
			next.ServeHTTP(w, r)
			return
		}

		key := keyFn(r)
		if key == "" || l.allow(key) {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Retry-After", l.retryAfter)
		respond.Error(w, http.StatusTooManyRequests, "rate limit exceeded")
	})
}

func (l *RateLimiter) allow(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastCleanup) >= l.cleanupInterval {
		l.cleanup(now)
	}

	entry, ok := l.entries[key]
	if !ok {
		entry = &rateLimitEntry{
			limiter: rate.NewLimiter(l.limit, l.burst),
		}
		l.entries[key] = entry
	}
	entry.lastSeen = now
	return entry.limiter.AllowN(now, 1)
}

func (l *RateLimiter) cleanup(now time.Time) {
	for key, entry := range l.entries {
		if now.Sub(entry.lastSeen) > l.entryTTL {
			delete(l.entries, key)
		}
	}
	l.lastCleanup = now
}

func exemptFromRateLimit(r *http.Request) bool {
	return r.Method == http.MethodOptions || r.URL.Path == "/health"
}

func clientIP(r *http.Request) string {
	for _, raw := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil {
			return ip.String()
		}
	}

	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
		return host
	}
	if ip := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); ip != nil {
		return ip.String()
	}
	return strings.TrimSpace(r.RemoteAddr)
}
