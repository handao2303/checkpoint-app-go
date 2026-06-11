package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisRateLimiterSharesQuota(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 2}
	store := NewRedisRateLimitStore(client, cfg)
	first := NewRateLimiter(cfg, WithRateLimitStore(store, "checkpoint:rate-limit:ip:"))
	second := NewRateLimiter(cfg, WithRateLimitStore(store, "checkpoint:rate-limit:ip:"))

	assertStatus(t, first.ByIP(okHandler()), request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	assertStatus(t, second.ByIP(okHandler()), request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	assertStatus(t, first.ByIP(okHandler()), request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusTooManyRequests)
}

func TestRedisRateLimiterUsesSeparateNamespaces(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1}
	store := NewRedisRateLimitStore(client, cfg)
	ipLimiter := NewRateLimiter(cfg, WithRateLimitStore(store, "checkpoint:rate-limit:ip:"))
	userLimiter := NewRateLimiter(cfg, WithRateLimitStore(store, "checkpoint:rate-limit:user:"))

	assertStatus(t, ipLimiter.ByIP(okHandler()), request(http.MethodGet, "/", "198.51.100.10:1234", ""), http.StatusOK)
	assertStatus(t, userLimiter.ByUser(okHandler()), userRequest("198.51.100.10"), http.StatusOK)

	if !server.Exists("checkpoint:rate-limit:ip:198.51.100.10") {
		t.Fatal("IP rate-limit key was not created")
	}
	if !server.Exists("checkpoint:rate-limit:user:198.51.100.10") {
		t.Fatal("user rate-limit key was not created")
	}
}

func TestRedisRateLimiterRefillsAndSetsTTL(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	now := time.Date(2026, time.June, 11, 0, 0, 0, 0, time.UTC)
	cfg := RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1}
	store := NewRedisRateLimitStore(client, cfg)
	store.now = func() time.Time { return now }

	key := "checkpoint:rate-limit:ip:198.51.100.10"
	allowed, err := store.Allow(context.Background(), key)
	if err != nil || !allowed {
		t.Fatalf("first Allow() = %v, %v; want true, nil", allowed, err)
	}
	allowed, err = store.Allow(context.Background(), key)
	if err != nil || allowed {
		t.Fatalf("second Allow() = %v, %v; want false, nil", allowed, err)
	}

	now = now.Add(time.Second)
	allowed, err = store.Allow(context.Background(), key)
	if err != nil || !allowed {
		t.Fatalf("refilled Allow() = %v, %v; want true, nil", allowed, err)
	}
	if ttl := server.TTL(key); ttl <= time.Minute {
		t.Fatalf("TTL = %v, want more than one minute", ttl)
	}
}

func TestRedisRateLimiterFallsBackAndRecovers(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:          server.Addr(),
		DialTimeout:   10 * time.Millisecond,
		ReadTimeout:   10 * time.Millisecond,
		WriteTimeout:  10 * time.Millisecond,
		DialerRetries: 1,
		MaxRetries:    -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	cfg := RateLimitConfig{Enabled: true, RequestsPerMinute: 60, Burst: 1}
	store := NewRedisRateLimitStore(client, cfg)
	limiter := NewRateLimiter(cfg, WithRateLimitStore(store, "checkpoint:rate-limit:ip:"))
	handler := limiter.ByIP(okHandler())
	req := func() *http.Request {
		return request(http.MethodGet, "/", "198.51.100.10:1234", "")
	}

	server.Close()
	assertStatus(t, handler, req(), http.StatusOK)
	assertStatus(t, handler, req(), http.StatusTooManyRequests)

	if err := server.Restart(); err != nil {
		t.Fatalf("restart miniredis: %v", err)
	}
	assertStatus(t, handler, req(), http.StatusOK)
	assertStatus(t, handler, req(), http.StatusTooManyRequests)
}

func TestNewRedisClient(t *testing.T) {
	t.Parallel()

	client, err := NewRedisClient("")
	if err != nil || client != nil {
		t.Fatalf("empty URL = %v, %v; want nil, nil", client, err)
	}

	if _, err := NewRedisClient("not-a-redis-url"); err == nil {
		t.Fatal("malformed URL should return an error")
	}

	client, err = NewRedisClient("rediss://default:password@example.com:6380/0")
	if err != nil {
		t.Fatalf("valid URL: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	opts := client.Options()
	if opts.PoolSize != 4 {
		t.Fatalf("PoolSize = %d, want 4", opts.PoolSize)
	}
	if opts.MaxActiveConns != 4 {
		t.Fatalf("MaxActiveConns = %d, want 4", opts.MaxActiveConns)
	}
	if opts.DialerRetries != 1 {
		t.Fatalf("DialerRetries = %d, want 1", opts.DialerRetries)
	}
	if opts.MaxRetries != 0 {
		t.Fatalf("MaxRetries = %d, want normalized no-retry value 0", opts.MaxRetries)
	}
	if opts.DialTimeout != 100*time.Millisecond || opts.ReadTimeout != 100*time.Millisecond || opts.WriteTimeout != 100*time.Millisecond {
		t.Fatalf("timeouts = %v/%v/%v, want 100ms", opts.DialTimeout, opts.ReadTimeout, opts.WriteTimeout)
	}
	if opts.PoolTimeout != 100*time.Millisecond {
		t.Fatalf("PoolTimeout = %v, want 100ms", opts.PoolTimeout)
	}
}
