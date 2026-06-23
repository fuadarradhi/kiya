package kiya

import (
	"io/fs"
	"net/http"
	"time"
)

// DefaultConditionFunc defines the function signature for default query conditions.
type DefaultConditionFunc func(fields []string, res *Resources) map[string]any

// Config holds all configuration options for the Kiya framework.
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

// TelegramConfig holds Telegram bot notification settings.
type TelegramConfig struct {
	Token string
	Group string
}

// ServerConfig holds HTTP server settings.
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
	TrustProxyHeaders bool
	CSRFEnabled       bool
	CSRFExemptPaths   []string
	SameSite          string // "lax", "strict", "none", or "" for default (lax)
}

// SessionStoreConfig configures the session store backend.
type SessionStoreConfig struct {
	Type  string // "cookie" (default) or "redis"
	Redis RedisConfig
}

// RedisConfig holds Redis connection settings for session storage.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// DatabaseConfig holds database connection settings.
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

// ViewConfig holds template engine settings.
type ViewConfig struct {
	FS fs.FS
}

// RateLimiterConfig holds rate limiter settings.
type RateLimiterConfig struct {
	Enabled         bool
	Rate            float64
	Burst           int
	TTL             time.Duration
	CleanupInterval time.Duration
	KeyFunc         func(r *http.Request, sess *Session) string
}

// EncryptionConfig holds encryption key settings.
type EncryptionConfig struct {
	Key string
}

// LogConfig holds logging settings.
type LogConfig struct {
	Path    string // directory for log files (default: "./temp/log")
	WAFPath string // directory for WAF log files (default: "./temp/waf")
	JSON    bool   // output logs in JSON format for log aggregation
}

// SecurityConfig holds security header and WAF settings.
type SecurityConfig struct {
	CSP            string   // Content-Security-Policy header value (empty = disabled)
	CSPExemptPaths []string // paths that skip CSP header
	WAFExemptPaths []string // paths that skip WAF inspection (e.g., file upload endpoints)
}

// CORSConfig holds Cross-Origin Resource Sharing settings.
type CORSConfig struct {
	Enabled          bool
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// CompressionConfig holds response compression settings.
type CompressionConfig struct {
	Enabled bool
	Level   int // gzip compression level (1-9), default 5 if enabled and 0
}

// HealthCheckConfig holds health check endpoint settings.
type HealthCheckConfig struct {
	Enabled bool
	Path    string // default: "/health"
}
