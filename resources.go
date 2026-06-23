package kiya

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/web"
)

// Map is a shortcut for map[string]any
type Map = map[string]any

// JsonData defines the standard JSON response structure.
type JsonData struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
	Data    any                 `json:"data"`
}

// Resources is the context for each HTTP request.
type Resources struct {
	response    http.ResponseWriter
	request     *http.Request
	session     *Session
	database    *DB
	params      []router.Param
	locals      *Locals
	renderer    *web.Renderer
	written     bool
	aborted     bool
	body        []byte
	encryptKey  []byte
	csrfEnabled bool
}

// Getters
func (r *Resources) Response() http.ResponseWriter { return r.response }
func (r *Resources) Request() *http.Request        { return r.request }
func (r *Resources) Session() *Session             { return r.session }
func (r *Resources) Database() *DB                 { return r.database }
func (r *Resources) Locals() *Locals               { return r.locals }

func (r *Resources) reset(w http.ResponseWriter, req *http.Request, renderer *web.Renderer) {
	r.response = w
	r.request = req
	r.session = nil
	r.database = nil
	r.renderer = renderer
	r.params = r.params[:0]
	r.aborted = false
	r.body = nil
	r.encryptKey = nil
	r.csrfEnabled = false

	if r.locals == nil {
		r.locals = &Locals{
			store: make(map[string]any),
		}
	} else {
		r.locals.Clear()
	}

	r.written = false
}

// Model initializes a new model instance and binds it to the resources.
func Model[T any](res *Resources) *T {
	instance := new(T)

	val := reflect.ValueOf(instance).Elem()
	bmField := val.FieldByName("BaseModel")

	if bmField.IsValid() && bmField.CanAddr() {
		initMethod := bmField.Addr().MethodByName("Init")
		if initMethod.IsValid() {
			args := []reflect.Value{
				reflect.ValueOf(res.Database()),
				reflect.ValueOf(res),
				reflect.ValueOf(instance),
			}
			initMethod.Call(args)
		}
	}

	return instance
}

func (r *Resources) Abort() {
	r.aborted = true
}

func (r *Resources) IsAborted() bool {
	return r.aborted
}

func (r *Resources) AbortWithStatus(code int) {
	r.Status(code)
	r.Abort()
}

func (r *Resources) Status(code int) *Resources {
	if !r.written {
		if r.session != nil {
			if err := r.session.Save(); err != nil {
				LogError("Session Save Error before WriteHeader: %v", err)
			}
		}
		r.response.WriteHeader(code)
		r.written = true
	}
	return r
}

func (r *Resources) String(code int, s string) error {
	r.response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	r.Status(code)
	_, err := io.WriteString(r.response, s)
	return err
}

func (r *Resources) JSON(code int, obj any) error {
	r.response.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Status(code)
	return json.NewEncoder(r.response).Encode(obj)
}

// APIResponse writes the standard structured JSON envelope (JsonData).
func (r *Resources) APIResponse(code int, message string, errs map[string][]string, data any) error {
	if data == nil {
		data = []string{}
	}

	if errs == nil {
		errs = make(map[string][]string)
	}

	j := JsonData{
		Status:  code,
		Message: message,
		Errors:  errs,
		Data:    data,
	}

	r.response.Header().Set("Content-Type", "application/json; charset=utf-8")
	r.Status(code)

	out, err := json.Marshal(j)
	if err != nil {
		return err
	}

	_, err = r.response.Write(out)
	return err
}

func (r *Resources) Render(code int, name string, data ...Map) error {
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

		return r.APIResponse(code, message, map[string][]string{}, jsonData)
	}

	var ctx Map
	if len(data) > 0 && data[0] != nil {
		ctx = data[0]
	} else {
		ctx = make(Map)
	}

	if _, exists := ctx["Request"]; !exists {
		ctx["Request"] = r.request
	}

	if len(r.encryptKey) > 0 {
		ctx["_encKey"] = r.encryptKey
	}

	var csrfToken string
	if r.csrfEnabled && len(r.encryptKey) > 0 {
		if token, err := web.GenerateCSRFToken(r.session, r.encryptKey); err == nil {
			csrfToken = token
			ctx["csrf_token"] = csrfToken
		}
	}

	var buf bytes.Buffer
	if err := r.renderer.Render(&buf, name, ctx); err != nil {
		return err
	}

	htmlStr := buf.String()
	if csrfToken != "" {
		htmlStr = web.InjectCSRFIntoForms(htmlStr, csrfToken)
		htmlStr = web.InjectCSRFMeta(htmlStr, csrfToken)
	}

	r.response.Header().Set("Content-Type", "text/html; charset=utf-8")
	r.Status(code)
	_, err := io.WriteString(r.response, htmlStr)
	return err
}

