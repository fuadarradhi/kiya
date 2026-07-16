package kiya

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/corazawaf/coraza/v3"
	"github.com/gorilla/sessions"

	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/fuadarradhi/kiya/internal/web"
)

type contextKey string

const RequestIDKey contextKey = "request_id"

type Router struct {
	tree         *router.Tree
	middleware   []Middleware
	errorHandler func(*Context, int, string, error)
	noRoute      HandlerFunc
	noMethod     HandlerFunc
	prefix       string
	mu           sync.RWMutex
	resPool      *sync.Pool
	server       *http.Server
	addr         string
	waf          coraza.WAF
	database     *DB
	sessionStore sessions.Store
	renderer     *web.Renderer

	rateLimiter      *security.Store
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

	csp            string
	cspExemptPaths []string
	wafExemptPaths []string

	corsConfig         CORSConfig
	compressionEnabled bool
	compressionLevel   int
	requestIDEnabled   bool

	routeNames      map[string]string
	healthCheckPath string

	currentUserFunc func(*Context) (any, string)
}

func adapterFunc(h HandlerFunc) router.HandlerFunc {
	return func(c any) error {
		if c, ok := c.(*Context); ok {
			return h(c)
		}
		return errors.New("invalid context type")
	}
}

func adapterMiddleware(m Middleware) router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		kiyaNext := func(c *Context) error {
			return next(c)
		}
		kiyaWrapped := m(kiyaNext)
		return adapterFunc(kiyaWrapped)
	}
}

func (r *Router) SetErrorHandler(fn func(*Context, int, string, error)) { r.errorHandler = fn }
func (r *Router) SetNoRoute(h HandlerFunc)                              { r.noRoute = h }
func (r *Router) SetNoMethod(h HandlerFunc)                             { r.noMethod = h }

func (r *Router) Use(m ...Middleware) {
	r.middleware = append(r.middleware, m...)
}

func (r *Router) clone() *Router {
	return &Router{
		tree:         r.tree,
		middleware:   append([]Middleware{}, r.middleware...),
		errorHandler: r.errorHandler,
		noRoute:      r.noRoute,
		noMethod:     r.noMethod,
		prefix:       r.prefix,
		resPool:      r.resPool,
		server:       r.server,
		addr:         r.addr,
		waf:          r.waf,
		database:     r.database,
		sessionStore: r.sessionStore,
		renderer:     r.renderer,

		rateLimiter:      r.rateLimiter,
		keyFunc:          r.keyFunc,
		forceHTTPS:       r.forceHTTPS,
		maxWAFBufferSize: r.maxWAFBufferSize,
		sessionMaxAge:    r.sessionMaxAge,
		debug:            r.debug,
		sameSite:         r.sameSite,
		encryptKey:       r.encryptKey,

		cachePaths:        r.cachePaths,
		noLogSuccessPaths: r.noLogSuccessPaths,

		csrfEnabled:     r.csrfEnabled,
		csrfExemptPaths: r.csrfExemptPaths,

		csp:            r.csp,
		cspExemptPaths: r.cspExemptPaths,
		wafExemptPaths: r.wafExemptPaths,

		corsConfig:         r.corsConfig,
		compressionEnabled: r.compressionEnabled,
		compressionLevel:   r.compressionLevel,
		requestIDEnabled:   r.requestIDEnabled,

		routeNames:      r.routeNames,
		healthCheckPath: r.healthCheckPath,

		currentUserFunc: r.currentUserFunc,
	}
}

func (r *Router) Route(prefix string, fn GroupFunc) {
	sub := r.clone()
	sub.prefix = r.prefix + prefix
	fn(sub)
}

func (r *Router) Get(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodGet, path, h, name...)
}
func (r *Router) Post(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodPost, path, h, name...)
}
func (r *Router) Put(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodPut, path, h, name...)
}
func (r *Router) Delete(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodDelete, path, h, name...)
}
func (r *Router) Patch(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodPatch, path, h, name...)
}
func (r *Router) Options(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodOptions, path, h, name...)
}
func (r *Router) Head(path string, h HandlerFunc, name ...string) {
	r.addRoute(http.MethodHead, path, h, name...)
}

func (r *Router) addRoute(method, path string, h HandlerFunc, name ...string) {
	fullPath := r.prefix + path

	fullHandler := chain(h, r.middleware...)
	r.tree.AddRoute(method, fullPath, fullHandler)

	if len(name) > 0 && name[0] != "" {
		r.routeNames[name[0]] = fullPath
	}
}

