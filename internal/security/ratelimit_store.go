package security

// RateLimitStore is the interface the router depends on for rate
// limiting. *Store (the existing in-memory implementation in ratelimit.go)
// already satisfies this without any changes, since Go interfaces are
// structural. RedisRateLimitStore (redis_ratelimit.go) is the opt-in
// distributed alternative for #3 — memory stays the default, Redis is
// selected explicitly via RateLimiterConfig.Backend = "redis".
type RateLimitStore interface {
	Allow(key string) bool
	Stop()
}
