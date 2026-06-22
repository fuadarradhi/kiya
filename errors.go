package kiya

import "net/http"

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
