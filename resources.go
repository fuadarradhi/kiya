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
	"sync"

	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/web"
)

type Map = map[string]any

type JsonData struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
	Data    any                 `json:"data"`
}

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
		r.locals = NewLocals()
	} else {
		r.locals.Clear()
	}

	r.written = false
}

var modelCache sync.Map

type modelMetaData struct {
	bmFieldIdx int
	hasInit    bool
}

func Model[T any](res *Resources) *T {
	instance := new(T)
	var metaData *modelMetaData

	typ := reflect.TypeOf(instance).Elem()

	if cached, ok := modelCache.Load(typ); ok {
		metaData = cached.(*modelMetaData)
	} else {
		bmField, found := typ.FieldByName("BaseModel")
		if found {
			ptrType := reflect.PointerTo(typ)
			_, exists := ptrType.MethodByName("Init")
			metaData = &modelMetaData{
				bmFieldIdx: bmField.Index[0],
				hasInit:    exists,
			}
		} else {
			metaData = &modelMetaData{bmFieldIdx: -1}
		}
		modelCache.Store(typ, metaData)
	}

	if metaData.bmFieldIdx != -1 && metaData.hasInit {
		bmField := reflect.ValueOf(instance).Elem().Field(metaData.bmFieldIdx)
		args := []reflect.Value{
			reflect.ValueOf(res.Database()),
			reflect.ValueOf(res),
			reflect.ValueOf(instance),
		}
		bmField.Addr().MethodByName("Init").Call(args)
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
			jsonData = web.SanitizeForJSON(data[0])
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

	http.Redirect(r.response, r.request, redirectURL, code)
	r.written = true
	return nil
}

func (r *Resources) RedirectWithQuery(code int, to string, queryParams Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
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

func (r *Resources) NewWebSocket() (*web.WebSocketConn, error) {
	return web.NewWebSocket(r.response, r.request)
}

func isValidRedirectCode(code int) bool {
	return code >= 300 && code <= 399
}
