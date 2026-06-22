package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/fuadarradhi/kiya/internal/decoder"
)

// MaxMultipartMemory defines the max memory for parsing multipart forms.
const MaxMultipartMemory int64 = 10 << 20

// GetBody reads the request body and caches it. It returns the cached body
// and restores the request body for subsequent reads.
func GetBody(w http.ResponseWriter, req *http.Request, cachedBody []byte) ([]byte, error) {
	if cachedBody != nil {
		req.Body = io.NopCloser(bytes.NewReader(cachedBody))
		return cachedBody, nil
	}

	if req.Body == nil {
		return []byte{}, nil
	}

	limitedReader := http.MaxBytesReader(w, req.Body, MaxMultipartMemory)
	b, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	req.Body = io.NopCloser(bytes.NewReader(b))

	return b, nil
}

// Get retrieves a query string value.
func Get(req *http.Request, key string) string {
	return req.URL.Query().Get(key)
}

// Post retrieves a post form value.
func Post(req *http.Request, key string) string {
	if req.PostForm == nil {
		req.ParseMultipartForm(MaxMultipartMemory)
	}
	return req.PostForm.Get(key)
}

// GetPost retrieves a value from query first, then post form.
func GetPost(req *http.Request, key string) string {
	val := req.URL.Query().Get(key)
	if val != "" {
		return val
	}

	if req.PostForm == nil {
		req.ParseMultipartForm(MaxMultipartMemory)
	}
	return req.PostForm.Get(key)
}

// PostGet retrieves a value from post form first, then query.
func PostGet(req *http.Request, key string) string {
	if req.PostForm == nil {
		req.ParseMultipartForm(MaxMultipartMemory)
	}
	val := req.PostForm.Get(key)
	if val != "" {
		return val
	}

	return req.URL.Query().Get(key)
}

// IsAJAX checks if the request is an AJAX request.
func IsAJAX(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept"), "application/json") ||
		strings.Contains(req.Header.Get("Content-Type"), "application/json") ||
		req.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

// BindJSON decodes a JSON body into v.
func BindJSON(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// Bind reads the body and binds it to v (JSON or Form).
// It returns the read body bytes so they can be cached by the caller.
func Bind(w http.ResponseWriter, req *http.Request, cachedBody []byte, v any) ([]byte, error) {
	body, err := GetBody(w, req, cachedBody)
	if err != nil {
		return body, err
	}

	ct := req.Header.Get("Content-Type")

	if strings.Contains(ct, "application/json") {
		err = BindJSON(body, v)
		return body, err
	}

	if strings.Contains(ct, "multipart/form-data") {
		if err := req.ParseMultipartForm(MaxMultipartMemory); err != nil {
			return body, err
		}
	} else {
		if err := req.ParseForm(); err != nil {
			return body, err
		}
	}

	err = decoder.FormDecoder.Decode(v, req.PostForm)
	return body, err
}
