package kiya

import (
	"net/http"
	"time"
)

type Option func(*config)

func WithDebug(debug bool) Option {
	return func(c *config) { c.Debug = debug }
}

func WithAddr(host string, port int) Option {
	return func(c *config) {
		c.Server.Host = host
		c.Server.Port = port
	}
}

func WithForceHTTPS() Option {
	return func(c *config) { c.Server.ForceHTTPS = true }
}

func WithSession(secret, storeType string) Option {
	return func(c *config) {
		c.Server.SessionEnabled = true
		c.Server.SessionSecret = secret
		c.Server.SessionStore.Type = storeType
	}
}

func WithSessionRedis(secret, addr, password string, db int) Option {
	return func(c *config) {
		c.Server.SessionEnabled = true
		c.Server.SessionSecret = secret
		c.Server.SessionStore.Type = SessionStoreRedis
		c.Server.SessionStore.Redis = RedisConfig{Addr: addr, Password: password, DB: db}
	}
}

func WithDatabase(driver, host, port, user, password, name string) Option {
	return func(c *config) {
		c.Database.Enabled = true
		c.Database.Driver = driver
		c.Database.Host = host
		c.Database.Port = port
		c.Database.User = user
		c.Database.Password = password
		c.Database.Name = name
	}
}

func WithRateLimiter(rate float64, burst int) Option {
	return func(c *config) {
		c.RateLimiter.Enabled = true
		c.RateLimiter.Backend = RateLimiterBackendMemory
		c.RateLimiter.Rate = rate
		c.RateLimiter.Burst = burst
	}
}

func WithRateLimiterRedis(addr, password string, db int, rate float64, burst int) Option {
	return func(c *config) {
		c.RateLimiter.Enabled = true
		c.RateLimiter.Backend = RateLimiterBackendRedis
		c.RateLimiter.Rate = rate
		c.RateLimiter.Burst = burst
		c.RateLimiter.Redis = RedisConfig{Addr: addr, Password: password, DB: db}
	}
}

func WithRateLimiterKeyFunc(fn func(r *http.Request, sess *Session) string) Option {
	return func(c *config) { c.RateLimiter.KeyFunc = fn }
}

func WithEncryption(key string) Option {
	return func(c *config) { c.Encryption.Key = key }
}

func WithCSP(csp string, exemptPaths ...string) Option {
	return func(c *config) {
		c.Security.CSP = csp
		c.Security.CSPExemptPaths = exemptPaths
	}
}

func WithCORS(allowOrigins ...string) Option {
	return func(c *config) {
		c.CORS.Enabled = true
		c.CORS.AllowOrigins = allowOrigins
	}
}

func WithCompression(level int) Option {
	return func(c *config) {
		c.Compression.Enabled = true
		c.Compression.Level = level
	}
}

func WithHealthCheck(path string) Option {
	return func(c *config) {
		c.HealthCheck.Enabled = true
		c.HealthCheck.Path = path
	}
}

func WithMetrics(path string) Option {
	return func(c *config) {
		c.Metrics.Enabled = true
		c.Metrics.Path = path
	}
}

func WithTelegramAlerts(token, group string) Option {
	return func(c *config) {
		c.Telegram.Enabled = true
		c.Telegram.Token = token
		c.Telegram.Group = group
	}
}

func WithTimeouts(read, write, idle time.Duration) Option {
	return func(c *config) {
		c.Server.ReadTimeout = read
		c.Server.WriteTimeout = write
		c.Server.IdleTimeout = idle
	}
}
