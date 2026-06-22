package kiya

import (
	"errors"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
)

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