func (r *Router) URL(name string, params ...Map) string {
	path, ok := r.routeNames[name]
	if !ok {
		return ""
	}

	if len(params) > 0 {
		for k, v := range params[0] {
			path = strings.ReplaceAll(path, ":"+k, fmt.Sprintf("%v", v))
		}
	}
	return path
}

func (r *Router) Static(prefix, root string) error {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return r.StaticFS(prefix, os.DirFS(root))
}

func (r *Router) StaticFS(prefix string, fsys fs.FS) error {
	r.Get(prefix+"/{path:*}", func(c *Context) error {
		p := c.Param("path")
		return router.ServeStatic(c.Response(), c.Request(), fsys, p)
	})
	return nil
}

func (r *Router) Redirect(path, target string, code int) {
	r.Get(path, func(c *Context) error {
		http.Redirect(c.Response(), c.Request(), target, code)
		return nil
	})
}

func generateRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("%x", b)
}

func (r *Router) createRootHandler() http.Handler {
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.serveInternal(w, req)
	})

	if r.waf == nil {
		return rootHandler
	}

	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for _, p := range r.wafExemptPaths {
			if strings.HasPrefix(req.URL.Path, p) {
				rootHandler.ServeHTTP(w, req)
				return
			}
		}
		router.WrapWithWAF(rootHandler, r.waf, r.maxWAFBufferSize).ServeHTTP(w, req)
	})
}

