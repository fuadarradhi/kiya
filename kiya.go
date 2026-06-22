package kiya

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/sessions"

	khttp "github.com/fuadarradhi/kiya/internal/http"
	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/fuadarradhi/kiya/internal/util"
)

// New creates a new Router instance with the provided configuration.
func New(cfg Config) *Router {
	host := cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", host, cfg.Server.Port)

	util.TrustProxyHeaders.Store(cfg.Server.TrustProxyHeaders)
	logger.Init(cfg.Debug, cfg.Telegram.Token, cfg.Telegram.Group)

	database, err := NewDatabase(cfg.Database)
	if err != nil {
		logger.LogError("CRITICAL: Failed to initialize Database: %v", err)
		panic(err)
	}

	r := &Router{
		addr:       addr,
		database:   database,
		debug:      cfg.Debug,
		forceHTTPS: cfg.Server.ForceHTTPS,
		renderer:   khttp.NewRenderer(cfg.View.FS),
		sameSite:   http.SameSiteLaxMode,

		csrfEnabled:     cfg.Server.CSRFEnabled,
		csrfExemptPaths: cfg.Server.CSRFExemptPaths,
	}

	if cfg.Encryption.Key != "" {
		hash := sha256.Sum256([]byte(cfg.Encryption.Key))
		r.encryptKey = hash[:]
		logger.LogInfo("Encryption enabled (AES-256-GCM)")
	} else {
		logger.LogInfo("Encryption disabled (no key configured)")
	}

	var store *sessions.CookieStore
	if cfg.Server.SessionEnabled {
		if cfg.Server.SessionSecret == "" {
			logger.LogError("CRITICAL: SESSION SECRET cannot be empty when SessionEnabled is true.")
			panic("SESSION SECRET cannot be empty")
		}
		store = sessions.NewCookieStore([]byte(cfg.Server.SessionSecret))

		sessionMaxAge := cfg.Server.SessionMaxAge
		if sessionMaxAge <= 0 {
			sessionMaxAge = 86400 * 7
		}
		r.sessionMaxAge = sessionMaxAge

		store.Options = &sessions.Options{
			Path:     "/",
			MaxAge:   r.sessionMaxAge,
			HttpOnly: true,
			Secure:   true,
			SameSite: r.sameSite,
		}
		r.sessionStore = store
	} else {
		logger.LogInfo("Session Disabled via config")
	}

	wafInstance, err := router.InitWAF(cfg.Debug)
	if err != nil {
		logger.LogWarn("Failed to initialize WAF: %v. Server running WITHOUT WAF protection.", err)
	}
	r.waf = wafInstance

	readTimeout := cfg.Server.ReadTimeout
	if readTimeout == 0 {
		readTimeout = 30 * time.Second
	}
	writeTimeout := cfg.Server.WriteTimeout
	if writeTimeout == 0 {
		writeTimeout = 30 * time.Second
	}
	idleTimeout := cfg.Server.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = 120 * time.Second
	}
	readHeaderTimeout := cfg.Server.ReadHeaderTimeout
	if readHeaderTimeout == 0 {
		readHeaderTimeout = 10 * time.Second
	}

	cachePaths := cfg.CachePaths
	if len(cachePaths) == 0 {
		cachePaths = []string{"/assets"}
	}
	noLogPaths := cfg.NoLogSuccessPaths
	if len(noLogPaths) == 0 {
		noLogPaths = []string{"/assets"}
	}
	maxWAFBuffer := cfg.Server.MaxWAFBufferSize
	if maxWAFBuffer <= 0 {
		maxWAFBuffer = 10 << 20
	}

	r.maxWAFBufferSize = maxWAFBuffer
	r.cachePaths = cachePaths
	r.noLogSuccessPaths = noLogPaths

	if cfg.RateLimiter.Enabled {
		rate := cfg.RateLimiter.Rate
		if rate <= 0 {
			rate = 10
		}
		burst := cfg.RateLimiter.Burst
		if burst <= 0 {
			burst = 20
		}
		ttl := cfg.RateLimiter.TTL
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		cleanupInterval := cfg.RateLimiter.CleanupInterval

		r.rateLimiter = security.NewStore(rate, burst, ttl, cleanupInterval)

		if cfg.RateLimiter.KeyFunc != nil {
			r.keyFunc = func(req *http.Request, sess *Session) string {
				return cfg.RateLimiter.KeyFunc(req, sess)
			}
		} else {
			r.keyFunc = func(req *http.Request, sess *Session) string {
				if sess != nil && sess.ID() != "" {
					return "sess:" + sess.ID()
				}
				return "ip:" + util.RealIP(req)
			}
		}
	} else {
		logger.LogInfo("Rate Limiter Disabled via config")
	}

	r.resPool = &sync.Pool{
		New: func() any {
			return &Resources{}
		},
	}

	r.errorHandler = r.defaultErrorHandler
	r.noRoute = r.defaultNoRoute
	r.noMethod = r.defaultNoMethod

	r.server = &http.Server{
		Addr:              addr,
		ReadTimeout:       readTimeout,
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    1 << 20,
	}

	if r.csrfEnabled {
		logger.LogInfo("CSRF protection enabled (encrypt-time session-bound, 2h validity)")
	} else {
		logger.LogInfo("CSRF protection disabled")
	}

	// Initialize router tree
	r.tree = router.NewTree(nil)

	return r
}
