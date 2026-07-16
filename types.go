package kiya

import (
	"net/http"

	"github.com/gorilla/sessions"

	"github.com/fuadarradhi/kiya/internal/db"
	"github.com/fuadarradhi/kiya/internal/httperr"
	"github.com/fuadarradhi/kiya/internal/security"
)

type HandlerFunc func(c *Context) error

type Middleware func(HandlerFunc) HandlerFunc

type GroupFunc func(r *Router)

type Session = security.Session

func NewSession(raw *sessions.Session, r *http.Request, w http.ResponseWriter) *Session {
	return security.NewSession(raw, r, w)
}

type HTTPError = httperr.HTTPError

func NewHTTPError(code int, msg string, err ...error) *HTTPError {
	return httperr.New(code, msg, err...)
}

type RouteInfo struct {
	Method string
	Path   string
	Name   string
}

type WhereFunc = db.WhereFunc
