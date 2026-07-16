package security

type RateLimitStore interface {
	Allow(key string) bool
	Stop()
}
