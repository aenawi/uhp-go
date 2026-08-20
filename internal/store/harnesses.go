package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/aenawi/uhp-go/internal/domain"
)

// FileHarnesses persists the harnesses a client created over the API as one
// JSON document on disk.
//
// Durability is the point, not an optimisation: a client stores a harness id
// and comes back to it days later, and a set that empties on every deploy
// would answer with a 404 for something it was told exists.
//
// One file is enough for a set that changes at human speed and is read on
// every discovery call. It loads once into memory, so reads never touch the
// disk, and every mutation is written through atomically — a temporary file
// and a rename — so an interrupted write leaves the previous document rather
// than a truncated one. The richer engine tracked in issue #15 replaces this
// by implementing the same interface.
type FileHarnesses struct {
	mu   sync.RWMutex
	path string
	byID map[string]domain.HarnessConfig
}

// NewFileHarnesses opens (or creates) the document at path and loads it.
//
// A missing file is an empty set, not an error: the first start of a server
// has no configured harnesses and that is a valid state. A file that exists
// but cannot be parsed IS an error — continuing from an empty set would
// silently discard a deployment's entire configuration on the next write.
func NewFileHarnesses(path string) (*FileHarnesses, error) {
	if path == "" {
		return nil, fmt.Errorf("store: harness store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("store: create harness store directory: %w", err)
	}
	f := &FileHarnesses{path: path, byID: make(map[string]domain.HarnessConfig)}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read harness store: %w", err)
	}
	if len(data) == 0 {
		return f, nil
	}
	var doc harnessDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("store: parse harness store: %w", err)
	}
	for _, h := range doc.Harnesses {
		f.byID[h.ID] = h
	}
	return f, nil
}

// harnessDoc wraps the list so the document has somewhere to grow a version
// field without every existing file becoming unparseable.
type harnessDoc struct {
	Harnesses []domain.HarnessConfig `json:"harnesses"`
}

func (f *FileHarnesses) ListHarnesses(_ context.Context) ([]domain.HarnessConfig, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]domain.HarnessConfig, 0, len(f.byID))
	for _, h := range f.byID {
		out = append(out, copyHarnessConfig(h))
	}
	// Ranging over a map returns a different order every time, and a client
	// that takes "the first harness listed" as its default would then get a
	// different one on successive requests.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *FileHarnesses) GetHarness(_ context.Context, id string) (domain.HarnessConfig, bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	h, ok := f.byID[id]
	if !ok {
		return domain.HarnessConfig{}, false, nil
	}
	return copyHarnessConfig(h), true, nil
}

func (f *FileHarnesses) PutHarness(_ context.Context, cfg domain.HarnessConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous, existed := f.byID[cfg.ID]
	f.byID[cfg.ID] = copyHarnessConfig(cfg)
	if err := f.flush(); err != nil {
		// The write failed, so the in-memory set must not claim it succeeded:
		// a caller told "created" whose harness is gone after a restart is
		// worse off than one told the create failed.
		if existed {
			f.byID[cfg.ID] = previous
		} else {
			delete(f.byID, cfg.ID)
		}
		return err
	}
	return nil
}

// DeleteHarness removes a harness. Deleting one that is not there is not an
// error — the caller's intent is already satisfied.
func (f *FileHarnesses) DeleteHarness(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous, existed := f.byID[id]
	if !existed {
		return nil
	}
	delete(f.byID, id)
	if err := f.flush(); err != nil {
		f.byID[id] = previous
		return err
	}
	return nil
}

// flush writes the whole document atomically. The caller holds the lock.
func (f *FileHarnesses) flush() error {
	ids := make([]string, 0, len(f.byID))
	for id := range f.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	doc := harnessDoc{Harnesses: make([]domain.HarnessConfig, 0, len(ids))}
	for _, id := range ids {
		doc.Harnesses = append(doc.Harnesses, f.byID[id])
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode harness store: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".harnesses-*.json")
	if err != nil {
		return fmt.Errorf("store: create harness store temp file: %w", err)
	}
	name := tmp.Name()
	// 0o600: the document can carry an MCP server's bearer token.
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("store: harness store permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("store: write harness store: %w", err)
	}
	// Sync before the rename: a rename is atomic against a concurrent reader,
	// but not against a machine that loses power with the data still in the
	// page cache.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("store: sync harness store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store: close harness store: %w", err)
	}
	if err := os.Rename(name, f.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("store: replace harness store: %w", err)
	}
	return nil
}

// copyHarnessConfig deep-copies the slices and maps a config carries, so that
// a caller mutating what it read cannot edit storage without going through
// PutHarness.
func copyHarnessConfig(h domain.HarnessConfig) domain.HarnessConfig {
	out := h
	out.McpServers = make([]domain.McpServer, len(h.McpServers))
	for i, m := range h.McpServers {
		copied := m
		if m.Enabled != nil {
			enabled := *m.Enabled
			copied.Enabled = &enabled
		}
		if m.Headers != nil {
			copied.Headers = make(map[string]string, len(m.Headers))
			for k, v := range m.Headers {
				copied.Headers[k] = v
			}
		}
		out.McpServers[i] = copied
	}
	out.Skills = make([]domain.Skill, len(h.Skills))
	for i, s := range h.Skills {
		copied := s
		if s.Enabled != nil {
			enabled := *s.Enabled
			copied.Enabled = &enabled
		}
		copied.Files = append([]domain.SkillFile(nil), s.Files...)
		out.Skills[i] = copied
	}
	out.DisabledTools = append([]string(nil), h.DisabledTools...)
	if h.MaxStep != nil {
		v := *h.MaxStep
		out.MaxStep = &v
	}
	if h.TimeoutSeconds != nil {
		v := *h.TimeoutSeconds
		out.TimeoutSeconds = &v
	}
	// An empty set stays empty rather than becoming a non-nil empty slice the
	// caller cannot distinguish; the transport decides how to render it.
	if len(out.McpServers) == 0 {
		out.McpServers = nil
	}
	if len(out.Skills) == 0 {
		out.Skills = nil
	}
	return out
}
