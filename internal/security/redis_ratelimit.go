package security

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/fuadarradhi/kiya/internal/logger"
)

// tokenBucketScript implements the same token-bucket algorithm as the
// in-memory Store (ratelimit.go), but atomically inside Redis so multiple
// app instances share one limit instead of each enforcing its own.
//
// KEYS[1] = bucket key
// ARGV[1] = rate (tokens refilled per second)
// ARGV[2] = burst (max tokens / bucket capacity)
// ARGV[3] = now (unix time, seconds, as a float)
var tokenBucketScript = redis.NewScript(`
local tokens_key = KEYS[1] .. ":tokens"
local ts_key = KEYS[1] .. ":ts"

local rate = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

local last_tokens = tonumber(redis.call("GET", tokens_key))
if last_tokens == nil then
    last_tokens = burst
end

local last_refreshed = tonumber(redis.call("GET", ts_key))
if last_refreshed == nil then
    last_refreshed = 0
end

local delta = now - last_refreshed
if delta < 0 then
    delta = 0
end

local filled = math.min(burst, last_tokens + (delta * rate))
local allowed = 0
if filled >= 1 then
    allowed = 1
    filled = filled - 1
end

local ttl = math.floor(burst / rate) + 2
if ttl < 1 then
    ttl = 1
end

redis.call("SETEX", tokens_key, ttl, tostring(filled))
redis.call("SETEX", ts_key, ttl, tostring(now))

return allowed
`)

// RedisRateLimitStore is the opt-in distributed alternative to the
// in-memory Store. Selected via RateLimiterConfig.Backend = "redis".
type RedisRateLimitStore struct {
	client *redis.Client
	rate   float64
	burst  float64
	prefix string
}

func NewRedisRateLimitStore(addr, password string, db int, rate float64, burst int) (*RedisRateLimitStore, error) {
	if rate <= 0 {
		rate = 10
	}
	if burst <= 0 {
		burst = 20
	}

	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("kiya: redis rate limiter ping failed: %w", err)
	}

	return &RedisRateLimitStore{
		client: client,
		rate:   rate,
		burst:  float64(burst),
		prefix: "ratelimit_",
	}, nil
}

func (s *RedisRateLimitStore) Allow(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	now := float64(time.Now().UnixNano()) / 1e9

	res, err := tokenBucketScript.Run(ctx, s.client, []string{s.prefix + key}, s.rate, s.burst, now).Int()
	if err != nil {
		// Fail OPEN rather than blocking all traffic if Redis is briefly
		// unavailable — a rate limiter outage should degrade to "no
		// limiting", not "everyone gets 429". Logged so it's visible.
		logger.LogWarn("[RateLimit] Redis error, allowing request through: %v", err)
		return true
	}

	return res == 1
}

func (s *RedisRateLimitStore) Stop() {
	_ = s.client.Close()
}
