package kiya

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/corazawaf/coraza/v3"
	"github.com/corazawaf/coraza/v3/types"
	"github.com/fuadarradhi/kiya/owasp"
	"github.com/gorilla/sessions"
)

type HandlerFunc func(res *Resources) error
type Middleware func(HandlerFunc) HandlerFunc
type GroupFunc func(r *Router)

const defaultMaxWAFBufferSize int64 = 10 << 20

type Router struct {
	trees        map[string]*node
	middleware   []Middleware
	errorHandler func(*Resources, int, string, error)
	noRoute      HandlerFunc
	noMethod     HandlerFunc
	prefix       string
	mu           sync.RWMutex
	resPool      *sync.Pool
	server       *http.Server
	addr         string
	waf          coraza.WAF
	db           *DB
	sessionStore *sessions.CookieStore
	renderer     *Renderer

	rateLimiter      *rateLimitStore
	keyFunc          func(r *http.Request, sess *Session) string
	forceHTTPS       bool
	maxWAFBufferSize int64
	sessionMaxAge    int
	debug            bool
	sameSite         http.SameSite
	encryptKey       []byte

	cachePaths        []string
	noLogSuccessPaths []string

	csrfEnabled     bool
	csrfExemptPaths []string
}

var globalTrustProxyHeaders atomic.Bool

type HTTPError struct {
	Code    int
	Message string
	Err     error
}

func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.Code)
}

func (e *HTTPError) Unwrap() error {
	return e.Err
}

type nodeType uint8

const (
	static nodeType = iota
	paramNode
	regexNode
	wildcardNode
)

type node struct {
	part      string
	nType     nodeType
	paramName string
	regex     *regexp.Regexp
	children  []*node
	handler   HandlerFunc
}

type wafResponseWriter struct {
	http.ResponseWriter
	status              int
	body                bytes.Buffer
	wrote               bool
	streaming           bool
	maxBufferSize       int64
	bufferLimitExceeded bool
	tx                  types.Transaction
	blocked             bool
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rec.ResponseWriter.(http.Hijacker); ok {
		return hj.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (rec *statusRecorder) Flush() {
	if fl, ok := rec.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

func (w *wafResponseWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}

	isBlocked := false
	if w.tx != nil {
		for k, v := range w.Header() {
			lowerKey := strings.ToLower(k)
			for _, vv := range v {
				w.tx.AddResponseHeader(lowerKey, vv)
			}
		}
		w.tx.ProcessResponseHeaders(code, "HTTP/1.1")

		if it := w.tx.Interruption(); it != nil {
			LogWAF("BLOCK Response Header Phase - RuleID: %v", it.RuleID)
			isBlocked = true
			code = it.Status
			w.body.Reset()
			w.body.WriteString("Blocked by WAF")
			w.Header().Set("Content-Type", "text/plain")
		}
	}

	ct := w.Header().Get("Content-Type")

	isBinaryContent := strings.HasPrefix(ct, "image/") ||
		strings.HasPrefix(ct, "video/") ||
		strings.HasPrefix(ct, "audio/") ||
		ct == "application/octet-stream" ||
		ct == "application/pdf" ||
		strings.HasPrefix(ct, "application/font-") ||
		strings.HasPrefix(ct, "font/")

	if isBinaryContent && !isBlocked {
		w.ResponseWriter.WriteHeader(code)
		w.wrote = true
		w.streaming = true
	} else {
		w.status = code
		w.wrote = true
	}

	if isBlocked {
		w.blocked = true
	}
}

func (w *wafResponseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}

	if w.blocked {
		return len(b), nil
	}

	if w.streaming {
		return w.ResponseWriter.Write(b)
	}

	if w.maxBufferSize > 0 && (int64(w.body.Len())+int64(len(b))) > w.maxBufferSize {
		w.bufferLimitExceeded = true
		w.streaming = true
		w.FlushToClient()
		return w.ResponseWriter.Write(b)
	}

	return w.body.Write(b)
}

func (w *wafResponseWriter) FlushToClient() error {
	if w.streaming {
		return nil
	}

	if w.body.Len() > 0 || w.wrote {
		if !w.wrote {
			w.WriteHeader(http.StatusOK)
		}
		w.ResponseWriter.WriteHeader(w.status)
		_, err := w.ResponseWriter.Write(w.body.Bytes())
		return err
	}
	return nil
}

func (w *wafResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w.streaming || w.wrote {
		return nil, nil, fmt.Errorf("cannot hijack after response has been written")
	}

	if hj, ok := w.ResponseWriter.(http.Hijacker); ok {
		w.streaming = true
		return hj.Hijack()
	}

	return nil, nil, fmt.Errorf("underlying ResponseWriter does not implement http.Hijacker")
}

func (w *wafResponseWriter) Flush() {
	if fl, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wrote {
			w.WriteHeader(w.status)
		}
		if w.streaming {
			fl.Flush()
			return
		}
		w.ResponseWriter.WriteHeader(w.status)
		if w.body.Len() > 0 {
			w.ResponseWriter.Write(w.body.Bytes())
			w.body.Reset()
		}
		w.streaming = true
		fl.Flush()
	}
}

