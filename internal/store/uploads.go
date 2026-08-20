package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/aenawi/uhp-go/internal/domain"
)

// MemoryUploads holds uploaded input files in memory, satisfying
// service.Uploads.
//
// In memory for the same reason MemoryStore is: this server runs with no
// database and no hosted dependency, and an upload is short-lived — a client
// uploads a file in order to reference it from a task. An operator who needs
// uploads to outlive the process implements the same two methods against disk
// and changes one line in cmd/uhpd/main.go.
type MemoryUploads struct {
	mu    sync.RWMutex
	files map[string]domain.Upload
}

func NewMemoryUploads() *MemoryUploads {
	return &MemoryUploads{files: make(map[string]domain.Upload)}
}

func (u *MemoryUploads) Put(_ context.Context, up domain.Upload) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.files[up.ID] = copyUpload(up)
	return nil
}

func (u *MemoryUploads) Get(_ context.Context, id string) (domain.Upload, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	up, ok := u.files[id]
	if !ok {
		return domain.Upload{}, fmt.Errorf("store: upload %s not found", id)
	}
	return copyUpload(up), nil
}

// copyUpload copies the bytes as well as the struct, so a caller cannot mutate
// stored content after handing it over.
func copyUpload(up domain.Upload) domain.Upload {
	up.Data = append([]byte(nil), up.Data...)
	return up
}
