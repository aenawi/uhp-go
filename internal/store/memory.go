// Package store holds the persistence engines. MemoryStore is the default,
// dependency-free backend and satisfies service.Store directly — no adapter,
// no wrapper. A disk-backed engine is additive: implement the same interface
// and change one line in cmd/uhpd/main.go.
package store

import (
	"context"
	"fmt"
	"sort"
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

// ListSessions returns one page of sessions, newest first.
//
// The ordering is deliberate and total: newest CreatedAt first, ties broken by
// id. Cursor paging over an unstable order silently skips and repeats rows, and
// map iteration has no order at all.
func (s *MemoryStore) ListSessions(_ context.Context, f domain.SessionFilter) (domain.SessionPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	all := make([]*domain.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if f.HarnessID != "" && sess.HarnessID != f.HarnessID {
			continue
		}
		cp := *sess
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID < all[j].ID
	})

	start := 0
	if f.Cursor != "" {
		for i, sess := range all {
			if sess.ID == f.Cursor {
				start = i + 1
				break
			}
		}
	}
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]

	next := ""
	if end < len(all) && len(page) > 0 {
		next = page[len(page)-1].ID
	}
	return domain.SessionPage{Sessions: page, NextCursor: next}, nil
}

// ListSessionTasks returns a session's tasks in the order they ran.
func (s *MemoryStore) ListSessionTasks(_ context.Context, sessionID string) ([]*domain.Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*domain.Task, 0, 4)
	for _, t := range s.tasks {
		if t.SessionID == sessionID {
			out = append(out, copyTask(t))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
