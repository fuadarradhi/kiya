package kiya

import (
	"net/http"

	"github.com/gorilla/sessions"

	"github.com/fuadarradhi/kiya/internal/security"
)

// HandlerFunc defines the handler signature for Kiya routes.
type HandlerFunc func(res *Resources) error

// Middleware defines the middleware signature.
type Middleware func(HandlerFunc) HandlerFunc

// GroupFunc defines the signature for route grouping.
type GroupFunc func(r *Router)

// Session is an alias for security.Session to hide internal implementation.
type Session = security.Session

// NewSession creates a new session wrapper. Called internally by the router.
func NewSession(raw *sessions.Session, r *http.Request, w http.ResponseWriter) *Session {
	return security.NewSession(raw, r, w)
}

// HTTPError represents an HTTP error with code, message, and underlying error.
type HTTPError struct {
	Code    int
	Message string
	Err     error
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return http.StatusText(e.Code)
}

// Unwrap allows errors.Is and errors.As to work with nested errors.
func (e *HTTPError) Unwrap() error {
	return e.Err
}

// RouteInfo holds information about a registered route.
type RouteInfo struct {
	Method string
	Path   string
	Name   string
}