func initWAF(debug bool) (coraza.WAF, error) {
	engineMode := "On"
	if debug {
		engineMode = "DetectionOnly"
	}

	cfg := coraza.NewWAFConfig().
		WithErrorCallback(func(rule types.MatchedRule) {
			r := rule.Rule()

			var matchDetails []string
			for _, md := range rule.MatchedDatas() {
				matchDetails = append(matchDetails, fmt.Sprintf(
					"%v:%s=%s", md.Variable(), md.Key(), md.Value(),
				))
			}

			LogWAF(
				"Matched Rule ID: %d | File: %s | Line: %d | Severity: %s | Matched: [%s] | Raw: %s",
				r.ID(),
				r.File(),
				r.Line(),
				r.Severity().String(),
				strings.Join(matchDetails, "; "),
				r.Raw(),
			)
		})

	directives := fmt.Sprintf(`
        SecRuleEngine %s
        Include crs-setup.conf
        Include rules/*.conf
        Include ignore.conf
    `, engineMode)

	cfg = cfg.WithRootFS(owasp.RulesFS)
	cfg = cfg.WithDirectives(directives)

	wafInstance, err := coraza.NewWAF(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to init WAF: %w", err)
	}

	modeStr := "PROTECTION"
	if debug {
		modeStr = "DETECTION ONLY"
	}
	LogInfo("WAF Initialized successfully (Mode: %s)", modeStr)
	return wafInstance, nil
}

