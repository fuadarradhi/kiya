package storage

import (
	"errors"
	"io"
	"mime/multipart"
	"sync"
)

type Disk interface {
	Put(path string, file multipart.File, header *multipart.FileHeader) error

	PutBytes(path string, data []byte) error

	Get(path string) (io.ReadCloser, error)

	Delete(path string) error

	Exists(path string) (bool, error)

	URL(path string) string
}

var (
	mu    sync.RWMutex
	disks = make(map[string]Disk)
)

func Register(name string, d Disk) {
	mu.Lock()
	defer mu.Unlock()
	disks[name] = d
}

func Use(name string) (Disk, error) {
	mu.RLock()
	defer mu.RUnlock()
	d, ok := disks[name]
	if !ok {
		return nil, errors.New("kiya/storage: disk '" + name + "' is not registered")
	}
	return d, nil
}
