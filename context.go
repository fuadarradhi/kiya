package kiya

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"sync"

	"github.com/fuadarradhi/kiya/internal/router"
	"github.com/fuadarradhi/kiya/internal/security"
	"github.com/fuadarradhi/kiya/internal/util"
	"github.com/fuadarradhi/kiya/internal/web"
)

type Map = map[string]any

type JsonData struct {
	Status  int                 `json:"status"`
	Message string              `json:"message"`
	Errors  map[string][]string `json:"errors"`
	Data    any                 `json:"data"`
}

type Context struct {
	response        http.ResponseWriter
	request         *http.Request
	session         *Session
	database        *DB
	params          []router.Param
	locals          *Locals
	written         bool
	aborted         bool
	body            []byte
	encryptKey        []byte
	currentUserFunc   func(*Context) (any, string)
	browserID         string
	browserValid      bool
}

func (c *Context) Response() http.ResponseWriter { return c.response }
func (c *Context) Request() *http.Request        { return c.request }
func (c *Context) Session() *Session             { return c.session }
func (c *Context) Database() *DB                 { return c.database }
func (c *Context) Locals() *Locals               { return c.locals }
func (c *Context) BrowserID() string             { return c.browserID }
func (c *Context) ValidBrowser() bool            { return c.browserValid }

func (c *Context) reset(w http.ResponseWriter, req *http.Request) {
	c.response = w
	c.request = req
	c.session = nil
	c.database = nil
	c.params = c.params[:0]
	c.aborted = false
	c.body = nil
	c.encryptKey = nil
	c.currentUserFunc = nil
	c.browserID = ""
	c.browserValid = false

	if c.locals == nil {
		c.locals = NewLocals()
	} else {
		c.locals.Clear()
	}

	c.written = false
}

var modelCache sync.Map

type modelMetaData struct {
	bmFieldIdx int
	hasInit    bool
}

func Model[T any](res *Context) *T {
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

func (c *Context) Abort() {
	c.aborted = true
}

func (c *Context) IsAborted() bool {
	return c.aborted
}

func (c *Context) AbortWithStatus(code int) {
	c.Status(code)
	c.Abort()
}

func (c *Context) Status(code int) *Context {
	if !c.written {
		c.response.WriteHeader(code)
		c.written = true
	}
	return c
}

func (c *Context) String(code int, s string) error {
	c.response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	c.Status(code)
	_, err := io.WriteString(c.response, s)
	return err
}

func (c *Context) JSON(code int, obj any) error {
	c.response.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Status(code)
	return json.NewEncoder(c.response).Encode(obj)
}

func (c *Context) APIResponse(code int, message string, errs map[string][]string, data any) error {
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

	c.response.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Status(code)

	out, err := json.Marshal(j)
	if err != nil {
		return err
	}

	_, err = c.response.Write(out)
	return err
}



func (c *Context) Redirect(code int, redirectURL string) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	http.Redirect(c.response, c.request, redirectURL, code)
	c.written = true
	return nil
}

func (c *Context) RedirectWithQuery(code int, to string, queryParams Map) error {
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

	http.Redirect(c.response, c.request, parsedURL.String(), code)
	c.written = true
	return nil
}

func (c *Context) RedirectWithRequery(code int, to string, queryParams ...Map) error {
	if !isValidRedirectCode(code) {
		return errors.New("redirect status code must be 3xx (300-399)")
	}

	parsedURL, err := url.Parse(to)
	if err != nil {
		return err
	}

	query := c.request.URL.Query()
	if len(queryParams) > 0 {
		for _, params := range queryParams {
			for key, value := range params {
				query.Set(key, fmt.Sprintf("%v", value))
			}
		}
	}
	parsedURL.RawQuery = query.Encode()

	http.Redirect(c.response, c.request, parsedURL.String(), code)
	c.written = true
	return nil
}

func (c *Context) Param(key string) string {
	for _, p := range c.params {
		if p.Key == key {
			return p.Value
		}
	}
	return ""
}

func (c *Context) Get(key string) string {
	return web.Get(c.request, key)
}

func (c *Context) Post(key string) string {
	return web.Post(c.request, key)
}

func (c *Context) GetPost(key string) string {
	return web.GetPost(c.request, key)
}

func (c *Context) PostGet(key string) string {
	return web.PostGet(c.request, key)
}

func (c *Context) IsAJAX() bool {
	return web.IsAJAX(c.request)
}

func (c *Context) GetBody() ([]byte, error) {
	b, err := web.GetBody(c.response, c.request, c.body)
	if err != nil {
		return nil, err
	}
	c.body = b
	return b, nil
}

func (c *Context) BindJSON(v any) error {
	body, err := c.GetBody()
	if err != nil {
		return err
	}
	return web.BindJSON(body, v)
}

func (c *Context) Bind(v any) error {
	b, err := web.Bind(c.response, c.request, c.body, v)
	if err != nil {
		return err
	}
	c.body = b
	return nil
}

func (c *Context) Validator(val any, bind ...bool) *Validator {
	v := &Validator{ctx: c}
	if val != nil {
		v.Bind(val, bind...)
	}
	return v
}

func (c *Context) File(key string) (*multipart.FileHeader, error) {
	return web.File(c.request, key)
}

func (c *Context) SaveFile(key string, dst string) error {
	return web.SaveFile(c.request, key, dst)
}

func (c *Context) Encrypt(plaintext []byte) (string, error) {
	return security.Encrypt(plaintext, c.encryptKey)
}

func (c *Context) Decrypt(encoded string) ([]byte, error) {
	return security.Decrypt(encoded, c.encryptKey)
}

func (c *Context) EncryptString(plaintext string) (string, error) {
	return security.EncryptString(plaintext, c.encryptKey)
}

func (c *Context) DecryptString(encoded string) (string, error) {
	return security.DecryptString(encoded, c.encryptKey)
}

func (c *Context) EncryptID(id int64) (string, error) {
	return c.EncryptString(strconv.FormatInt(id, 10))
}

func (c *Context) DecryptID(encoded ...string) (int64, error) {
	val := ""
	if len(encoded) > 0 {
		val = encoded[0]
	}
	if val == "" {
		val = c.Get("id")
	}
	if val == "" {
		return 0, errors.New("id tidak ditemukan di request")
	}

	str, err := c.DecryptString(val)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(str, 10, 64)
}

func (c *Context) ExtractIP() string {
	return util.RealIP(c.request)
}

// GenerateCSRFToken issues a token bound to the current session, encrypted with the
// app's configured encryption key (kiya.WithEncryption). Valid for 7200s.
func (c *Context) GenerateCSRFToken() (string, error) {
	return security.GenerateCSRFToken(c.session, c.encryptKey)
}

// VerifyCSRFToken checks a token produced by GenerateCSRFToken against the current
// session and encryption key.
func (c *Context) VerifyCSRFToken(token string) bool {
	return security.VerifyCSRFToken(token, c.session, c.encryptKey)
}

func (c *Context) CurrentUser() (id any, name string) {
	if c.currentUserFunc == nil {
		return nil, ""
	}
	return c.currentUserFunc(c)
}

func isValidRedirectCode(code int) bool {
	return code >= 300 && code <= 399
}
