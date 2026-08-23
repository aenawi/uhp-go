package store

import (
	"context"
	"sort"
	"sync"

	"github.com/aenawi/uhp-go/internal/domain"
)

// MemoryHarnesses keeps created harnesses in memory only.
//
// It exists so that harness management works on a server started with no
// configuration at all, which is what makes the `harness_management` capability
// unconditional. The trade is real and is not hidden: every harness created
// against this store is gone when the process exits, so a client that stored an
// id gets a 404 for it after a restart. `uhpd` says so on startup when it falls
// back to this, and the README says so too — naming the trade is what makes it
// a choice rather than a surprise.
//
// FileHarnesses is the same interface with a document behind it, and is what a
// deployment that has anywhere to write should use.
type MemoryHarnesses struct {
	mu   sync.RWMutex
	byID map[string]domain.HarnessConfig
}

func NewMemoryHarnesses() *MemoryHarnesses {
	return &MemoryHarnesses{byID: make(map[string]domain.HarnessConfig)}
}

func (m *MemoryHarnesses) ListHarnesses(_ context.Context) ([]domain.HarnessConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.HarnessConfig, 0, len(m.byID))
	for _, h := range m.byID {
		out = append(out, copyHarnessConfig(h))
	}
	// Same reason FileHarnesses sorts: a client that takes "the first harness
	// listed" as its default must not get a different one each request.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (m *MemoryHarnesses) GetHarness(_ context.Context, id string) (domain.HarnessConfig, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.byID[id]
	if !ok {
		return domain.HarnessConfig{}, false, nil
	}
	return copyHarnessConfig(h), true, nil
}

func (m *MemoryHarnesses) PutHarness(_ context.Context, cfg domain.HarnessConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[cfg.ID] = copyHarnessConfig(cfg)
	return nil
}

// DeleteHarness removes a harness. Deleting one that is not there is not an
// error — the caller's intent is already satisfied.
func (m *MemoryHarnesses) DeleteHarness(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, id)
	return nil
}
