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
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
)

type MemoryStore struct {
	mu       sync.RWMutex
	tasks    map[string]*domain.Task
	sessions map[string]*domain.Session

	// arrived records the order tasks were created in, which a map does not
	// keep and CreatedAt no longer supplies on its own.
	//
	// A response's created_at is Unix seconds — the protocol's resolution, not
	// this store's choice — and two tasks in one session routinely start inside
	// the same second. Ordering on the timestamp alone would then fall through
	// to the tie-break, and a task id is a UUID: the transcript a client
	// rebuilds from /turns would come back shuffled, deterministically and
	// wrongly. SQLiteStore answers the same question with rowid.
	arrived map[string]uint64
	nextSeq uint64

	// shares is keyed by share id, and sharedBy is the reverse index a session
	// is looked up through.
	//
	// The reverse index is kept rather than derived by scanning, unlike the
	// task sweep in DeleteSession below, because both directions are on a
	// request path: an idempotent POST reads the session's share before it
	// mints one, and every anonymous read resolves a share id. The two maps
	// are written under the same lock and are never allowed to disagree — a
	// share id in one and not the other is either a capability with no owner
	// or an owner whose revoke misses.
	shares   map[string]*domain.Share
	sharedBy map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:    make(map[string]*domain.Task),
		sessions: make(map[string]*domain.Session),
		arrived:  make(map[string]uint64),
		shares:   make(map[string]*domain.Share),
		sharedBy: make(map[string]string),
	}
}

func (s *MemoryStore) CreateTask(_ context.Context, t *domain.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[t.ID] = copyTask(t)
	// Only on create. An update must not move a task to the end of its own
	// session's history.
	if _, ok := s.arrived[t.ID]; !ok {
		s.nextSeq++
		s.arrived[t.ID] = s.nextSeq
	}
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

// GetTask answers with found=false for an id it does not hold. A map lookup
// cannot fail any other way, so the error is always nil here — it exists for
// the engines that read something that can.
func (s *MemoryStore) GetTask(_ context.Context, id string) (*domain.Task, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return nil, false, nil
	}
	return copyTask(t), true, nil
}

// DeleteTask removes a task. A missing id is found=false and not an error, for
// the reason GetTask gives.
func (s *MemoryStore) DeleteTask(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return false, nil
	}
	delete(s.tasks, id)
	// The arrival order goes with it. Leaving the entry behind would keep a
	// deleted task's position in its session's history reserved for ever, and
	// nextSeq only ever counts up, so nothing would reuse it.
	delete(s.arrived, id)
	return true, nil
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

// GetSession answers with found=false for an id it does not hold, for the
// reason GetTask gives.
func (s *MemoryStore) GetSession(_ context.Context, id string) (*domain.Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, false, nil
	}
	cp := *sess
	return &cp, true, nil
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

// DeleteSession removes a session and every task that ran in it. A missing id
// is found=false and not an error, for the reason GetSession gives.
//
// The sweep is over every task rather than over a per-session index, because
// this engine keeps none: the tasks map is the only record of which session a
// task belongs to. A store holding a conversation's worth of tasks is small
// enough that the scan costs nothing worth an index that every other write
// would then have to maintain.
func (s *MemoryStore) DeleteSession(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return false, nil
	}
	delete(s.sessions, id)
	// The share is a live capability, so it goes before anything else: a
	// reader that raced this delete must not still be able to resolve the id.
	_, _ = s.revokeLocked(id)
	for taskID, t := range s.tasks {
		if t.SessionID != id {
			continue
		}
		delete(s.tasks, taskID)
		// The arrival order goes with the task, for the reason DeleteTask gives.
		delete(s.arrived, taskID)
	}
	return true, nil
}