func (r *Router) serveInternal(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			logger.LogError("PANIC recovered (Global): %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()

	reqID := req.Header.Get("X-Request-Id")
	if reqID == "" {
		reqID = generateRequestID()
	}
	w.Header().Set("X-Request-Id", reqID)
	ctx := context.WithValue(req.Context(), RequestIDKey, reqID)
	req = req.WithContext(ctx)

	if r.corsConfig.Enabled {
		origin := req.Header.Get("Origin")
		if origin != "" {
			allowed := false
			if len(r.corsConfig.AllowOrigins) == 0 {
				allowed = true
			} else {
				for _, o := range r.corsConfig.AllowOrigins {
					if o == "*" || o == origin {
						allowed = true
						break
					}
				}
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if r.corsConfig.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}

				if req.Method == http.MethodOptions {
					if len(r.corsConfig.AllowMethods) > 0 {
						w.Header().Set("Access-Control-Allow-Methods", strings.Join(r.corsConfig.AllowMethods, ", "))
					}
					if len(r.corsConfig.AllowHeaders) > 0 {
						w.Header().Set("Access-Control-Allow-Headers", strings.Join(r.corsConfig.AllowHeaders, ", "))
					}
					if r.corsConfig.MaxAge > 0 {
						w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", int(r.corsConfig.MaxAge.Seconds())))
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}

				if len(r.corsConfig.ExposeHeaders) > 0 {
					w.Header().Set("Access-Control-Expose-Headers", strings.Join(r.corsConfig.ExposeHeaders, ", "))
				}
			}
		}
	}

	if r.forceHTTPS {
		isSecure := req.TLS != nil || req.Header.Get("X-Forwarded-Proto") == "https"
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
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-DNS-Prefetch-Control", "off")

	if r.forceHTTPS {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}

	if r.csp != "" {
		isExempt := false
		for _, p := range r.cspExemptPaths {
			if strings.HasPrefix(req.URL.Path, p) {
				isExempt = true
				break
			}
		}
		if !isExempt {
			h.Set("Content-Security-Policy", r.csp)
		}
	}

	if r.shouldCache(req.URL.Path) {
		h.Set("Cache-Control", "public, max-age=3600")
	} else {
		h.Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		h.Set("Pragma", "no-cache")
		h.Set("Expires", "0")
	}

	res := r.resPool.Get().(*Context)
	defer func() {
		res.reset(nil, nil, nil)
		r.resPool.Put(res)
	}()

	reqCtx, cancel := context.WithTimeout(req.Context(), 15*time.Second)
	defer cancel()
	req = req.WithContext(reqCtx)

	res.reset(w, req, r.renderer)
	res.encryptKey = r.encryptKey
	res.csrfEnabled = r.csrfEnabled
	res.currentUserFunc = r.currentUserFunc

	defer func() {
		if req.MultipartForm != nil {
			req.MultipartForm.RemoveAll()
		}
	}()

	defer func() {
		if res.Session() != nil {
			if saveErr := res.Session().Save(); saveErr != nil {
				logger.LogError("Session Save Error: %v", saveErr)
			}
		}
	}()

	if r.database != nil {
		res.database = r.database.WithContext(req.Context())
	}

	if r.sessionStore != nil {
		rawSess, err := r.sessionStore.Get(req, "sessions")
		if err != nil {
			rawSess, _ = r.sessionStore.New(req, "sessions")
		}
		res.session = NewSession(rawSess, req, w)

		if res.Session().Get("_t") == nil {
			b := make([]byte, 16)
			rand.Read(b)
			res.Session().Set("_t", hex.EncodeToString(b))
		}
	}

	if r.rateLimiter != nil {
		key := r.keyFunc(req, res.Session())
		if !r.rateLimiter.Allow(key) {
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
				return
			}
		}
	}

	handler, params := r.tree.FindRoute(req.Method, req.URL.Path)

	var finalHandler router.HandlerFunc
	if handler == nil {
		if r.tree.AnyMethodExists(req.URL.Path) {
			finalHandler = chain(r.noMethod, r.middleware...)
		} else {
			finalHandler = chain(r.noRoute, r.middleware...)
		}
	} else {
		res.params = params
		finalHandler = handler
	}

	var statusRec router.StatusRecorder
	if _, ok := w.(router.WrittenChecker); !ok {
		rec := router.NewStatusRecorder(w)
		w = rec
		res.response = w
		statusRec, _ = rec.(router.StatusRecorder)
	}

	err := finalHandler(res)
	if err != nil {
		r.handleError(res, err)
	}

	statusCode := http.StatusOK
	if statusRec != nil {
		statusCode = statusRec.StatusCode()
	} else if ww, ok := w.(router.StatusRecorder); ok {
		statusCode = ww.StatusCode()
	}

	shouldLog := true
	if statusCode == http.StatusOK && r.shouldSkipLog(req.URL.Path) {
		shouldLog = false
	}

	if shouldLog {
		if statusCode >= 400 {
			logger.LogError("%s %s %d [%s]", req.Method, req.URL.Path, statusCode, reqID)
		} else if r.debug {
			logger.LogInfo("%s %s %d [%s]", req.Method, req.URL.Path, statusCode, reqID)
		}
	}
}

func chain(h HandlerFunc, m ...Middleware) router.HandlerFunc {
	if h == nil {
		return nil
	}

	next := adapterFunc(h)

	for i := len(m) - 1; i >= 0; i-- {
		mw := m[i]
		currentNext := next
		wrapped := adapterMiddleware(mw)(currentNext)

		next = func(c any) error {
			if ctx, ok := c.(interface{ IsAborted() bool }); ok && ctx.IsAborted() {
				return nil
			}
			return wrapped(c)
		}
	}
	return next
}

func (r *Router) handleError(c *Context, err error) {
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
		logger.LogError("Internal Error: %v", err)
		logger.LogTelegram(c.Request(), err)
	} else if code >= 400 {
		logger.LogWarn("Client Error (%d): %v", code, err)
	}

	if c.written {
		return
	}

	r.errorHandler(c, code, msg, err)
}

func (r *Router) defaultErrorHandler(c *Context, code int, msg string, err error) {
	if c.IsAJAX() {
		c.APIResponse(code, http.StatusText(code), map[string][]string{}, []string{})
		return
	}

	if r.debug {
		c.String(code, fmt.Sprintf("%d %s\n\n%s", code, http.StatusText(code), msg))
	} else {
		c.String(code, fmt.Sprintf("%d %s", code, http.StatusText(code)))
	}
}

func (r *Router) defaultNoRoute(c *Context) error {
	return c.String(http.StatusNotFound, "404 page not found")
}

func (r *Router) defaultNoMethod(c *Context) error {
	return c.String(http.StatusMethodNotAllowed, "405 method not allowed")
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

func (r *Router) Start() error {
	r.server.Handler = r.createRootHandler()

	listenErr := make(chan error, 1)
	go func() {
		defer func() {
			if err := recover(); err != nil {
				logger.LogError("FATAL: Server panic: %v", err)
				listenErr <- fmt.Errorf("panic: %v", err)
			}
		}()
		logger.LogInfo("Server listening on %s", r.addr)
		err := r.server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.LogError("Server failed to listen: %v", err)
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
		logger.LogInfo("Shutdown signal received: %v", sig)
	}

	logger.LogInfo("Shutting down server...")
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

	if r.database != nil {
		r.database.Close()
	}
	logger.Close()

	return err
}
