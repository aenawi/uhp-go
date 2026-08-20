// Package store holds the persistence engines. MemoryStore is the default,
// dependency-free backend and satisfies service.Store directly — no adapter,
// no wrapper. A disk-backed engine is additive: implement the same interface
// and change one line in cmd/uhpd/main.go.
package store

import (
	"context"
	"fmt"
	"sync"

	"github.com/aenawi/uhp-go/internal/domain"
)

type MemoryStore struct {
	mu       sync.RWMutex
	tasks    map[string]*domain.Task
	sessions map[string]*domain.Session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:    make(map[string]*domain.Task),
		sessions: make(map[string]*domain.Session),
	}
}

func (s *MemoryStore) CreateTask(_ context.Context, t *domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = copyTask(t)
	return nil
}

func (s *MemoryStore) UpdateTask(_ context.Context, t *domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; !ok {
		return fmt.Errorf("store: task %s not found", t.ID)
	}
	s.tasks[t.ID] = copyTask(t)
	return nil
}

func (s *MemoryStore) GetTask(_ context.Context, id string) (*domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, fmt.Errorf("store: task %s not found", id)
	}
	return copyTask(t), nil
}

func (s *MemoryStore) AppendArtifact(_ context.Context, taskID string, a domain.Artifact) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[taskID]
	if !ok {
		return fmt.Errorf("store: task %s not found", taskID)
	}
	t.Artifacts = append(t.Artifacts, a)
	return nil
}

func (s *MemoryStore) CreateSession(_ context.Context, sess *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sess
	s.sessions[sess.ID] = &cp
	return nil
}

func (s *MemoryStore) GetSession(_ context.Context, id string) (*domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("store: session %s not found", id)
	}
	cp := *sess
	return &cp, nil
}

func (s *MemoryStore) UpdateSession(_ context.Context, sess *domain.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sess.ID]; !ok {
		return fmt.Errorf("store: session %s not found", sess.ID)
	}
	cp := *sess
	s.sessions[sess.ID] = &cp
	return nil
}

// copyTask deep-copies the reference-typed fields as well as the struct.
// A shallow `cp := *t` leaves Metadata and Artifacts aliasing the caller's
// memory, so a caller could mutate stored state after handing it over.
func copyTask(t *domain.Task) *domain.Task {
	cp := *t
	if t.Metadata != nil {
		cp.Metadata = make(map[string]any, len(t.Metadata))
		for k, v := range t.Metadata {
			cp.Metadata[k] = v
		}
	}
	if t.Artifacts != nil {
		cp.Artifacts = append([]domain.Artifact(nil), t.Artifacts...)
	}
	if t.Error != nil {
		e := *t.Error
		cp.Error = &e
	}
	return &cp
}
