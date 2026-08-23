// Package store holds the persistence engines. MemoryStore and SQLiteStore
// both satisfy service.Store directly — no adapter, no wrapper — and
// cmd/uhpd/main.go picks between them on one line.
//
// MemoryStore is what a server with nowhere to write gets. It is not the
// preferred engine: a task it holds is gone on the next restart, and a client
// that stored the response id gets a 404 for work this server did. See
// SQLiteStore, and store_contract_test.go for the suite both have to pass.
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
// A shallow `cp := *t` leaves Metadata, Output and Artifacts aliasing the
// caller's memory, so a caller could mutate stored state after handing it over.
//
// Output is the one that bites hardest and was the one missed: the service
// holds a task across a whole run and calls Task.AppendText on every delta,
// which assigns to Output[i].Content[0].Text — a field in the backing array
// this store would otherwise share. Storage would then track a live run
// whether or not UpdateTask was ever called, and a failed update would leave
// the "unwritten" text in place anyway.
func copyTask(t *domain.Task) *domain.Task {
	cp := *t
	cp.Metadata = copyAnyMap(t.Metadata)
	cp.IncompleteDetails = copyAnyMap(t.IncompleteDetails)
	if t.Output != nil {
		cp.Output = make([]domain.OutputItem, len(t.Output))
		for i, item := range t.Output {
			cp.Output[i] = copyOutputItem(item)
		}
	}
	cp.Artifacts = copySlice(t.Artifacts)
	if t.Error != nil {
		e := *t.Error
		cp.Error = &e
	}
	if t.Usage != nil {
		u := *t.Usage
		cp.Usage = &u
	}
	return &cp
}

// copyOutputItem copies an item and the slices hanging off it. Content parts
// carry their own annotation slice, so a one-level copy is not enough.
func copyOutputItem(item domain.OutputItem) domain.OutputItem {
	if item.Content != nil {
		content := make([]domain.ContentPart, len(item.Content))
		for i, part := range item.Content {
			part.Annotations = copySlice(part.Annotations)
			content[i] = part
		}
		item.Content = content
	}
	item.Summary = copyAnySlice(item.Summary)
	return item
}

// copySlice copies a slice, preserving its length as well as whether it was
// nil at all.
//
// `append([]T(nil), s...)` is the shorter form and is wrong here: it returns
// nil for an empty input, silently turning an empty slice into a missing one.
// ContentPart.Annotations is exactly that case and it reaches the wire —
// domain renders it without `omitempty` on purpose, so nil becomes `null` and
// empty becomes `[]`, and a client is meant to be able to tell "no
// annotations" from "this server predates the field". Task.AppendText mints
// an empty one on every first delta.
func copySlice[T any](s []T) []T {
	if s == nil {
		return nil
	}
	return append(make([]T, 0, len(s)), s...)
}

// copyAnyMap and copyAnySlice copy a decoded-JSON tree, preserving the
// difference between nil and empty: a caller that sent no metadata and one
// that sent `{}` are not the same thing to the transport that renders it.
//
// They recurse because a serialising engine has no choice but to, and the two
// engines are held to one contract. A store that isolates the top level and
// shares a nested array would let a caller edit stored state through the one
// door it left open, and only on the engine that happens to be configured.
//
// Everything below is a JSON value — string, float64, bool, nil, []any or
// map[string]any — because that is what a decoder produces and metadata only
// ever arrives through one. The two composite cases are therefore the whole
// of the recursion.
func copyAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = copyAnyValue(v)
	}
	return out
}

func copyAnySlice(s []any) []any {
	if s == nil {
		return nil
	}
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = copyAnyValue(v)
	}
	return out
}

func copyAnyValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return copyAnyMap(v)
	case []any:
		return copyAnySlice(v)
	default:
		return v
	}
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
