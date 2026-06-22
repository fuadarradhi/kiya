package kiya

import (
	"net/http"

	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/gorilla/sessions"
)

// Session is an alias for security.Session to hide internal implementation.
type Session = security.Session

// NewSession creates a new session wrapper. Called internally by the router.
func NewSession(raw *sessions.Session, r *http.Request, w http.ResponseWriter) *Session {
	return security.NewSession(raw, r, w)
}
