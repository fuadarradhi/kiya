package kiya

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

type Map = map[string]any

type param struct {
	key   string
	value string
}

type JsonData struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
	Data    any                 `json:"data"`
}

const maxMultipartMemory int64 = 10 << 20

type Resources struct {
	Response    http.ResponseWriter
	Request     *http.Request
	Session     *Session
	Database    *DB
	params      []param
	Globals     *Globals
	renderer    *Renderer
	written     bool
	aborted     bool
	body        []byte
	encryptKey  []byte
	csrfEnabled bool
}

func (r *Resources) reset(w http.ResponseWriter, req *http.Request, renderer *Renderer) {
	r.Response = w
	r.Request = req
	r.Session = nil
	r.Database = nil
	r.renderer = renderer
	r.params = r.params[:0]
	r.aborted = false
	r.body = nil
	r.encryptKey = nil
	r.csrfEnabled = false

	if r.Globals == nil {
		r.Globals = &Globals{
			store: make(map[string]any),
		}
	} else {
		r.Globals.Clear()
	}

	r.written = false
}

func Model[T any](res *Resources) *T {
	instance := new(T)

	val := reflect.ValueOf(instance).Elem()
	bmField := val.FieldByName("BaseModel")

	if bmField.IsValid() && bmField.CanAddr() {
		initMethod := bmField.Addr().MethodByName("Init")
		if initMethod.IsValid() {
			args := []reflect.Value{
				reflect.ValueOf(res.Database),
				reflect.ValueOf(res),
				reflect.ValueOf(instance),
			}
			initMethod.Call(args)
		}
	}

	return instance
}

func (r *Resources) Model(model any) any {
	if model == nil {
		return nil
	}

	val := reflect.ValueOf(model)

	if val.Kind() != reflect.Ptr {
		return model
	}

	if val.Elem().Kind() == reflect.Struct {
		elem := val.Elem()
		bmField := elem.FieldByName("BaseModel")

		if bmField.IsValid() && bmField.Kind() == reflect.Struct {
			if bmField.CanAddr() {
				initMethod := bmField.Addr().MethodByName("Init")
				if initMethod.IsValid() {
					args := []reflect.Value{
						reflect.ValueOf(r.Database),
						reflect.ValueOf(r),
						reflect.ValueOf(model),
					}
					initMethod.Call(args)
				}
			}
		}
	}

	return model
}

func (r *Resources) Abort() {
	r.aborted = true
}

func (r *Resources) AbortWithStatus(code int) {
	r.Status(code)
	r.Abort()
}

func (r *Resources) Status(code int) *Resources {
	if !r.written {
		if r.Session != nil {
			if err := r.Session.Save(); err != nil {
				LogError("Session Save Error before WriteHeader: %v", err)
			}
		}
		r.Response.WriteHeader(code)
		r.written = true
	}
	return r
}

func (r *Resources) LogInfo(format string, v ...any) {
	LogInfo(format, v...)
}

func (r *Resources) LogWarn(format string, v ...any) {
	LogWarn(format, v...)
}

func (r *Resources) LogError(format string, v ...any) {
	LogError(format, v...)
}

func (r *Resources) String(code int, s string) error {
	r.Response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.Status(code)
	_, err := io.WriteString(r.Response, s)
	return err
}

func (r *Resources) JSON(code int, obj any) error {
	r.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Status(code)
	return json.NewEncoder(r.Response).Encode(obj)
}

func (r *Resources) Json(code int, message string, errors map[string][]string, data any) error {
	if data == nil {
		data = []string{}
	}

	if errors == nil {
		errors = make(map[string][]string)
	}

	j := JsonData{
		Status:  code,
		Message: message,
		Errors:  errors,
		Data:    data,
	}

	r.Response.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Status(code)

	out, err := json.Marshal(j)
	if err != nil {
		return err
	}

	_, err = r.Response.Write(out)
	return err
}

