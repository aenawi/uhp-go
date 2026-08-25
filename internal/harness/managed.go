package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/aenawi/uhp-go/internal/domain"
	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
)

// ErrNoBase is returned when a managed harness names a base this server does
// not have. It is a 502-class condition rather than a bad request: the client
// configured something this server accepted at the time.
var ErrNoBase = errors.New("harness: configured base is not available on this server")

// Managed is a harness created over the API: stored configuration bound to the
// compiled-in base adapter that actually runs the work.
//
// It is an Adapter like any other, so nothing downstream — the task service,
// the supervisor, the streaming layer — needs to know whether a harness came
// from a declaration in main.go or from POST /v1/harnesses.
type Managed struct {
	cfg  domain.HarnessConfig
	base Adapter
}

// NewManaged binds a configuration to its base. A nil base means the base is
// not compiled into this server, which is a state the harness reports rather
// than hides: the configuration is still real, it just cannot run here.
func NewManaged(cfg domain.HarnessConfig, base Adapter) *Managed {
	return &Managed{cfg: cfg, base: base}
}

// Info merges what the client configured with what only the base can answer.
//
// The split is deliberate. Name, prompt and budgets are configuration and come
// back exactly as they were set; models, capabilities and status are computed
// from the base on every read, because a stored copy of "this CLI is ready"
// becomes a lie the moment the CLI is uninstalled.
//
// This is the only way a configuration becomes a harness object, which is why
// the MCP credentials are stripped here rather than by whoever serializes the
// result: a redaction the caller has to remember is one a future caller
// forgets, and Harnesses §4.1 gives no second chance — a credential returned
// once is a credential leaked.
func (m *Managed) Info() uhpgo.Harness {
	h := uhpgo.Harness{
		Harness: uhp.Harness{
			ID:             m.cfg.ID,
			Object:         "harness",
			Name:           m.cfg.Name,
			Base:           m.cfg.Base,
			DefaultModel:   m.cfg.DefaultModel,
			SystemPrompt:   m.cfg.SystemPrompt,
			McpServers:     withoutCredentials(m.cfg.McpServers),
			Skills:         m.cfg.Skills,
			DisabledTools:  m.cfg.DisabledTools,
			MaxStep:        m.cfg.MaxStep,
			TimeoutSeconds: m.cfg.TimeoutSeconds,
			CreatedAt:      m.cfg.CreatedAt,
		},
		Status: uhpgo.HarnessUnavailable,
	}
	if h.Skills == nil {
		h.Skills = []uhp.Skill{}
	}
	if h.DisabledTools == nil {
		h.DisabledTools = []string{}
	}
	h.Models = []string{}
	h.Capabilities = []uhpgo.Capability{}

	if m.base == nil {
		return h
	}
	info := m.base.Info()
	h.BaseLabel = info.BaseLabel
	h.Models = info.Models
	h.Capabilities = info.Capabilities
	h.Status = info.Status
	if h.DefaultModel == "" {
		h.DefaultModel = info.DefaultModel
	}
	return h
}

func (m *Managed) HealthCheck(ctx context.Context) error {
	if m.base == nil {
		return fmt.Errorf("%w: %q", ErrNoBase, m.cfg.Base)
	}
	return m.base.HealthCheck(ctx)
}

// Available reports whether this harness can serve a model right now, which is
// entirely the base's answer: configuration cannot make an uninstalled CLI
// reachable.
func (m *Managed) Available(model string) bool {
	if m.base == nil {
		return false
	}
	if model == "" {
		model = m.cfg.DefaultModel
	}
	if av, ok := m.base.(interface{ Available(string) bool }); ok {
		return av.Available(model)
	}
	// A base that cannot answer for itself is not taken at its word: an
	// unqualified `true` here is the "assert rather than compute" failure
	// Harnesses §3.1 exists to forbid.
	info := m.base.Info()
	if info.Status != uhpgo.HarnessReady {
		return false
	}
	if model == "" {
		return true
	}
	for _, have := range info.Models {
		if have == model {
			return true
		}
	}
	return false
}

// Run applies the harness's default model and delegates.
//
// Only the model is applied here. Everything else a harness carries — the
// system prompt, skill folders, MCP servers, disabled tools — has to be
// written to disk or turned into argv before the process starts, which is the
// router's job and not this wrapper's; see service.prepareRuntime. The model
// stays because it is the one field with nowhere else to go: the request
// reaches the base having already lost track of which harness chose it.
func (m *Managed) Run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error) {
	if m.base == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoBase, m.cfg.Base)
	}
	if req.Model == "" {
		req.Model = m.cfg.DefaultModel
	}
	return m.base.Run(ctx, req)
}

// Delivery forwards the base's answer: a wrapper cannot enforce what the
// runtime underneath it cannot.
func (m *Managed) Delivery() Delivery {
	if d, ok := m.base.(Deliverer); ok {
		return d.Delivery()
	}
	return Delivery{}
}

func (m *Managed) Cancel(ctx context.Context, taskID string) error {
	if m.base == nil {
		return fmt.Errorf("%w: %q", ErrNoBase, m.cfg.Base)
	}
	return m.base.Cancel(ctx, taskID)
}

// withoutCredentials copies the MCP entries with their `auth` removed.
//
// Harnesses §4.1: "A server must never return a resolved credential to a
// client." The copy matters as much as the blanking — clearing the field in
// place would erase the credential this server still has to connect with.
//
// It blanks `auth` and nothing else, which is enough here and is *not* enough
// for a reader with no credential. Headers is a free-form map, and
// writeMcpConfig materialises the resolved `auth` into exactly that map as an
// Authorization header — so an entry can carry a working key with `auth` empty.
// Every caller of this is behind bearer auth. The anonymous share view
// (Sessions §5) does not narrow an McpServer at all; it drops the whole list.
// See service.sharedHarness, and do not relax it to a call to this.
func withoutCredentials(servers []uhp.McpServer) []uhp.McpServer {
	out := make([]uhp.McpServer, len(servers))
	for i, m := range servers {
		m.Auth = ""
		out[i] = m
	}
	return out
}
