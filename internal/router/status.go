package router

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

type StatusRecorder interface {
	StatusCode() int
}

type WrittenChecker interface {
	Written() bool
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) StatusCode() int {
	return rec.statusCode
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

func NewStatusRecorder(w http.ResponseWriter) http.ResponseWriter {
	return &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
}