func (r *Resources) Render(code int, name string, data ...Map) error {
	r.Status(code)
	if r.renderer == nil {
		return errors.New("renderer is not initialized")
	}

	if r.IsAJAX() {
		message := http.StatusText(code)
		if len(data) > 0 && data[0] != nil {
			if msg, ok := data[0]["message"]; ok {
				if msgStr, ok := msg.(string); ok {
					message = msgStr
				}
			}
		}

		var jsonData any
		if len(data) > 0 && data[0] != nil {
			jsonData = sanitizeForJSON(data[0])
		} else {
			jsonData = []string{}
		}

		return r.Json(code, message, map[string][]string{}, jsonData)
	}

	var ctx Map
	if len(data) > 0 && data[0] != nil {
		ctx = data[0]
	} else {
		ctx = make(Map)
	}

	if _, exists := ctx["Request"]; !exists {
		ctx["Request"] = r.Request
	}

	if len(r.encryptKey) > 0 {
		ctx["_encKey"] = r.encryptKey
	}

	var csrfToken string
	if r.csrfEnabled && len(r.encryptKey) > 0 {
		if token, err := r.GenerateCSRFToken(); err == nil {
			csrfToken = token
			ctx["csrf_token"] = csrfToken
		}
	}

	var buf bytes.Buffer
	if err := r.renderer.Render(&buf, name, ctx); err != nil {
		return err
	}

	html := buf.String()
	if csrfToken != "" {
		html = injectCSRFIntoForms(html, csrfToken)
		html = injectCSRFMeta(html, csrfToken)
	}

	_, err := io.WriteString(r.Response, html)
	return err
}

func (r *Resources) Redirect(code int, redirectURL string) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.Session != nil {
		if err := r.Session.Save(); err != nil {
			return err
		}
	}

	http.Redirect(r.Response, r.Request, redirectURL, code)

	r.written = true
	return nil
}

func (r *Resources) RedirectWithQuery(code int, to string, queryParams Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.Session != nil {
		if err := r.Session.Save(); err != nil {
			return err
		}
	}

	parsedURL, err := url.Parse(to)
	if err != nil {
		return err
	}

	query := url.Values{}

	for key, value := range queryParams {
		query.Set(key, fmt.Sprintf("%v", value))
	}

	parsedURL.RawQuery = query.Encode()

	http.Redirect(r.Response, r.Request, parsedURL.String(), code)
	r.written = true
	return nil
}

func (r *Resources) RedirectWithRequery(code int, to string, queryParams ...Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.Session != nil {
		if err := r.Session.Save(); err != nil {
			return err
		}
	}

	parsedURL, err := url.Parse(to)
	if err != nil {
		return err
	}

	query := r.Request.URL.Query()

	if len(queryParams) > 0 {
		for _, params := range queryParams {
			for key, value := range params {
				query.Set(key, fmt.Sprintf("%v", value))
			}
		}
	}

	parsedURL.RawQuery = query.Encode()

	http.Redirect(r.Response, r.Request, parsedURL.String(), code)
	r.written = true
	return nil
}

// Digunakan oleh Redirect functions
func isValidRedirectCode(code int) bool {
	return code >= 300 && code <= 399
}

func sanitizeForJSON(v any) any {
	if v == nil {
		return nil
	}

	rv := reflect.ValueOf(v)

	for (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface) && !rv.IsNil() {
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.Bool,
		reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return rv.Interface()
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		result := make(map[string]any)
		iter := rv.MapRange()
		for iter.Next() {
			key := fmt.Sprintf("%v", iter.Key().Interface())
			result[key] = sanitizeForJSON(iter.Value().Interface())
		}
		return result
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		result := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			result[i] = sanitizeForJSON(rv.Index(i).Interface())
		}
		return result
	case reflect.Struct:
		if t, ok := rv.Interface().(time.Time); ok {
			if t.IsZero() {
				return nil
			}
			return t.Format(time.RFC3339)
		}
		result := make(map[string]any)
		rt := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			field := rt.Field(i)
			if !field.IsExported() {
				continue
			}
			jsonTag := field.Tag.Get("json")
			if jsonTag == "-" {
				continue
			}
			fieldName := field.Name
			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if len(parts) > 0 && parts[0] != "" && parts[0] != "-" {
					fieldName = parts[0]
				}
			}
			if rv.Field(i).CanInterface() {
				result[fieldName] = sanitizeForJSON(rv.Field(i).Interface())
			}
		}
		return result
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return nil
	default:
		return nil
	}
}
