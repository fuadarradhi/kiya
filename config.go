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
	MaxWAFBufferSize  int64
	ForceHTTPS        bool
	TrustProxyHeaders bool
	CSRFEnabled       bool
	CSRFExemptPaths   []string
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
