package harness

import (
	"sort"
	"sync"

	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// Registry resolves a harness id to its adapter and lists what is registered.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
	aliases  map[string]string
}

func NewRegistry() *Registry {
	return &Registry{adapters: make(map[string]Adapter), aliases: make(map[string]string)}
}

// Register adds an adapter under its canonical id, plus any aliases.
//
// A CLIHarness is registered under both its `chrn_` id and its base name, so
// that `{"harness_id": "claude-code"}` keeps working alongside the canonical
// `{"harness_id": "chrn_…"}`. The protocol requires the canonical form on the
// wire; accepting the friendly name as well costs nothing and is what every
// operator will actually type.
func (r *Registry) Register(a Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info := a.Info()
	r.adapters[info.ID] = a
	if info.Base != "" {
		r.aliases[info.Base] = info.ID
	}
}

func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if a, ok := r.adapters[id]; ok {
		return a, true
	}
	if canonical, ok := r.aliases[id]; ok {
		a, ok := r.adapters[canonical]
		return a, ok
	}
	return nil, false
}

// Resolve returns the canonical harness id for an id or alias.
func (r *Registry) Resolve(id string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.adapters[id]; ok {
		return id, true
	}
	canonical, ok := r.aliases[id]
	return canonical, ok
}

// List returns every registered harness, ordered by id.
//
// The order is deliberate, not incidental. Ranging over the map returned a
// different order on every request, and clients — including the UHP
// conformance suite — routinely take "the first harness listed" as the default,
// so an unordered list hands successive callers different harnesses.
func (r *Registry) List() []uhpgo.Harness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]uhpgo.Harness, 0, len(r.adapters))
	for _, a := range r.adapters {
		out = append(out, a.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
