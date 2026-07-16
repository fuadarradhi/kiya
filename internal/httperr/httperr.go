package httperr

import "net/http"

type HTTPError struct {
	Code    int
	Message string
	Err     error
}

func New(code int, msg string, err ...error) *HTTPError {
	e := &HTTPError{
		Code:    code,
		Message: msg,
	}
	if len(err) > 0 {
		e.Err = err[0]
	}
	return e
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