func New(cfg Config) *Router {
	host := cfg.Server.Host
	if host == "" {
		host = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", host, cfg.Server.Port)

	globalTrustProxyHeaders.Store(cfg.Server.TrustProxyHeaders)

	InitLogger(cfg.Debug, cfg.Telegram.Token, cfg.Telegram.Group)

	db, err := NewDatabase(cfg.Database)
	if err != nil {
		LogError("CRITICAL: Failed to initialize Database: %v", err)
		panic(err)
	}

	r := &Router{
		trees:      make(map[string]*node),
		addr:       addr,
		middleware: make([]Middleware, 0),
		db:         db,
		debug:      cfg.Debug,
		forceHTTPS: cfg.Server.ForceHTTPS,
		renderer:   NewRenderer(cfg.View.FS),
		sameSite:   http.SameSiteLaxMode,

		csrfEnabled:     cfg.Server.CSRFEnabled,
		csrfExemptPaths: cfg.Server.CSRFExemptPaths,
	}

	if cfg.Encryption.Key != "" {
		hash := sha256.Sum256([]byte(cfg.Encryption.Key))
		r.encryptKey = hash[:]
		LogInfo("Encryption enabled (AES-256-GCM)")
	} else {
		LogInfo("Encryption disabled (no key configured)")
	}

	var store *sessions.CookieStore
	if cfg.Server.SessionEnabled {
		if cfg.Server.SessionSecret == "" {
			LogError("CRITICAL: SESSION SECRET cannot be empty when SessionEnabled is true.")
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
		LogInfo("Session Disabled via config")
	}

	wafInstance, err := initWAF(cfg.Debug)
	if err != nil {
		LogWarn("Failed to initialize WAF: %v. Server running WITHOUT WAF protection.", err)
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
		maxWAFBuffer = defaultMaxWAFBufferSize
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

		r.rateLimiter = newRateLimitStore(rate, burst, ttl, cleanupInterval)

		if cfg.RateLimiter.KeyFunc != nil {
			r.keyFunc = cfg.RateLimiter.KeyFunc
		} else {
			r.keyFunc = func(req *http.Request, sess *Session) string {
				if sess != nil && sess.ID() != "" {
					return "sess:" + sess.ID()
				}
				return "ip:" + realIP(req)
			}
		}
	} else {
		LogInfo("Rate Limiter Disabled via config")
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
		LogInfo("CSRF protection enabled (encrypt-time session-bound, 2h validity)")
	} else {
		LogInfo("CSRF protection disabled")
	}

	return r
}

func (r *Router) shouldCache(path string) bool {
	for _, p := range r.cachePaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (r *Router) shouldSkipLog(path string) bool {
	for _, p := range r.noLogSuccessPaths {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func (r *Router) SetErrorHandler(fn func(*Resources, int, string, error)) { r.errorHandler = fn }
func (r *Router) SetNoRoute(h HandlerFunc)                                { r.noRoute = h }
func (r *Router) SetNoMethod(h HandlerFunc)                               { r.noMethod = h }

func (r *Router) Use(m ...Middleware) {
	r.middleware = append(r.middleware, m...)
}

func (r *Router) Route(prefix string, fn GroupFunc) {
	sub := &Router{
		trees:             r.trees,
		middleware:        append([]Middleware{}, r.middleware...),
		errorHandler:      r.errorHandler,
		prefix:            r.prefix + prefix,
		resPool:           r.resPool,
		waf:               r.waf,
		sessionStore:      r.sessionStore,
		db:                r.db,
		renderer:          r.renderer,
		rateLimiter:       r.rateLimiter,
		keyFunc:           r.keyFunc,
		forceHTTPS:        r.forceHTTPS,
		debug:             r.debug,
		maxWAFBufferSize:  r.maxWAFBufferSize,
		sessionMaxAge:     r.sessionMaxAge,
		encryptKey:        r.encryptKey,
		cachePaths:        r.cachePaths,
		noLogSuccessPaths: r.noLogSuccessPaths,
		csrfEnabled:       r.csrfEnabled,
		csrfExemptPaths:   r.csrfExemptPaths,
	}
	fn(sub)
}

func (r *Router) Get(path string, h HandlerFunc)     { r.addRoute(http.MethodGet, path, h) }
func (r *Router) Post(path string, h HandlerFunc)    { r.addRoute(http.MethodPost, path, h) }
func (r *Router) Put(path string, h HandlerFunc)     { r.addRoute(http.MethodPut, path, h) }
func (r *Router) Delete(path string, h HandlerFunc)  { r.addRoute(http.MethodDelete, path, h) }
func (r *Router) Patch(path string, h HandlerFunc)   { r.addRoute(http.MethodPatch, path, h) }
func (r *Router) Options(path string, h HandlerFunc) { r.addRoute(http.MethodOptions, path, h) }
func (r *Router) Head(path string, h HandlerFunc)    { r.addRoute(http.MethodHead, path, h) }

func (r *Router) Static(prefix, root string) error {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimSuffix(prefix, "/")

	sub, err := fs.Sub(os.DirFS(root), ".")
	if err != nil {
		return fmt.Errorf("failed to create static fs: %w", err)
	}
	return r.StaticFS(prefix, sub)
}

func (r *Router) StaticFS(prefix string, fsys fs.FS) error {
	r.Get(prefix+"/{path:*}", func(c *Resources) error {
		p := c.Param("path")
		return serveStatic(c.Response, c.Request, fsys, p)
	})
	return nil
}

func (r *Router) Redirect(path, target string, code int) {
	r.Get(path, func(c *Resources) error {
		http.Redirect(c.Response, c.Request, target, code)
		return nil
	})
}

func (r *Router) createRootHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				LogError("PANIC recovered (Global): %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}()

		if r.waf != nil {
			func() {
				wafWriter := &wafResponseWriter{
					ResponseWriter: w,
					status:         http.StatusOK,
					maxBufferSize:  r.maxWAFBufferSize,
				}

				defer func() {
					if err := recover(); err != nil {
						LogError("PANIC recovered (Inside Request Context): %v", err)

						wafWriter.wrote = true
						wafWriter.status = http.StatusInternalServerError
						wafWriter.body.Reset()
						wafWriter.body.WriteString("Internal Server Error")
						wafWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
						wafWriter.FlushToClient()
					}
				}()

				tx := r.waf.NewTransaction()
				defer tx.Close()

				wafWriter.tx = tx

				serverAddr := r.addr
				if addr := req.Context().Value(http.LocalAddrContextKey); addr != nil {
					if tcpAddr, ok := addr.(net.Addr); ok {
						serverAddr = tcpAddr.String()
					}
				}
				serverIP, serverPort := parseIPPort(serverAddr, 80)

				clientIP := realIP(req)
				clientPort := 0

				tx.ProcessConnection(serverIP, serverPort, clientIP, clientPort)
				tx.ProcessURI(req.RequestURI, req.Method, req.Proto)

				if req.Host != "" {
					tx.AddRequestHeader("host", req.Host)
				}

				for k, v := range req.Header {
					lowerKey := strings.ToLower(k)
					for _, vv := range v {
						tx.AddRequestHeader(lowerKey, vv)
					}
				}

				tx.ProcessRequestHeaders()

				if it := tx.Interruption(); it != nil {
					LogWAF("BLOCK Request Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
					w.WriteHeader(it.Status)
					w.Write([]byte("Blocked by WAF"))
					return
				}

				var bodyBytes []byte

				if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch {
					req.Body = http.MaxBytesReader(w, req.Body, r.maxWAFBufferSize)
					var err error
					bodyBytes, err = io.ReadAll(req.Body)

					req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

					if err != nil {
						if strings.Contains(err.Error(), "http: request body too large") {
							w.WriteHeader(http.StatusRequestEntityTooLarge)
							return
						}
						LogError("[WAF] Body read error: %v", err)
						bodyBytes = []byte{}
					}
				}

				tx.WriteRequestBody(bodyBytes)
				tx.ProcessRequestBody()

				if it := tx.Interruption(); it != nil {
					LogWAF("BLOCK Body Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
					w.WriteHeader(it.Status)
					w.Write([]byte("Blocked by WAF"))
					return
				}

				r.ServeHTTP(wafWriter, req)

				if wafWriter.bufferLimitExceeded {
					LogWarn("WAF: Response body exceeded buffer limit (%d bytes), skipping response body inspection", r.maxWAFBufferSize)
					return
				}

				if !wafWriter.streaming && wafWriter.body.Len() > 0 {
					tx.WriteResponseBody(wafWriter.body.Bytes())
				} else {
					tx.WriteResponseBody([]byte{})
				}

				tx.ProcessResponseBody()

				if it := wafWriter.tx.Interruption(); it != nil {
					LogWAF("BLOCK Response Body Phase - IP: %s | RuleID: %v", clientIP, it.RuleID)
					if !wafWriter.streaming {
						wafWriter.body.Reset()
						wafWriter.status = it.Status
						wafWriter.body.WriteString("Blocked by WAF (Response Body)")
						wafWriter.Header().Set("Content-Type", "text/plain")
						wafWriter.blocked = true
					} else {
						LogWarn("WARNING: WAF attempted to block a streaming response due to body content. Data might have been sent.")
					}
				}

				if err := wafWriter.FlushToClient(); err != nil {
					LogError("Error flushing response: %v", err)
				}
			}()
		} else {
			r.ServeHTTP(w, req)
		}
	})
}

func (r *Router) Start() error {
	r.server.Handler = r.createRootHandler()

	listenErr := make(chan error, 1)
	go func() {
		defer func() {
			if err := recover(); err != nil {
				LogError("FATAL: Server panic: %v", err)
				listenErr <- fmt.Errorf("panic: %v", err)
			}
		}()
		LogInfo("Server listening on %s", r.addr)
		err := r.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			LogError("Server failed to listen: %v", err)
			listenErr <- err
		} else {
			listenErr <- nil
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-listenErr:
		if err != nil {
			r.Stop()
			return fmt.Errorf("server startup failed: %w", err)
		}
	case sig := <-quit:
		LogInfo("Shutdown signal received: %v", sig)
	}

	LogInfo("Shutting down server...")
	return r.Stop()
}

func (r *Router) Stop() error {
	if r.server == nil {
		return nil
	}

	if r.rateLimiter != nil {
		r.rateLimiter.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := r.server.Shutdown(ctx)

	if r.db != nil {
		r.db.Close()
	}
	CloseLogger()

	return err
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if r.forceHTTPS {
		isSecure := false
		if req.TLS != nil {
			isSecure = true
		} else {
			proto := req.Header.Get("X-Forwarded-Proto")
			if proto == "https" {
				isSecure = true
			}
		}

		if !isSecure {
			target := "https://" + req.Host + req.RequestURI
			http.Redirect(w, req, target, http.StatusMovedPermanently)
			return
		}
	}

	h := w.Header()

	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")

	if r.forceHTTPS {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}

	if r.shouldCache(req.URL.Path) {
		h.Set("Cache-Control", "public, max-age=3600")
	} else {
		h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")
	}

	res := r.resPool.Get().(*Resources)

	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)

	res.reset(w, req, r.renderer)
	res.encryptKey = r.encryptKey
	res.csrfEnabled = r.csrfEnabled

	defer func() {
		if req.MultipartForm != nil {
			req.MultipartForm.RemoveAll()
		}
	}()

	defer func() {
		res.reset(nil, nil, nil)
		r.resPool.Put(res)
	}()

	defer func() {
		if res.Session != nil {
			if saveErr := res.Session.Save(); saveErr != nil {
				LogError("Session Save Error: %v", saveErr)
			}
		}
	}()

	if r.db != nil {
		res.Database = r.db.WithContext(req.Context())
	}

	if r.sessionStore != nil {
		rawSess, err := r.sessionStore.Get(req, "sessions")
		if err != nil {
			rawSess, _ = r.sessionStore.New(req, "sessions")
		}
		res.Session = newSession(rawSess, req, w)

		if res.Session.Get("_t") == nil {
			res.Session.Set("_t", time.Now().UnixNano())
		}
	}

	if r.rateLimiter != nil {
		key := r.keyFunc(req, res.Session)
		if !r.rateLimiter.allow(key) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("Too Many Requests"))
			return
		}
	}

	if r.csrfEnabled && req.Method != http.MethodGet && req.Method != http.MethodHead && req.Method != http.MethodOptions {
		isExempt := false
		for _, p := range r.csrfExemptPaths {
			if strings.HasPrefix(req.URL.Path, p) {
				isExempt = true
				break
			}
		}

		if !isExempt {
			csrfToken := req.Header.Get("X-CSRF-Token")

			if csrfToken == "" {
				ct := req.Header.Get("Content-Type")
				if strings.HasPrefix(ct, "application/x-www-form-urlencoded") || strings.HasPrefix(ct, "multipart/form-data") {
					if err := req.ParseForm(); err == nil {
						csrfToken = req.FormValue("csrf_token")
					}
				}
			}

			if !res.VerifyCSRFToken(csrfToken) {
				err := &HTTPError{
					Code:    http.StatusForbidden,
					Message: "Invalid or expired CSRF token",
				}
				r.handleError(res, err)

				statusCode := http.StatusForbidden
				if _statusRecorder, ok := w.(*statusRecorder); ok {
					statusCode = _statusRecorder.statusCode
				}

				if statusCode >= 400 {
					LogError("%s %s %d CSRF_INVALID", req.Method, req.URL.Path, statusCode)
				}
				return
			}
		}
	}
	var finalHandler HandlerFunc
	var params []param

	r.mu.RLock()
	root := r.trees[req.Method]
	handler, params := r.findRoute(root, req.URL.Path)
	r.mu.RUnlock()

	if handler == nil {
		r.mu.RLock()
		methodExists := r.anyMethodExists(req.URL.Path)
		r.mu.RUnlock()

		if methodExists {
			finalHandler = chain(r.noMethod, r.middleware...)
		} else {
			finalHandler = chain(r.noRoute, r.middleware...)
		}
	} else {
		res.params = params
		finalHandler = handler
	}

	var _statusRecorder *statusRecorder
	if _, ok := w.(*wafResponseWriter); !ok {
		_statusRecorder = &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		w = _statusRecorder
		res.Response = _statusRecorder
	}

	err := finalHandler(res)

	if err != nil {
		r.handleError(res, err)
	}

	statusCode := http.StatusOK
	if _statusRecorder != nil {
		statusCode = _statusRecorder.statusCode
	} else if ww, ok := w.(*wafResponseWriter); ok {
		statusCode = ww.status
	}

	shouldLog := true
	if statusCode == http.StatusOK && r.shouldSkipLog(req.URL.Path) {
		shouldLog = false
	}

	if shouldLog {
		if statusCode >= 400 {
			LogError("%s %s %d", req.Method, req.URL.Path, statusCode)
		} else if r.debug {
			LogInfo("%s %s %d", req.Method, req.URL.Path, statusCode)
		}
	}
}

func (r *Router) handleError(c *Resources, err error) {
	if err == nil {
		return
	}

	code := http.StatusInternalServerError
	msg := "Internal Server Error"

	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Code > 0 {
			code = httpErr.Code
		}
		if httpErr.Message != "" {
			msg = httpErr.Message
		}
	}

	if code >= 500 {
		LogError("Internal Error: %v", err)
		LogTelegram(c.Request, err)
	} else if code >= 400 {
		LogWarn("Client Error (%d): %v", code, err)
	}

	if c.written {
		return
	}

	if ww, ok := c.Response.(*wafResponseWriter); ok && ww.wrote {
		return
	}

	r.errorHandler(c, code, msg, err)
}

func (r *Router) defaultErrorHandler(c *Resources, code int, msg string, err error) {
	if c.IsAJAX() {
		c.Json(code, msg, map[string][]string{}, []string{})
		return
	}
	c.String(code, fmt.Sprintf("%d %s\n\n%s", code, http.StatusText(code), msg))
}

func (r *Router) defaultNoRoute(c *Resources) error {
	return c.String(http.StatusNotFound, "404 page not found")
}

func (r *Router) defaultNoMethod(c *Resources) error {
	return c.String(http.StatusMethodNotAllowed, "405 method not allowed")
}

func (r *Router) addRoute(method, path string, h HandlerFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fullPath := cleanPath(r.prefix + path)

	if r.trees[method] == nil {
		r.trees[method] = &node{}
	}

	current := r.trees[method]
	segments := splitPath(fullPath)

	if len(segments) == 0 {
		current.handler = chain(h, r.middleware...)
		return
	}

	for i, seg := range segments {
		n := parseSegment(seg)

		var child *node
		for _, c := range current.children {
			if sameNode(c, n) {
				child = c
				break
			}
		}

		if child == nil {
			for _, c := range current.children {
				isConflict := false
				if c.nType == paramNode && (n.nType == paramNode || n.nType == regexNode) {
					isConflict = true
				}
				if (c.nType == regexNode || c.nType == wildcardNode) && (n.nType == paramNode || n.nType == regexNode) {
					isConflict = true
				}
				if c.nType == wildcardNode && n.nType == wildcardNode {
					isConflict = true
				}

				if isConflict {
					LogError("ROUTE CONFLICT: Cannot register '%s'. Segment '%s' conflicts with existing '%s'.", fullPath, seg, c.part)
					panic(fmt.Sprintf("route conflict: %s", fullPath))
				}
			}

			child = n
			current.children = append(current.children, child)
		}

		current = child

		if n.nType == wildcardNode {
			current.handler = chain(h, r.middleware...)
			return
		}

		if i == len(segments)-1 {
			current.handler = chain(h, r.middleware...)
		}
	}
}

func (r *Router) findRoute(root *node, path string) (HandlerFunc, []param) {
	if root == nil {
		return nil, nil
	}

	segments := splitPath(cleanPath(path))
	var params []param

	h := r.search(root, segments, &params)
	return h, params
}

func (r *Router) search(n *node, segments []string, params *[]param) HandlerFunc {
	if len(segments) == 0 {
		return n.handler
	}

	seg := segments[0]
	rest := segments[1:]

	for _, c := range n.children {
		if c.nType == static && c.part == seg {
			if h := r.search(c, rest, params); h != nil {
				return h
			}
		}
	}

	for _, c := range n.children {
		if c.nType == regexNode && c.regex.MatchString(seg) {
			*params = append(*params, param{c.paramName, seg})
			if h := r.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == paramNode {
			*params = append(*params, param{c.paramName, seg})
			if h := r.search(c, rest, params); h != nil {
				return h
			}
			*params = (*params)[:len(*params)-1]
		}
	}

	for _, c := range n.children {
		if c.nType == wildcardNode {
			val := strings.Join(segments, "/")
			*params = append(*params, param{c.paramName, val})
			return c.handler
		}
	}

	return nil
}

func (r *Router) anyMethodExists(path string) bool {
	for _, root := range r.trees {
		if root != nil {
			if h, _ := r.findRoute(root, path); h != nil {
				return true
			}
		}
	}
	return false
}

func chain(h HandlerFunc, m ...Middleware) HandlerFunc {
	if h == nil {
		return nil
	}

	next := h

	for i := len(m) - 1; i >= 0; i-- {
		mw := m[i]
		currentNext := next

		next = func(c *Resources) error {
			if c.aborted {
				return nil
			}
			return mw(currentNext)(c)
		}
	}

	return next
}

func parseSegment(seg string) *node {
	if !strings.HasPrefix(seg, "{") {
		return &node{part: seg, nType: static}
	}

	body := seg[1 : len(seg)-1]

	if len(body) > 100 {
		return &node{nType: paramNode, paramName: body}
	}

	parts := strings.SplitN(body, ":", 2)
	name := parts[0]

	if len(parts) == 2 {
		pattern := parts[1]
		if pattern == "*" {
			return &node{nType: wildcardNode, paramName: name}
		}
		re, err := regexp.Compile("^" + pattern + "$")
		if err == nil {
			return &node{nType: regexNode, paramName: name, regex: re}
		}
	}
	return &node{nType: paramNode, paramName: name}
}

func sameNode(a, b *node) bool {
	return a.nType == b.nType && a.part == b.part && a.paramName == b.paramName
}

func splitPath(p string) []string {
	if p == "/" {
		return nil
	}
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func cleanPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}

func parseIPPort(addr string, defaultPort int) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, defaultPort
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return host, defaultPort
	}
	return host, port
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) error {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")

	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return nil
		}
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	if stat.IsDir() {
		indexName := path.Join(name, "index.html")
		idx, err := fsys.Open(indexName)
		if err == nil {
			defer idx.Close()
			idxStat, _ := idx.Stat()
			ct := mime.TypeByExtension(".html")
			w.Header().Set("Content-Type", ct)
			if rs, ok := idx.(io.ReadSeeker); ok {
				http.ServeContent(w, r, "index.html", idxStat.ModTime(), rs)
			} else {
				io.Copy(w, idx)
			}
			return nil
		}
		http.NotFound(w, r)
		return nil
	}

	ct := mime.TypeByExtension(filepath.Ext(stat.Name()))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)

	etag := fmt.Sprintf(`"%x"`, stat.ModTime().UnixNano())
	w.Header().Set("ETag", etag)

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
	} else {
		io.Copy(w, f)
	}

	return nil
}

func realIP(r *http.Request) string {
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	if isPrivateIP(remoteIP) && globalTrustProxyHeaders.Load() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" {
					return ip
				}
			}
		}

		if xrip := r.Header.Get("X-Real-IP"); xrip != "" {
			return xrip
		}
	}

	return remoteIP
}

func isPrivateIP(ip string) bool {
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return false
	}

	if ipAddr.IsLoopback() {
		return true
	}
	if ipAddr.IsPrivate() {
		return true
	}
	return false
}
