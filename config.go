package kiya

import (
	"errors"
	"io/fs"
	"net/http"
	"time"
)

const (
	SessionStoreCookie = "cookie"
	SessionStoreRedis  = "redis"
)

type DefaultConditionFunc func(fields []string, res *Resources) map[string]any

type Config struct {
	Debug             bool
	Telegram          TelegramConfig
	Server            ServerConfig
	Database          DatabaseConfig
	View              ViewConfig
	RateLimiter       RateLimiterConfig
	Encryption        EncryptionConfig
	CachePaths        []string
	NoLogSuccessPaths []string
	Log               LogConfig
	Security          SecurityConfig
	CORS              CORSConfig
	Compression       CompressionConfig
	HealthCheck       HealthCheckConfig
}

func (c Config) Validate() error {
	if c.Server.SessionEnabled {
		if c.Server.SessionSecret == "" {
			return errors.New("session secret cannot be empty when sessions are enabled")
		}
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
	MaxWAFBufferSize  int64
	ForceHTTPS        bool
	SecureCookie      bool
	TrustProxyHeaders bool
	CSRFEnabled       bool
	CSRFExemptPaths   []string
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
	Enabled          bool
	Driver           string
	Host             string
	Port             string
	User             string
	Password         string
	Name             string
	MaxOpenConns     int
	MaxIdleConns     int
	ConnMaxLifetime  time.Duration
	Timezone         string
	DefaultCondition DefaultConditionFunc
}

type ViewConfig struct {
	FS fs.FS
}

type RateLimiterConfig struct {
	Enabled         bool
	Rate            float64
	Burst           int
	TTL             time.Duration
	CleanupInterval time.Duration
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
