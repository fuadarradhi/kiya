package kiya

import (
	"io/fs"
	"net/http"
	"time"
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
}

type TelegramConfig struct {
	Token string
	Group string
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
	MaxWAFBufferSize  int64
	ForceHTTPS        bool
	TrustProxyHeaders bool
	CSRFEnabled       bool
	CSRFExemptPaths   []string
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
