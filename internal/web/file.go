package web

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// File retrieves a file header from the request.
func File(req *http.Request, key string) (*multipart.FileHeader, error) {
	if req.MultipartForm == nil {
		if err := req.ParseMultipartForm(MaxMultipartMemory); err != nil {
			return nil, err
		}
	}
	_, fh, err := req.FormFile(key)
	if err != nil {
		return nil, err
	}
	return fh, nil
}

// SaveFile saves an uploaded file to the specified destination path.
func SaveFile(req *http.Request, key string, dstPath string) error {
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

	if req.MultipartForm == nil {
		if err := req.ParseMultipartForm(MaxMultipartMemory); err != nil {
			return err
		}
	}

	src, _, err := req.FormFile(key)
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

// SaveFileStream saves an uploaded file to the specified destination path using streaming.
// It is highly recommended for large files as it does not load the entire file into memory.
// Note: Using this function consumes the request body, so it should only be used when
// no other form fields need to be read from the request afterwards.
func SaveFileStream(req *http.Request, key string, dstPath string) error {
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

	mr, err := req.MultipartReader()
	if err != nil {
		return err
	}

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if part.FormName() == key {
			_, err = io.Copy(dst, part)
			return err
		}
	}

	return errors.New("file field not found in request")
}
