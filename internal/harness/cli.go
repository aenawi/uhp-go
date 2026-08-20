package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aenawi/uhp-go/internal/domain"
)

// ErrUnsupportedModel is returned when a request names a model the harness
// does not advertise. The router maps it to a 400 rather than dispatching and
// letting the CLI fail in its own words minutes later.
var ErrUnsupportedModel = errors.New("harness: unsupported model")

// PromptMode says how the user's prompt reaches the CLI. It exists because
// the safe answer is not the same for every CLI, and the difference is only
// discoverable by running them:
//
//	claude   `-p -- <prompt>` is safe; a bare `-p <prompt>` is not
//	codex    `exec -- <prompt>` is safe; stdin also works
//	grok     `--` does NOT work; `-p=<prompt>` is the only safe argv form,
//	         and grok does not read a prompt from stdin at all
//	pi       `--` does NOT work; stdin is the only safe form
//
// A blanket "put -- before the prompt" rule would leave grok and pi injectable
// while appearing to fix them.
type PromptMode string

const (
	// PromptStdin writes the prompt to the child's stdin. It never appears in
	// argv, so nothing it contains can be re-parsed as an option. Preferred.
	PromptStdin PromptMode = "stdin"

	// PromptArgs means BuildArgs places the prompt in argv itself, for a CLI
	// that cannot read one from stdin. BuildArgs is then responsible for using
	// a form the CLI cannot parse as an option, and owes a table test proving
	// a leading-hyphen prompt stays a prompt.
	PromptArgs PromptMode = "args"
)

// CLIHarness is a harness backend declared as data. Everything that differs
// between two CLI-driven harnesses is a field; everything that does not —
// process-group isolation, prompt delivery, model validation, scanner limits,
// guarded sends — lives in the shared runner and therefore cannot be forgotten
// when a sixth harness is added.
type CLIHarness struct {
	// ID is the `chrn_`-prefixed opaque identifier. It is derived
	// deterministically from Base by New, so it survives a restart: clients
	// store harness ids, and regenerating them would break every saved
	// reference on every deploy.
	ID string

	// Base names the runtime — "claude-code", "codex". This is what the
	// operator and the README call the harness, and it is accepted as an
	// alias for ID wherever a harness id is expected.
	Base string

	Name         string
	Vendor       string
	Binary       string
	Models       []string
	Capabilities []domain.Capability

	// Prompt selects how Input reaches the CLI. See PromptMode.
	Prompt PromptMode

	// BuildArgs returns argv (excluding the binary). Under PromptStdin it must
	// not include the prompt.
	BuildArgs func(req RunRequest) ([]string, error)

	// ParseLine turns one line of stdout into zero or more updates.
	ParseLine func(line string) []RunUpdate

	proc *process

	healthMu sync.Mutex
	health   string
	healthAt time.Time
}

// healthTTL bounds how stale a cached health result may be.
const healthTTL = 30 * time.Second

// startedAtMillis stands in for a creation timestamp. These harnesses are
// static configuration rather than rows in a table, so "when this process
// started" is the only honest answer available.
var startedAtMillis = time.Now().UnixMilli()

// NewID derives a stable `chrn_`-prefixed id from a base name.
//
// Deterministic on purpose: clients store harness ids, so minting a random one
// per process would invalidate every saved reference on each restart.
func NewID(base string) string {
	sum := sha256.Sum256([]byte("uhp-go/harness/" + base))
	return "chrn_" + hex.EncodeToString(sum[:])[:32]
}

// Build finalises a declaration into a runnable Adapter.
func (h *CLIHarness) Build() *CLIHarness {
	h.proc = newProcess(h.Binary, h.Prompt, h.BuildArgs, h.ParseLine)
	return h
}

// DefaultModel is the model used when a task omits one: the first advertised.
func (h *CLIHarness) DefaultModel() string {
	if len(h.Models) == 0 {
		return ""
	}
	return h.Models[0]
}

func (h *CLIHarness) Info() domain.Harness {
	return domain.Harness{
		ID:             h.ID,
		Object:         "harness",
		Name:           h.Name,
		Base:           h.Base,
		BaseLabel:      h.Name,
		DefaultModel:   h.DefaultModel(),
		SystemPrompt:   "",
		McpServers:     []domain.McpServer{},
		Skills:         []domain.Skill{},
		DisabledTools:  []string{},
		MaxStep:        nil,
		TimeoutSeconds: nil,
		CreatedAt:      startedAtMillis,
		Models:         h.Models,
		Capabilities:   h.Capabilities,
		Status:         h.status(),
	}
}

// status reports whether the harness can actually run right now.
//
// Every adapter used to return "ready" unconditionally, so discovery
// advertised harnesses whose CLI was not even installed. The health check is
// cached because discovery is called often and spawning five processes per
// request to ask their versions is not free.
func (h *CLIHarness) status() string {
	h.healthMu.Lock()
	defer h.healthMu.Unlock()
	if time.Since(h.healthAt) < healthTTL {
		return h.health
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := h.proc.healthCheck(ctx); err != nil {
		h.health = domain.HarnessUnavailable
	} else {
		h.health = domain.HarnessReady
	}
	h.healthAt = time.Now()
	return h.health
}

// Available reports whether this harness can serve the given model right now.
// Harnesses §3.1: "A server MUST compute `available`, not assert it. Listing a
// model as available and then failing the task is the worst outcome for a
// client."
func (h *CLIHarness) Available(model string) bool {
	if h.status() != domain.HarnessReady {
		return false
	}
	return h.validateModel(model) == nil
}

func (h *CLIHarness) HealthCheck(ctx context.Context) error { return h.proc.healthCheck(ctx) }

// Run validates the request against what this harness advertises, then starts
// the subprocess. Validation is here rather than in each declaration so that a
// harness cannot forget it.
func (h *CLIHarness) Run(ctx context.Context, req RunRequest) (<-chan RunUpdate, error) {
	if err := h.validateModel(req.Model); err != nil {
		return nil, err
	}
	return h.proc.run(ctx, req)
}

func (h *CLIHarness) Cancel(ctx context.Context, taskID string) error {
	return h.proc.cancelTask(ctx, taskID)
}

// validateModel rejects a model this harness does not advertise. An empty
// model means "the harness default", which is always allowed.
func (h *CLIHarness) validateModel(model string) error {
	if model == "" {
		return nil
	}
	for _, m := range h.Models {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("%w %q for harness %q (supported: %v)", ErrUnsupportedModel, model, h.ID, h.Models)
}

// passthroughParseLine treats each line of stdout as an incremental delta.
// Used by every CLI that emits plain text rather than structured events.
func passthroughParseLine(line string) []RunUpdate {
	return []RunUpdate{{Type: UpdateDelta, Delta: line + "\n"}}
}
