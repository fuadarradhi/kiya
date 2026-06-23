package kiya

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/sessions"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/fuadarradhi/kiya/internal/util"
	"github.com/fuadarradhi/kiya/internal/web"
)

func New(cfg Config) (*Router, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	host := cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", host, cfg.Server.Port)

	util.TrustProxyHeaders.Store(cfg.Server.TrustProxyHeaders)

	logPath := cfg.Log.Path
	if logPath == "" {
		logPath = "./temp/log"
	}
	wafLogPath := cfg.Log.WAFPath
	if wafLogPath == "" {
		wafLogPath = "./temp/waf"
	}
	logger.Init(logger.Config{
		Debug:           cfg.Debug,
		Token:           cfg.Telegram.Token,
		Group:           cfg.Telegram.Group,
		LogPath:         logPath,
		WAFPath:         wafLogPath,
		JSON:            cfg.Log.JSON,
		TelegramEnabled: cfg.Telegram.Enabled,
	})

	database, err := NewDatabase(cfg.Database)
	if err != nil {
		return nil, fmt.Errorf("initialize database: %w", err)
	}

	var sameSite http.SameSite
	switch strings.ToLower(cfg.Server.SameSite) {
	case "strict":
		sameSite = http.SameSiteStrictMode
	case "none":
		sameSite = http.SameSiteNoneMode
	default:
		sameSite = http.SameSiteLaxMode
	}

	r := &Router{
		addr:       addr,
		database:   database,
		debug:      cfg.Debug,
		forceHTTPS: cfg.Server.ForceHTTPS,
		renderer:   web.NewRenderer(cfg.View.FS),
		sameSite:   sameSite,

		csrfEnabled:     cfg.Server.CSRFEnabled,
		csrfExemptPaths: cfg.Server.CSRFExemptPaths,

		csp:            cfg.Security.CSP,
		cspExemptPaths: cfg.Security.CSPExemptPaths,
		wafExemptPaths: cfg.Security.WAFExemptPaths,

		corsConfig:         cfg.CORS,
		compressionEnabled: cfg.Compression.Enabled,
		requestIDEnabled:   true,
	}

	if cfg.Compression.Enabled {
		r.compressionLevel = cfg.Compression.Level
		if r.compressionLevel == 0 {
			r.compressionLevel = 5
		}
	}

	if cfg.Encryption.Key != "" {
		hash := sha256.Sum256([]byte(cfg.Encryption.Key))
		r.encryptKey = hash[:]
		logger.LogInfo("Encryption enabled (AES-256-GCM)")
	} else {
		logger.LogInfo("Encryption disabled (no key configured)")
	}

	if cfg.Server.SessionEnabled {
		sessionMaxAge := cfg.Server.SessionMaxAge
		if sessionMaxAge <= 0 {
			sessionMaxAge = 86400 * 7
		}
		r.sessionMaxAge = sessionMaxAge

		secureCookie := cfg.Server.ForceHTTPS || cfg.Server.SecureCookie

		switch cfg.Server.SessionStore.Type {
		case SessionStoreRedis:
			store, err := security.NewRedisStore(
				cfg.Server.SessionStore.Redis.Addr,
				cfg.Server.SessionStore.Redis.Password,
				cfg.Server.SessionStore.Redis.DB,
				[]byte(cfg.Server.SessionSecret),
				sessionMaxAge,
				sameSite,
				secureCookie,
			)
			if err != nil {
				return nil, fmt.Errorf("create redis session store: %w", err)
			}
			r.sessionStore = store
			logger.LogInfo("Session enabled (Redis store) | Addr: %s | DB: %d",
				cfg.Server.SessionStore.Redis.Addr, cfg.Server.SessionStore.Redis.DB)

		default:
			store := sessions.NewCookieStore([]byte(cfg.Server.SessionSecret))
			store.Options = &sessions.Options{
				Path:     "/",
				MaxAge:   sessionMaxAge,
				HttpOnly: true,
				Secure:   secureCookie,
				SameSite: sameSite,
			}
			r.sessionStore = store
			logger.LogInfo("Session enabled (Cookie store)")
		}
	} else {
		logger.LogInfo("Session disabled via config")
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
	r.cachePaths = cachePaths
	r.noLogSuccessPaths = noLogPaths

	maxWAFBuffer := cfg.Server.MaxWAFBufferSize
	if maxWAFBuffer <= 0 {
		maxWAFBuffer = 10 << 20
	}
	r.maxWAFBufferSize = maxWAFBuffer

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
		logger.LogInfo("Rate limiter disabled via config")
	}

	r.resPool = &sync.Pool{
		New: func() any {
			return &Resources{}
		},
	}

	r.errorHandler = r.defaultErrorHandler
	r.noRoute = r.defaultNoRoute
	r.noMethod = r.defaultNoMethod

	r.routeNames = make(map[string]string)
	r.tree = router.NewTree()

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

	if cfg.HealthCheck.Enabled {
		hcPath := cfg.HealthCheck.Path
		if hcPath == "" {
			hcPath = "/health"
		}
		r.healthCheckPath = hcPath
		r.Get(hcPath, func(c *Resources) error {
			status := "ok"
			checks := make(map[string]any)

			if r.database != nil {
				if err := r.database.Ping(); err != nil {
					status = "error"
					checks["database"] = err.Error()
				} else {
					checks["database"] = "ok"
				}
			}

			code := http.StatusOK
			if status != "ok" {
				code = http.StatusServiceUnavailable
			}

			return c.JSON(code, map[string]any{
				"status":    status,
				"checks":    checks,
				"timestamp": time.Now().Format(time.RFC3339),
			})
		})
		logger.LogInfo("Health check endpoint registered at %s", hcPath)
	}

	if r.csp != "" {
		logger.LogInfo("CSP header enabled")
	}
	if cfg.CORS.Enabled {
		logger.LogInfo("CORS enabled")
	}
	if r.compressionEnabled {
		logger.LogInfo("Compression enabled (gzip level %d)", r.compressionLevel)
	}
	if len(r.wafExemptPaths) > 0 {
		logger.LogInfo("WAF exempt paths: %v", r.wafExemptPaths)
	}

	return r, nil
}
