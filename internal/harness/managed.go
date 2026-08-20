package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/aenawi/uhp-go/internal/domain"
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
func (m *Managed) Info() domain.Harness {
	h := domain.Harness{
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
		Status:         domain.HarnessUnavailable,
	}
	if h.Skills == nil {
		h.Skills = []domain.Skill{}
	}
	if h.DisabledTools == nil {
		h.DisabledTools = []string{}
	}
	h.Models = []string{}
	h.Capabilities = []domain.Capability{}

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
	if info.Status != domain.HarnessReady {
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

// Run applies the harness's own configuration to the request and delegates.
//
// Two things are applied here rather than left to the caller, because the
// caller has no way to know they were configured: the default model, and the
// system prompt. Harnesses §2 defines `systemPrompt` as "additional standing
// instructions", and prepending it to the input is how a standing instruction
// reaches an agent through a CLI that has no separate channel for one. Storing
// it and never delivering it would be the silent drop §4.3 calls the worst
// outcome, one field over.
//
// Skills, MCP servers and disabled tools are stored and returned but not yet
// delivered to the agent; that is issue #4, and until it lands this server
// does not claim conformance class `full`.
func (m *Managed) Run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error) {
	if m.base == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoBase, m.cfg.Base)
	}
	if req.Model == "" {
		req.Model = m.cfg.DefaultModel
	}
	if m.cfg.SystemPrompt != "" {
		req.Input = m.cfg.SystemPrompt + "\n\n" + req.Input
	}
	return m.base.Run(ctx, req)
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
func withoutCredentials(servers []domain.McpServer) []domain.McpServer {
	out := make([]domain.McpServer, len(servers))
	for i, m := range servers {
		m.Auth = ""
		out[i] = m
	}
	return out
}
