// Package storage provides a small, cloud-agnostic file storage
// abstraction ("Disks"), similar in spirit to Laravel's Filesystem or
// Flysystem. kiya's core only ships the Disk interface plus a LocalDisk
// implementation (local.go) — it deliberately does NOT bundle an AWS
// SDK / GCS / MinIO client, so apps that don't need cloud storage don't
// pay for that dependency weight. Add a cloud-backed Disk implementation
// in your own app (or a small companion package) and Register it under
// whatever name you like:
//
//	storage.Register("s3", myS3Disk)
//	storage.Register("local", storage.NewLocalDisk("./uploads", "/files"))
//
//	d, _ := storage.Use("s3")
//	err := d.PutBytes("reports/2026-07.csv", data)
package storage

import (
	"errors"
	"io"
	"mime/multipart"
	"sync"
)

// Disk is the contract every storage backend implements. Keep it small —
// anything beyond basic put/get/delete/url belongs in the concrete
// implementation, not here, so adding a new backend never means adding a
// new required method to every existing one.
type Disk interface {
	// Put streams an uploaded file (as received via *Context.File) to path.
	Put(path string, file multipart.File, header *multipart.FileHeader) error

	// PutBytes writes raw bytes to path — for generated content (reports,
	// exported files) that didn't come from a multipart upload.
	PutBytes(path string, data []byte) error

	// Get opens path for reading. Caller must Close() the result.
	Get(path string) (io.ReadCloser, error)

	Delete(path string) error

	Exists(path string) (bool, error)

	// URL returns a URL the stored file can be retrieved from. For
	// LocalDisk this is just BaseURL + path (assumes you're serving it via
	// Router.Static / StaticFS); a cloud disk would typically return a
	// signed/public object URL instead.
	URL(path string) string
}

var (
	mu    sync.RWMutex
	disks = make(map[string]Disk)
)

// Register makes a Disk available under name for later retrieval via Use.
// Typically called once during app startup (e.g. alongside kiya.New(...)).
func Register(name string, d Disk) {
	mu.Lock()
	defer mu.Unlock()
	disks[name] = d
}

// Use retrieves a previously Registered Disk by name.
func Use(name string) (Disk, error) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := disks[name]
	if !ok {
		return nil, errors.New("kiya/storage: disk '" + name + "' is not registered")
	}
	return d, nil
}
