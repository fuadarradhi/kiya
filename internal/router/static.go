package router

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

func ServeStatic(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) error {
	name = path.Clean("/" + name)
	name = strings.TrimPrefix(name, "/")

	acceptEncoding := r.Header.Get("Accept-Encoding")
	var file fs.File
	var stat fs.FileInfo

	if strings.Contains(acceptEncoding, "br") {
		brName := name + ".br"
		if f, err := fsys.Open(brName); err == nil {
			if s, err := f.Stat(); err == nil && !s.IsDir() {
				file = f
				stat = s
				w.Header().Set("Content-Encoding", "br")
				w.Header().Add("Vary", "Accept-Encoding")
			}
		}
	}

	if file == nil && strings.Contains(acceptEncoding, "gzip") {
		gzName := name + ".gz"
		if f, err := fsys.Open(gzName); err == nil {
			if s, err := f.Stat(); err == nil && !s.IsDir() {
				file = f
				stat = s
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Add("Vary", "Accept-Encoding")
			}
		}
	}

	if file == nil {
		f, err := fsys.Open(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.NotFound(w, r)
				return nil
			}
			return err
		}
		file = f

		s, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		stat = s
	}
	defer file.Close()

	if stat.IsDir() {
		indexName := path.Join(name, "index.html")
		idx, err := fsys.Open(indexName)
		if err == nil {
			defer idx.Close()
			idxStat, _ := idx.Stat()

			if strings.Contains(acceptEncoding, "br") {
				if brIdx, err := fsys.Open(indexName + ".br"); err == nil {
					defer brIdx.Close()
					w.Header().Set("Content-Encoding", "br")
					w.Header().Add("Vary", "Accept-Encoding")
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					http.ServeContent(w, r, "index.html", idxStat.ModTime(), brIdx.(io.ReadSeeker))
					return nil
				}
			}

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

	ct := mime.TypeByExtension(filepath.Ext(name))
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

	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".css", ".js", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".woff", ".woff2", ".webp":
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case ".html":
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	default:
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}

	if rs, ok := file.(io.ReadSeeker); ok {
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), rs)
	} else {
		io.Copy(w, file)
	}

	return nil
}