// CreateShare records a session's read-only view, or reports the one it already
// has.
//
// The session check, the lookup and the write are all under one lock, which is
// the whole point. Two concurrent shares of one session must not both mint —
// only one of the two ids can survive, and the other was already handed to a
// client as though it worked — and neither may interleave with a DeleteSession
// and leave a share row naming a conversation that is gone.
func (s *MemoryStore) CreateShare(_ context.Context, sh *domain.Share) (*domain.Share, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sh.SessionID]; !ok {
		return nil, false, nil
	}
	if id, ok := s.sharedBy[sh.SessionID]; ok {
		if existing, ok := s.shares[id]; ok {
			cp := *existing
			return &cp, true, nil
		}
		// The reverse index pointed at nothing, which the two writers here
		// cannot produce. Dropping the dangling entry is the repair; the
		// alternative is a session that can never be shared again.
		delete(s.sharedBy, sh.SessionID)
	}
	cp := *sh
	s.shares[sh.ID] = &cp
	s.sharedBy[sh.SessionID] = sh.ID
	out := *sh
	return &out, true, nil
}

// GetShare answers with found=false for an id it does not hold, for the reason
// GetTask gives — and a revoked id is exactly that, deliberately: "never
// existed" and "was revoked" are the same answer, so a probe learns nothing
// from the difference.
func (s *MemoryStore) GetShare(_ context.Context, shareID string) (*domain.Share, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh, ok := s.shares[shareID]
	if !ok {
		return nil, false, nil
	}
	cp := *sh
	return &cp, true, nil
}

// GetSessionShare finds a session's share, if it has one.
func (s *MemoryStore) GetSessionShare(_ context.Context, sessionID string) (*domain.Share, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.sharedBy[sessionID]
	if !ok {
		return nil, false, nil
	}
	sh, ok := s.shares[id]
	if !ok {
		return nil, false, nil
	}
	cp := *sh
	return &cp, true, nil
}

// CountShares reports how many shares this store holds.
//
// Read under the same lock as everything else, so the number is one this store
// was in at some instant rather than a walk of a map somebody is writing to.
func (s *MemoryStore) CountShares(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.shares), nil
}

// DeleteSessionShare revokes a session's share and reports which id it removed.
func (s *MemoryStore) DeleteSessionShare(_ context.Context, sessionID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, found := s.revokeLocked(sessionID)
	return id, found, nil
}

// revokeLocked drops a session's share from both maps and reports the id it
// removed. The caller holds the write lock; it exists because three callers
// need the same two deletes and forgetting one of them leaves a live id.
func (s *MemoryStore) revokeLocked(sessionID string) (string, bool) {
	id, ok := s.sharedBy[sessionID]
	if !ok {
		return "", false
	}
	delete(s.sharedBy, sessionID)
	delete(s.shares, id)
	return id, true
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
		cp.Output = make([]uhp.OutputItem, len(t.Output))
		for i, item := range t.Output {
			cp.Output[i] = copyOutputItem(item)
		}
	}
	cp.Artifacts = copySlice(t.Artifacts)
	cp.IgnoredFields = copySlice(t.IgnoredFields)
	// Each item is a byte slice, so copying the outer slice alone would still
	// hand a reader the writer's bytes. Nothing mutates a stored item today —
	// they are written once at creation — but this store's contract is that a
	// reader cannot reach what it did not write, and an exception that holds
	// only while nobody mutates is one nobody will remember.
	if t.InputItems != nil {
		cp.InputItems = make([]json.RawMessage, len(t.InputItems))
		for i, item := range t.InputItems {
			cp.InputItems[i] = append(json.RawMessage(nil), item...)
		}
	}
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
func copyOutputItem(item uhp.OutputItem) uhp.OutputItem {
	if item.Content != nil {
		content := make([]uhp.ContentPart, len(item.Content))
		for i, part := range item.Content {
			part.Annotations = copySlice(part.Annotations)
			content[i] = part
		}
		item.Content = content
	}
	// A reasoning summary is an array of objects, so each entry needs the same
	// deep copy an item's metadata gets: a shallow copy would hand two callers
	// the same map, and this store's whole contract is that a reader cannot
	// reach what it did not write.
	if item.Summary != nil {
		summary := make([]map[string]any, len(item.Summary))
		for i, part := range item.Summary {
			summary[i] = copyAnyMap(part)
		}
		item.Summary = summary
	}
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
		if all[i].CreatedAt != all[j].CreatedAt {
			return all[i].CreatedAt > all[j].CreatedAt
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
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		// Arrival, not id: see the field's comment. A task with no recorded
		// arrival sorts first and then by id, which is the old behaviour and
		// reachable only for a task this store did not create.
		if a, b := s.arrived[out[i].ID], s.arrived[out[j].ID]; a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
