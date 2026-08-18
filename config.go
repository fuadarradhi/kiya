package kiya

import (
	"errors"
	"net/http"
	"time"
)

const (
	SessionStoreCookie = "cookie"
	SessionStoreRedis  = "redis"
)

const (
	RateLimiterBackendMemory = "memory"
	RateLimiterBackendRedis  = "redis"
)

type ScopeFunc func(fields []string, c *Context) map[string]any

type SessionResolverFunc func(r *http.Request) (name string, path string)

type config struct {
	Debug             bool
	Telegram          TelegramConfig
	Server            ServerConfig
	Database          DatabaseConfig
	RateLimiter       RateLimiterConfig
	Encryption        EncryptionConfig
	CachePaths        []string
	NoLogSuccessPaths []string
	Log               LogConfig
	Security          SecurityConfig
	CORS              CORSConfig
	Compression       CompressionConfig
	HealthCheck       HealthCheckConfig
	Metrics           MetricsConfig

	CurrentUserFunc func(*Context) (id any, name string)

	// RequestScopeFunc, if set, wraps every request that reaches a route handler in a database
	// transaction. fn runs after the session has been resolved but before the route handler, and
	// must return a statement (query+args) to execute as that transaction's first statement -
	// e.g. Postgres set_config() calls to establish a Row-Level-Security session context.
	// Return an empty query to skip wrapping for that request (public routes with no
	// session/tenant yet). If fn returns an error, the request is aborted with 500 before the
	// handler runs. The handler sees the wrapped transaction transparently through
	// Context.Database() - it commits when the handler returns a nil error, and rolls back on
	// error or panic. Kiya itself has no opinion on what the statement does; it is entirely
	// up to the caller (Postgres set_config, or nothing at all for apps that don't need this).
	RequestScopeFunc func(c *Context) (query string, args []any, err error)
}

func (c config) validate() error {
	if c.Server.SessionEnabled {
		if c.Server.SessionStore.Type == SessionStoreRedis {
			if c.Server.SessionStore.Redis.Addr == "" {
				return errors.New("redis address cannot be empty when using redis session store")
			}
		}
	}
	if c.Database.Enabled {
		if c.Database.Driver != "mysql" && c.Database.Driver != "postgres" {
			return errors.New("unsupported database driver, only 'mysql' or 'postgres' are available")
		}
		if c.Database.Host == "" || c.Database.Port == "" || c.Database.Name == "" || c.Database.User == "" {
			return errors.New("database host, port, name, and user are required when database is enabled")
		}
	}
	if c.RateLimiter.Enabled && c.RateLimiter.Backend == RateLimiterBackendRedis {
		if c.RateLimiter.Redis.Addr == "" {
			return errors.New("rate limiter redis address cannot be empty when RateLimiter.Backend is \"redis\"")
		}
	}
	return nil
}

type TelegramConfig struct {
	Enabled bool
	Token   string
	Group   string
}

type ServerConfig struct {
	Host              string
	Port              int
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	SessionSecret     string
	SessionEnabled    bool
	SessionMaxAge     int
	SessionStore      SessionStoreConfig
	SessionResolver   SessionResolverFunc
	BrowserCookieEnabled bool
	BrowserCookieName    string
	MaxWAFBufferSize  int64
	ForceHTTPS        bool
	SecureCookie      bool
	TrustProxyHeaders bool
	SameSite          string
}

type SessionStoreConfig struct {
	Type  string
	Redis RedisConfig
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

type DatabaseConfig struct {
	Enabled         bool
	Driver          string
	Host            string
	Port            string
	User            string
	Password        string
	Name            string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	Timezone        string
	Scope           ScopeFunc
}

type RateLimiterConfig struct {
	Enabled         bool
	Backend         string
	Rate            float64
	Burst           int
	TTL             time.Duration
	CleanupInterval time.Duration
	Redis           RedisConfig
	KeyFunc         func(r *http.Request, sess *Session) string
}

type EncryptionConfig struct {
	Key string
}

type LogConfig struct {
	Path    string
	WAFPath string
	JSON    bool
}

type SecurityConfig struct {
	CSP            string
	CSPExemptPaths []string
	WAFExemptPaths []string
}

type CORSConfig struct {
	Enabled          bool
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

type CompressionConfig struct {
	Enabled bool
	Level   int
}

type HealthCheckConfig struct {
	Enabled bool
	Path    string
}

type MetricsConfig struct {
	Enabled bool
	Path    string
}
