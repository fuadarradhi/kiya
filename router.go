package kiya

import (
	"context"
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

	khttp "github.com/fuadarradhi/kiya/internal/http"
	"github.com/fuadarradhi/kiya/internal/logger"
	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/security"
)

// NOTE: HandlerFunc, Middleware, and GroupFunc are declared in handler.go.
// NOTE: HTTPError is declared in errors.go.

// Router is the main HTTP router and framework entrypoint.
// All fields are unexported to prevent external modification.
type Router struct {
	_ [0]func() // Prevents struct literal construction

	tree         *router.Tree
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
	database     *DB
	sessionStore *sessions.CookieStore
	renderer     *khttp.Renderer

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
}

// adapterFunc converts a kiya.HandlerFunc to an internal router.HandlerFunc.
// It casts the generic `any` context back to *kiya.Resources.
func adapterFunc(h HandlerFunc) router.HandlerFunc {
	return func(c any) error {
		if res, ok := c.(*Resources); ok {
			return h(res)
		}
		return errors.New("invalid context type")
	}
}

// adapterMiddleware converts a kiya.Middleware to an internal router.Middleware.
func adapterMiddleware(m Middleware) router.Middleware {
	return func(next router.HandlerFunc) router.HandlerFunc {
		kiyaNext := func(res *Resources) error {
			return next(res)
		}
		kiyaWrapped := m(kiyaNext)
		return adapterFunc(kiyaWrapped)
	}
}

func (r *Router) SetErrorHandler(fn func(*Resources, int, string, error)) { r.errorHandler = fn }
func (r *Router) SetNoRoute(h HandlerFunc)                                { r.noRoute = h }
func (r *Router) SetNoMethod(h HandlerFunc)                               { r.noMethod = h }

func (r *Router) Use(m ...Middleware) {
	r.middleware = append(r.middleware, m...)

	// Update middleware on the existing tree (keeps already-registered routes).
	internalMws := make([]router.Middleware, len(r.middleware))
	for i, m := range r.middleware {
		internalMws[i] = adapterMiddleware(m)
	}
	r.tree.SetMiddleware(internalMws)
}

func (r *Router) Route(prefix string, fn GroupFunc) {
	sub := &Router{
		tree:              r.tree,
		middleware:        append([]Middleware{}, r.middleware...),
		errorHandler:      r.errorHandler,
		prefix:            r.prefix + prefix,
		resPool:           r.resPool,
		waf:               r.waf,
		sessionStore:      r.sessionStore,
		database:          r.database,
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

func (r *Router) addRoute(method, path string, h HandlerFunc) {
	fullPath := r.prefix + path
	r.tree.AddRoute(method, fullPath, adapterFunc(h))
}

func (r *Router) Static(prefix, root string) error {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimSuffix(prefix, "/")
	return r.StaticFS(prefix, os.DirFS(root))
}

func (r *Router) StaticFS(prefix string, fsys fs.FS) error {
	r.Get(prefix+"/{path:*}", func(c *Resources) error {
		p := c.Param("path")
		return router.ServeStatic(c.Response(), c.Request(), fsys, p)
	})
	return nil
}

func (r *Router) Redirect(path, target string, code int) {
	r.Get(path, func(c *Resources) error {
		http.Redirect(c.Response(), c.Request(), target, code)
		return nil
	})
}

func (r *Router) createRootHandler() http.Handler {
	rootHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.serveInternal(w, req)
	})

	if r.waf != nil {
		return router.WrapWithWAF(rootHandler, r.waf, r.maxWAFBufferSize)
	}
	return rootHandler
}

// serveInternal is the core HTTP request handler.
func (r *Router) serveInternal(w http.ResponseWriter, req *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			logger.LogError("PANIC recovered (Global): %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		}
	}()

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
	defer func() {
		res.reset(nil, nil, nil)
		r.resPool.Put(res)
	}()

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
		if res.Session() != nil {
			if saveErr := res.Session().Save(); saveErr != nil {
				logger.LogError("Session Save Error: %v", saveErr)
			}
		}
	}()

	if r.database != nil {
		// We assign to internal field using a trick since Resources is opaque in same package
		res.database = r.database.WithContext(req.Context())
	}

	if r.sessionStore != nil {
		rawSess, err := r.sessionStore.Get(req, "sessions")
		if err != nil {
			rawSess, _ = r.sessionStore.New(req, "sessions")
		}
		res.session = NewSession(rawSess, req, w)

		if res.Session().Get("_t") == nil {
			res.Session().Set("_t", time.Now().UnixNano())
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

	var finalHandler HandlerFunc
	if handler == nil {
		if r.tree.AnyMethodExists(req.URL.Path) {
			finalHandler = chain(r.noMethod, r.middleware...)
		} else {
			finalHandler = chain(r.noRoute, r.middleware...)
		}
	} else {
		res.params = params
		// We need to call the internal handler which expects `any`
		// So we wrap it back to a kiya.HandlerFunc
		finalHandler = func(r2 *Resources) error {
			return handler(r2)
		}
	}

	// Wrap response writer to record status code (skip if already wrapped, e.g. by WAF).
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
			logger.LogError("%s %s %d", req.Method, req.URL.Path, statusCode)
		} else if r.debug {
			logger.LogInfo("%s %s %d", req.Method, req.URL.Path, statusCode)
		}
	}
}

// chain links middleware together.
func chain(h HandlerFunc, m ...Middleware) HandlerFunc {
	if h == nil {
		return nil
	}
	next := h
	for i := len(m) - 1; i >= 0; i-- {
		mw := m[i]
		currentNext := next
		next = func(c *Resources) error {
			if c.IsAborted() {
				return nil
			}
			return mw(currentNext)(c)
		}
	}
	return next
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
