package middleware

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const rateLimitKeyTTLBuffer = time.Minute

const (
	redisPoolSize = 4
	redisTimeout  = 100 * time.Millisecond
)

var redisTokenBucketScript = redis.NewScript(`
local values = redis.call("HMGET", KEYS[1], "tokens", "updated_at")
local tokens = tonumber(values[1])
local updated_at = tonumber(values[2])
local now = tonumber(ARGV[1])
local refill_per_ms = tonumber(ARGV[2])
local burst = tonumber(ARGV[3])
local ttl_ms = tonumber(ARGV[4])

if tokens == nil then
	tokens = burst
	updated_at = now
else
	local elapsed = math.max(0, now - updated_at)
	tokens = math.min(burst, tokens + elapsed * refill_per_ms)
end

local allowed = 0
if tokens >= 1 then
	tokens = tokens - 1
	allowed = 1
end

redis.call("HSET", KEYS[1], "tokens", tokens, "updated_at", now)
redis.call("PEXPIRE", KEYS[1], ttl_ms)
return allowed
`)

// RedisRateLimitStore implements a shared atomic token bucket.
type RedisRateLimitStore struct {
	client      redis.Scripter
	refillPerMS float64
	burst       int
	ttl         time.Duration
	now         func() time.Time
}

// NewRedisClient parses a Redis connection URL and configures a small,
// latency-bounded pool suitable for Cloud Run. An empty URL disables Redis.
func NewRedisClient(rawURL string) (*redis.Client, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") {
		return nil, fmt.Errorf("REDIS_URL must be a valid redis:// or rediss:// URL")
	}

	opts, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("REDIS_URL must be a valid redis:// or rediss:// URL")
	}
	opts.PoolSize = redisPoolSize
	opts.MaxActiveConns = redisPoolSize
	opts.DialerRetries = 1
	opts.MaxRetries = -1
	opts.DialTimeout = redisTimeout
	opts.ReadTimeout = redisTimeout
	opts.WriteTimeout = redisTimeout
	opts.PoolTimeout = redisTimeout

	return redis.NewClient(opts), nil
}

// NewRedisRateLimitStore creates a shared token bucket store.
func NewRedisRateLimitStore(client redis.Scripter, cfg RateLimitConfig) *RedisRateLimitStore {
	requestsPerMinute := max(cfg.RequestsPerMinute, 1)
	burst := max(cfg.Burst, 1)
	refillPerSecond := float64(requestsPerMinute) / 60
	fullRefill := time.Duration(math.Ceil(float64(burst)/refillPerSecond)) * time.Second

	return &RedisRateLimitStore{
		client:      client,
		refillPerMS: refillPerSecond / 1000,
		burst:       burst,
		ttl:         fullRefill + rateLimitKeyTTLBuffer,
		now:         time.Now,
	}
}

// Allow atomically checks and consumes one token.
func (s *RedisRateLimitStore) Allow(ctx context.Context, key string) (bool, error) {
	result, err := redisTokenBucketScript.Run(
		ctx,
		s.client,
		[]string{key},
		s.now().UnixMilli(),
		s.refillPerMS,
		s.burst,
		s.ttl.Milliseconds(),
	).Int()
	if err != nil {
		return false, fmt.Errorf("redis token bucket: %w", err)
	}
	return result == 1, nil
}
