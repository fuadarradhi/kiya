package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/flosch/pongo2/v6"
)

var (
	reqStore       sync.Map
	encKeyStore    sync.Map
	ridByGoroutine sync.Map
	renderSeq      uint64
)

func nextRenderID() uint64 {
	return atomic.AddUint64(&renderSeq, 1)
}

func safeGoroutineID() uint64 {
	b := make([]byte, 64)
	n := runtime.Stack(b, false)
	s := string(b[:n])
	const prefix = "goroutine "
	if !strings.HasPrefix(s, prefix) {
		return 0
	}
	s = s[len(prefix):]
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	id, err := strconv.ParseUint(s[:end], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func currentRenderID() uint64 {
	gid := safeGoroutineID()
	if gid == 0 {
		return 0
	}
	val, ok := ridByGoroutine.Load(gid)
	if !ok {
		return 0
	}
	return val.(uint64)
}

func filterRequery(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	rid := currentRenderID()
	if rid == 0 {
		return in, nil
	}

	reqVal, ok := reqStore.Load(rid)
	if !ok {
		return in, nil
	}

	req, ok := reqVal.(*http.Request)
	if !ok || req == nil {
		return in, nil
	}

	targetStr := in.String()
	parsedURL, err := url.Parse(targetStr)
	if err != nil {
		return nil, &pongo2.Error{
			Sender:    "filter:requery",
			OrigError: fmt.Errorf("invalid target URL: %v", err),
		}
	}

	merged := parsedURL.Query()
	currentQuery := req.URL.Query()
	for k, v := range currentQuery {
		merged[k] = v
	}
	parsedURL.RawQuery = merged.Encode()

	return pongo2.AsValue(parsedURL.String()), nil
}

func filterEncrypt(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	rid := currentRenderID()
	if rid == 0 {
		return nil, &pongo2.Error{
			Sender:    "filter:encrypt",
			OrigError: fmt.Errorf("render context not available"),
		}
	}

	keyVal, ok := encKeyStore.Load(rid)
	if !ok {
		return nil, &pongo2.Error{
			Sender:    "filter:encrypt",
			OrigError: fmt.Errorf("encryption key not available"),
		}
	}

	encKey, ok := keyVal.([]byte)
	if !ok || len(encKey) == 0 {
		return nil, &pongo2.Error{
			Sender:    "filter:encrypt",
			OrigError: fmt.Errorf("invalid encryption key"),
		}
	}

	// Use the shared Encrypt helper so template output matches Decrypt
	// (AES-256-GCM, base64url). Previously this emitted hex and could not
	// be decrypted on the next request.
	encoded, err := Encrypt([]byte(in.String()), encKey)
	if err != nil {
		return nil, &pongo2.Error{
			Sender:    "filter:encrypt",
			OrigError: err,
		}
	}

	return pongo2.AsValue(encoded), nil
}

func filterJSONEncode(in *pongo2.Value, param *pongo2.Value) (*pongo2.Value, *pongo2.Error) {
	b, err := json.Marshal(in.Interface())
	if err != nil {
		return nil, &pongo2.Error{
			Sender:    "filter:json_encode",
			OrigError: fmt.Errorf("JSON encode error: %v", err),
		}
	}
	return pongo2.AsValue(string(b)), nil
}

// EmbedLoader loads templates from an embedded filesystem.
type EmbedLoader struct {
	fs fs.FS
}

func newEmbedLoader(fsys fs.FS) *EmbedLoader {
	return &EmbedLoader{fs: fsys}
}

func (l *EmbedLoader) Abs(base, name string) string {
	return path.Clean(name)
}

func (l *EmbedLoader) Get(name string) (io.Reader, error) {
	name = path.Clean(name)
	name = strings.TrimPrefix(name, "/")

	if name == "" || strings.HasPrefix(name, "..") {
		return nil, fmt.Errorf("invalid template path: %s", name)
	}

	b, err := fs.ReadFile(l.fs, name)
	if err != nil {
		return nil, err
	}

	return bytes.NewReader(b), nil
}

// Renderer wraps a pongo2 template set.
type Renderer struct {
	set *pongo2.TemplateSet
}

var registerFiltersOnce sync.Once

// NewRenderer creates a new Renderer from an embedded filesystem.
func NewRenderer(embedFS fs.FS) *Renderer {
	if embedFS == nil {
		return nil
	}

	loader := newEmbedLoader(embedFS)
	set := pongo2.NewSet("embed", loader)

	registerFiltersOnce.Do(func() {
		pongo2.RegisterFilter("requery", filterRequery)
		pongo2.RegisterFilter("encrypt", filterEncrypt)
		pongo2.RegisterFilter("json_encode", filterJSONEncode)
	})

	set.Globals["repeat"] = func(args ...*pongo2.Value) (*pongo2.Value, *pongo2.Error) {
		if len(args) < 2 {
			return nil, &pongo2.Error{
				Sender:    "func:repeat",
				OrigError: errors.New("requires 2 arguments (string and count)"),
			}
		}
		str := args[0].String()
		count := int(args[1].Integer())
		return pongo2.AsValue(strings.Repeat(str, count)), nil
	}

	return &Renderer{
		set: set,
	}
}

// Render executes a template and writes to the provided writer.
func (r *Renderer) Render(w io.Writer, name string, data ...map[string]any) error {
	if r == nil {
		return fmt.Errorf("renderer is not initialized")
	}

	if name == "" {
		return fmt.Errorf("template name is empty")
	}

	if !strings.HasSuffix(name, ".html") {
		name = name + ".html"
	}

	ctx := pongo2.Context{}
	if len(data) > 0 && data[0] != nil {
		ctx = pongo2.Context(data[0])
	}

	var encKey []byte
	if keyVal, ok := ctx["_encKey"]; ok {
		if castedKey, isKey := keyVal.([]byte); isKey {
			encKey = castedKey
		}
		delete(ctx, "_encKey")
	}

	var req *http.Request
	if rVal, ok := ctx["Request"]; ok {
		if castedReq, isReq := rVal.(*http.Request); isReq {
			req = castedReq
		}
	}

	rid := nextRenderID()
	gid := safeGoroutineID()

	if gid != 0 {
		ridByGoroutine.Store(gid, rid)
	}
	if req != nil {
		reqStore.Store(rid, req)
	}
	if encKey != nil {
		encKeyStore.Store(rid, encKey)
	}

	defer func() {
		if gid != 0 {
			ridByGoroutine.Delete(gid)
		}
		reqStore.Delete(rid)
		encKeyStore.Delete(rid)
	}()

	tpl, err := r.set.FromCache(name)
	if err != nil {
		return err
	}

	return tpl.ExecuteWriter(ctx, w)
}

// Output executes a template and returns the bytes.
func (r *Renderer) Output(name string, data ...map[string]any) ([]byte, error) {
	var buf bytes.Buffer

	if err := r.Render(&buf, name, data...); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}