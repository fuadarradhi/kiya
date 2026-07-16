package storage

import (
	"errors"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
)

// LocalDisk stores files on the local filesystem under Root. Same path
// safety rules as the existing internal/web.SaveFile (no absolute paths,
// no "..", no null bytes) — a cloud Disk implementation won't need these
// checks (the cloud SDK handles its own key namespacing), but a local one
// must, since Root is a real filesystem directory.
type LocalDisk struct {
	Root    string
	BaseURL string // e.g. "/files" if you serve Root via Router.StaticFS
}

func NewLocalDisk(root, baseURL string) *LocalDisk {
	return &LocalDisk{Root: root, BaseURL: strings.TrimSuffix(baseURL, "/")}
}

func (d *LocalDisk) resolve(path string) (string, error) {
	clean := filepath.Clean("/" + path)
	clean = strings.TrimPrefix(clean, "/")

	if strings.Contains(clean, "..") || strings.ContainsAny(clean, "\x00") || clean == "" {
		return "", errors.New("kiya/storage: invalid path")
	}
	return filepath.Join(d.Root, clean), nil
}

func (d *LocalDisk) Put(path string, file multipart.File, header *multipart.FileHeader) error {
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}

	dst, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, file)
	return err
}

func (d *LocalDisk) PutBytes(path string, data []byte) error {
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	return os.WriteFile(full, data, 0644)
}

func (d *LocalDisk) Get(path string) (io.ReadCloser, error) {
	full, err := d.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (d *LocalDisk) Delete(path string) error {
	full, err := d.resolve(path)
	if err != nil {
		return err
	}
	return os.Remove(full)
}

func (d *LocalDisk) Exists(path string) (bool, error) {
	full, err := d.resolve(path)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(full)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return false, nil
		}
		return false, statErr
	}
	return true, nil
}

func (d *LocalDisk) URL(path string) string {
	return d.BaseURL + "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")
}
