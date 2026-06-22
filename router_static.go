package kiya

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func (r *Router) Static(prefix, root string) error {
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimSuffix(prefix, "/")

	sub, err := fs.Sub(os.DirFS(root), ".")
	if err != nil {
		return fmt.Errorf("failed to create static fs: %w", err)
	}
	return r.StaticFS(prefix, sub)
}

func (r *Router) StaticFS(prefix string, fsys fs.FS) error {
	r.Get(prefix+"/{path:*}", func(c *Resources) error {
		p := c.Param("path")
		return serveStatic(c.Response, c.Request, fsys, p)
	})
	return nil
}

func (r *Router) Redirect(path, target string, code int) {
	r.Get(path, func(c *Resources) error {
		http.Redirect(c.Response, c.Request, target, code)
		return nil
	})
}

func serveStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) error {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")

	f, err := fsys.Open(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return nil
		}
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	if stat.IsDir() {
		indexName := path.Join(name, "index.html")
		idx, err := fsys.Open(indexName)
		if err == nil {
			defer idx.Close()
			idxStat, _ := idx.Stat()
			ct := mime.TypeByExtension(".html")
			w.Header().Set("Content-Type", ct)
			if rs, ok := idx.(io.ReadSeeker); ok {
				http.ServeContent(w, r, "index.html", idxStat.ModTime(), rs)
			} else {
				io.Copy(w, idx)
			}
			return nil
		}
		http.NotFound(w, r)
		return nil
	}

	ct := mime.TypeByExtension(filepath.Ext(stat.Name()))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)

	etag := fmt.Sprintf(`"%x"`, stat.ModTime().UnixNano())
	w.Header().Set("ETag", etag)

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
	} else {
		io.Copy(w, f)
	}

	return nil
}
