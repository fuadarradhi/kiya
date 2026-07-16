package security

import "context"

// Ping checks connectivity to the Redis session store. Used by the
// health check endpoint (#6) when SessionStoreConfig.Type == "redis", so
// a Redis outage shows up as a degraded health check instead of only
// surfacing when a user's session lookup fails mid-request.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}
