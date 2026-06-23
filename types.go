package kiya

import (
	"net/http"

	"github.com/gorilla/sessions"

	"github.com/fuadarradhi/kiya/internal/security"
)

type HandlerFunc func(res *Resources) error

type Middleware func(HandlerFunc) HandlerFunc

type GroupFunc func(r *Router)

type Session = security.Session

func NewSession(raw *sessions.Session, r *http.Request, w http.ResponseWriter) *Session {
	return security.NewSession(raw, r, w)
}

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

type RouteInfo struct {
	Method string
	Path   string
	Name   string
}
