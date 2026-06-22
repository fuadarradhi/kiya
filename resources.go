package kiya

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
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

var (
	reFormTag    = regexp.MustCompile(`(?i)<form\b[^>]*>`)
	reMethodAttr = regexp.MustCompile(`(?i)method\s*=\s*["']?\s*(\w+)`)
	reHeadTag    = regexp.MustCompile(`(?i)<head\b[^>]*>`)
)

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

func (r *Resources) BindJSON(v any) error {
	body, err := r.GetBody()
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
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

func (r *Resources) File(key string) (*multipart.FileHeader, error) {
	if r.Request.MultipartForm == nil {
		if err := r.Request.ParseMultipartForm(maxMultipartMemory); err != nil {
			return nil, err
		}
	}
	_, fh, err := r.Request.FormFile(key)
	if err != nil {
		return nil, err
	}
	return fh, nil
}

func (r *Resources) SaveFile(key string, dstPath string) error {
	cleanPath := filepath.Clean(dstPath)

	if filepath.IsAbs(cleanPath) {
		return errors.New("invalid destination path: absolute paths are not allowed")
	}

	if strings.Contains(cleanPath, "..") {
		return errors.New("invalid destination path: path traversal detected")
	}

	if strings.ContainsAny(cleanPath, "\x00") {
		return errors.New("invalid destination path: null character detected")
	}

	if r.Request.MultipartForm == nil {
		if err := r.Request.ParseMultipartForm(maxMultipartMemory); err != nil {
			return err
		}
	}

	src, _, err := r.Request.FormFile(key)
	if err != nil {
		return err
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		return err
	}

	dst, err := os.OpenFile(cleanPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return errors.New("file already exists")
		}
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
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

func (r *Resources) Encrypt(plaintext []byte) (string, error) {
	if len(r.encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}

	block, err := aes.NewCipher(r.encryptKey)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)

	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

func (r *Resources) Decrypt(encoded string) ([]byte, error) {
	if len(r.encryptKey) == 0 {
		return nil, fmt.Errorf("encryption key not configured")
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode base64url: %w", err)
	}

	block, err := aes.NewCipher(r.encryptKey)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce := data[:nonceSize]
	ciphertextWithTag := data[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, ciphertextWithTag, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}

func (r *Resources) EncryptString(plaintext string) (string, error) {
	return r.Encrypt([]byte(plaintext))
}

func (r *Resources) DecryptString(encoded string) (string, error) {
	plaintext, err := r.Decrypt(encoded)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (r *Resources) GenerateCSRFToken() (string, error) {
	if len(r.encryptKey) == 0 {
		return "", fmt.Errorf("encryption key not configured")
	}
	if r.Session == nil {
		return "", fmt.Errorf("session not available")
	}

	sessionID := r.Session.ID()
	if sessionID == "" {
		sessionID = fmt.Sprintf("%v", r.Session.Get("_t"))
	}

	timestamp := time.Now().Unix()
	plaintext := fmt.Sprintf("%s|%d", sessionID, timestamp)
	return r.EncryptString(plaintext)
}

func (r *Resources) VerifyCSRFToken(token string) bool {
	if len(r.encryptKey) == 0 || token == "" {
		return false
	}
	if r.Session == nil {
		return false
	}

	plaintext, err := r.DecryptString(token)
	if err != nil {
		return false
	}

	parts := strings.SplitN(plaintext, "|", 2)
	if len(parts) != 2 {
		return false
	}

	tokenSessionID := parts[0]
	timestampStr := parts[1]

	currentSessionID := r.Session.ID()
	if currentSessionID == "" {
		currentSessionID = fmt.Sprintf("%v", r.Session.Get("_t"))
	}

	if tokenSessionID != currentSessionID {
		return false
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false
	}

	now := time.Now().Unix()
	elapsed := now - timestamp
	if elapsed < 0 || elapsed > 7200 {
		return false
	}

	return true
}

func (r *Resources) ExtractIP() string {
	if r.Request == nil {
		return ""
	}

	remoteIP, _, err := net.SplitHostPort(r.Request.RemoteAddr)
	if err != nil {
		remoteIP = r.Request.RemoteAddr
	}

	if isPrivateIP(remoteIP) {
		if xff := r.Request.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			for i := len(parts) - 1; i >= 0; i-- {
				ip := strings.TrimSpace(parts[i])
				if ip != "" {
					return ip
				}
			}
		}

		if xri := r.Request.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return remoteIP
}

func injectCSRFIntoForms(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf_token"`) ||
		strings.Contains(html, `name='csrf_token'`) {
		return html
	}

	escapedToken := htmlEscape(token)
	csrfInput := fmt.Sprintf(
		`<input type="hidden" name="csrf_token" value="%s">`,
		escapedToken,
	)

	return reFormTag.ReplaceAllStringFunc(html, func(match string) string {
		methodMatches := reMethodAttr.FindStringSubmatch(match)

		method := "GET"
		if len(methodMatches) >= 2 && methodMatches[1] != "" {
			method = strings.ToUpper(methodMatches[1])
		}

		if method == "GET" {
			return match
		}

		return match + csrfInput
	})
}

func injectCSRFMeta(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf-token"`) ||
		strings.Contains(html, `name='csrf-token'`) {
		return html
	}

	escapedToken := htmlEscape(token)
	meta := fmt.Sprintf(
		`<meta name="csrf-token" content="%s">`,
		escapedToken,
	)

	return reHeadTag.ReplaceAllStringFunc(html, func(match string) string {
		return match + meta
	})
}
