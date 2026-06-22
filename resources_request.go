package kiya

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (r *Resources) GetBody() ([]byte, error) {
	if r.body != nil {
		r.Request.Body = io.NopCloser(bytes.NewReader(r.body))
		return r.body, nil
	}

	if r.Request.Body == nil {
		return []byte{}, nil
	}

	limitedReader := http.MaxBytesReader(r.Response, r.Request.Body, 10<<20)
	b, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, err
	}

	r.body = b
	r.Request.Body = io.NopCloser(bytes.NewReader(b))

	return b, nil
}

func (r *Resources) Param(key string) string {
	for _, p := range r.params {
		if p.key == key {
			return p.value
		}
	}
	return ""
}

func (r *Resources) Get(key string) string {
	return r.Request.URL.Query().Get(key)
}

func (r *Resources) Post(key string) string {
	if r.Request.PostForm == nil {
		r.Request.ParseMultipartForm(maxMultipartMemory)
	}
	return r.Request.PostForm.Get(key)
}

func (r *Resources) GetPost(key string) string {
	val := r.Request.URL.Query().Get(key)
	if val != "" {
		return val
	}

	if r.Request.PostForm == nil {
		r.Request.ParseMultipartForm(maxMultipartMemory)
	}
	return r.Request.PostForm.Get(key)
}

func (r *Resources) PostGet(key string) string {
	if r.Request.PostForm == nil {
		r.Request.ParseMultipartForm(maxMultipartMemory)
	}
	val := r.Request.PostForm.Get(key)
	if val != "" {
		return val
	}

	return r.Request.URL.Query().Get(key)
}

func (r *Resources) IsAJAX() bool {
	return strings.Contains(r.Request.Header.Get("Accept"), "application/json") ||
		strings.Contains(r.Request.Header.Get("Content-Type"), "application/json") ||
		r.Request.Header.Get("X-Requested-With") == "XMLHttpRequest"
}

func (r *Resources) BindJSON(v any) error {
	body, err := r.GetBody()
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

func (r *Resources) Bind(v any) error {
	if _, err := r.GetBody(); err != nil {
		return err
	}

	ct := r.Request.Header.Get("Content-Type")

	if strings.Contains(ct, "application/json") {
		return r.BindJSON(v)
	}

	if strings.Contains(ct, "multipart/form-data") {
		if err := r.Request.ParseMultipartForm(maxMultipartMemory); err != nil {
			return err
		}
	} else {
		if err := r.Request.ParseForm(); err != nil {
			return err
		}
	}

	return formDecoder.Decode(v, r.Request.PostForm)
}

func (r *Resources) Validator(val any, bind ...bool) *Validator {
	v := &Validator{
		res: r,
	}

	if val != nil {
		v.Bind(val, bind...)
	}

	return v
}