func (r *Resources) Redirect(code int, redirectURL string) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.session != nil {
		if err := r.session.Save(); err != nil {
			return err
		}
	}

	http.Redirect(r.response, r.request, redirectURL, code)
	r.written = true
	return nil
}

func (r *Resources) RedirectWithQuery(code int, to string, queryParams Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.session != nil {
		if err := r.session.Save(); err != nil {
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

	http.Redirect(r.response, r.request, parsedURL.String(), code)
	r.written = true
	return nil
}

func (r *Resources) RedirectWithRequery(code int, to string, queryParams ...Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	if r.session != nil {
		if err := r.session.Save(); err != nil {
			return err
		}
	}

	parsedURL, err := url.Parse(to)
	if err != nil {
		return err
	}

	query := r.request.URL.Query()
	if len(queryParams) > 0 {
		for _, params := range queryParams {
			for key, value := range params {
				query.Set(key, fmt.Sprintf("%v", value))
			}
		}
	}
	parsedURL.RawQuery = query.Encode()

	http.Redirect(r.response, r.request, parsedURL.String(), code)
	r.written = true
	return nil
}

// HTTP Helpers Delegation
func (r *Resources) Param(key string) string {
	for _, p := range r.params {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

func (r *Resources) Get(key string) string {
	return web.Get(r.request, key)
}

func (r *Resources) Post(key string) string {
	return web.Post(r.request, key)
}

func (r *Resources) GetPost(key string) string {
	return web.GetPost(r.request, key)
}

func (r *Resources) PostGet(key string) string {
	return web.PostGet(r.request, key)
}

func (r *Resources) IsAJAX() bool {
	return web.IsAJAX(r.request)
}

func (r *Resources) GetBody() ([]byte, error) {
	b, err := web.GetBody(r.response, r.request, r.body)
	if err != nil {
		return nil, err
	}
	r.body = b
	return b, nil
}

func (r *Resources) BindJSON(v any) error {
	body, err := r.GetBody()
	if err != nil {
		return err
	}
	return web.BindJSON(body, v)
}

func (r *Resources) Bind(v any) error {
	b, err := web.Bind(r.response, r.request, r.body, v)
	if err != nil {
		return err
	}
	r.body = b
	return nil
}

func (r *Resources) Validator(val any, bind ...bool) *Validator {
	v := &Validator{res: r}
	if val != nil {
		v.Bind(val, bind...)
	}
	return v
}

func (r *Resources) File(key string) (*multipart.FileHeader, error) {
	return web.File(r.request, key)
}

func (r *Resources) SaveFile(key string, dst string) error {
	return web.SaveFile(r.request, key, dst)
}

func (r *Resources) Encrypt(plaintext []byte) (string, error) {
	return web.Encrypt(plaintext, r.encryptKey)
}

func (r *Resources) Decrypt(encoded string) ([]byte, error) {
	return web.Decrypt(encoded, r.encryptKey)
}

func (r *Resources) EncryptString(plaintext string) (string, error) {
	return web.EncryptString(plaintext, r.encryptKey)
}

func (r *Resources) DecryptString(encoded string) (string, error) {
	return web.DecryptString(encoded, r.encryptKey)
}

func (r *Resources) GenerateCSRFToken() (string, error) {
	return web.GenerateCSRFToken(r.session, r.encryptKey)
}

func (r *Resources) VerifyCSRFToken(token string) bool {
	return web.VerifyCSRFToken(token, r.session, r.encryptKey)
}

func (r *Resources) ExtractIP() string {
	return web.ExtractIP(r.request)
}

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
	case reflect.Bool, reflect.String,
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

// Locals provides a concurrency-safe key-value store for request-local state.
type Locals struct {
	store map[string]any
	mu    sync.RWMutex
}

func (g *Locals) Set(key string, value any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store[key] = value
}

func (g *Locals) Get(key string) any {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key]; ok {
		return val
	}
	return nil
}

func (g *Locals) Has(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.store[key]
	return ok
}

func (g *Locals) Del(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.store, key)
}

func (g *Locals) Clear() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.store = make(map[string]any)
}

func (g *Locals) GetString(key string) string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(string); ok {
		return val
	}
	return ""
}

func (g *Locals) GetInt(key string) int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	}
	return 0
}

func (g *Locals) GetInt64(key string) int64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	}
	return 0
}

func (g *Locals) GetFloat64(key string) float64 {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

func (g *Locals) GetBool(key string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	switch v := g.store[key].(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(v)
		return b
	case int:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}

func (g *Locals) GetTime(key string) time.Time {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if val, ok := g.store[key].(time.Time); ok {
		return val
	}
	return time.Time{}
}
