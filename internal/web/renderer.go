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
	"strings"
	"sync"

	"github.com/flosch/pongo2/v6"
)

type EmbedLoader struct {
	fs fs.FS
}

func newEmbedLoader(fsys fs.FS) *EmbedLoader {
	return &EmbedLoader{fs: fsys}
}

func (l *EmbedLoader) Abs(base, name string) string {
	if path.IsAbs(name) {
		return path.Clean(strings.TrimPrefix(name, "/"))
	}
	return path.Clean(path.Join(path.Dir(base), name))
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

type Renderer struct {
	set *pongo2.TemplateSet
}

var registerFiltersOnce sync.Once

func NewRenderer(embedFS fs.FS) *Renderer {
	if embedFS == nil {
		return nil
	}

	loader := newEmbedLoader(embedFS)
	set := pongo2.NewSet("embed", loader)

	registerFiltersOnce.Do(func() {
		set.Globals["requery"] = func(args ...*pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			if len(args) < 2 {
				return nil, &pongo2.Error{Sender: "func:requery", OrigError: errors.New("requires 2 arguments (url and Request)")}
			}
			targetStr := args[0].String()
			req, ok := args[1].Interface().(*http.Request)
			if !ok || req == nil {
				return pongo2.AsValue(targetStr), nil
			}

			parsedURL, err := url.Parse(targetStr)
			if err != nil {
				return nil, &pongo2.Error{Sender: "func:requery", OrigError: err}
			}

			merged := parsedURL.Query()
			currentQuery := req.URL.Query()
			for k, v := range currentQuery {
				merged[k] = v
			}
			parsedURL.RawQuery = merged.Encode()

			return pongo2.AsValue(parsedURL.String()), nil
		}

		set.Globals["encrypt"] = func(args ...*pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			if len(args) < 2 {
				return nil, &pongo2.Error{Sender: "func:encrypt", OrigError: errors.New("requires 2 arguments (string and encKey)")}
			}
			plaintext := args[0].String()
			encKey, ok := args[1].Interface().([]byte)
			if !ok || len(encKey) == 0 {
				return nil, &pongo2.Error{Sender: "func:encrypt", OrigError: errors.New("invalid encryption key")}
			}

			encoded, err := Encrypt([]byte(plaintext), encKey)
			if err != nil {
				return nil, &pongo2.Error{Sender: "func:encrypt", OrigError: err}
			}
			return pongo2.AsValue(encoded), nil
		}

		pongo2.RegisterFilter("json_encode", filterJSONEncode)

		set.Globals["repeat"] = func(args ...*pongo2.Value) (*pongo2.Value, *pongo2.Error) {
			if len(args) < 2 {
				return nil, &pongo2.Error{Sender: "func:repeat", OrigError: errors.New("requires 2 arguments (string and count)")}
			}
			str := args[0].String()
			count := int(args[1].Integer())
			return pongo2.AsValue(strings.Repeat(str, count)), nil
		}
	})

	return &Renderer{
		set: set,
	}
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

	delete(ctx, "_encKey")
	delete(ctx, "Request")

	tpl, err := r.set.FromCache(name)
	if err != nil {
		return err
	}

	return tpl.ExecuteWriter(ctx, w)
}

func (r *Renderer) Output(name string, data ...map[string]any) ([]byte, error) {
	var buf bytes.Buffer

	if err := r.Render(&buf, name, data...); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
