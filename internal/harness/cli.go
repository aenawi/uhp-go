package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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

	Name   string
	Vendor string
	Binary string

	// Models is the configured model list. It is a *fallback*, consulted only
	// when the CLI cannot be asked — see ModelsArgs and models().
	Models       []string
	Capabilities []domain.Capability

	// ModelsArgs is argv that makes this CLI print the models it can actually
	// serve, and ParseModels turns that output into ids. Nil means this CLI
	// has no such command and the configured list is all there is.
	//
	// Both are filled in only where the command has been run against the real
	// binary. Issue #12: four of the five configured model lists named models
	// that do not exist, because they were written from memory.
	ModelsArgs  []string
	ParseModels func(stdout string) []string

	// Prompt selects how Input reaches the CLI. See PromptMode.
	Prompt PromptMode

	// BuildArgs returns argv (excluding the binary). Under PromptStdin it must
	// not include the prompt.
	BuildArgs func(req RunRequest) ([]string, error)

	// ParseLine turns one line of stdout into zero or more updates.
	ParseLine func(line string) []RunUpdate

	// MCPArgs returns argv pointing the CLI at a generated MCP configuration
	// file. Nil means this runtime has no per-run MCP mechanism, and a harness
	// configured with MCP servers is refused rather than run without them.
	MCPArgs func(configPath string) []string

	// DisallowArgs returns argv hard-blocking the named tools. Nil means the
	// runtime cannot block a tool, and the restriction is conveyed to the
	// agent as a standing instruction instead — never dropped (§4.3).
	DisallowArgs func(tools []string) []string

	// SkillArgs returns argv loading already-materialized skill folders. Nil
	// means the runtime has no native mechanism; the folders are still written
	// where the agent can read them and named in a standing instruction.
	SkillArgs func(dirs []string) []string

	proc *process

	healthMu sync.Mutex
	health   string
	healthAt time.Time

	modelsMu         sync.Mutex
	modelsCache      []string
	modelsAt         time.Time
	modelsRefreshing bool
}

// healthTTL bounds how stale a cached health result may be.
const healthTTL = 30 * time.Second

// modelsTTL bounds how stale a discovered model list may be. Longer than
// healthTTL because the answer changes when someone logs in to a provider or
// upgrades a CLI, not from second to second, and asking costs a fork.
const modelsTTL = 5 * time.Minute

// modelQueryTimeout bounds one "list your models" call. Across repeated runs
// of the four CLIs that can answer, the slowest single call was `opencode
// models` cold at 2.8s and 1.5s warm, so this leaves roughly two cold starts
// of headroom while staying short enough that a wedged CLI cannot hold the
// first discovery request open. Overrunning it is not fatal: the configured
// list stands, and the next call after the TTL asks again.
const modelQueryTimeout = 5 * time.Second

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
//
// It adds `cancellation` to whatever the declaration claimed, because that one
// is the shared runner's rather than any CLI's: every harness is started in its
// own process group and stopped by killing it, and no declaration can opt out
// of a mechanism it does not implement. Adding it here rather than repeating it
// five times is what stops a sixth harness from forgetting the line — and now
// that the router refuses a cancel for a harness that does not advertise it,
// forgetting it would refuse a cancel that in fact works.
func (h *CLIHarness) Build() *CLIHarness {
	h.proc = newProcess(h.Binary, h.Prompt, h.argsFor, h.ParseLine)
	if !domain.HasCapability(h.Capabilities, domain.CapCancellation) {
		h.Capabilities = append(h.Capabilities, domain.CapCancellation)
	}
	return h
}

// argsFor is BuildArgs plus whatever argv delivers this harness's own
// configuration to the runtime.
//
// It is composed here, once, rather than in each declaration: a harness that
// forgot to pass its own disabled-tool list would leave the operator believing
// a tool is off when it is not, which Harnesses §4.3 calls the worst outcome.
// A hook is only consulted when the run actually carries something for it.
func (h *CLIHarness) argsFor(req RunRequest) ([]string, error) {
	args, err := h.BuildArgs(req)
	if err != nil {
		return nil, err
	}
	if h.MCPArgs != nil && req.McpConfigPath != "" {
		args = append(args, h.MCPArgs(req.McpConfigPath)...)
	}
	if h.DisallowArgs != nil && len(req.DisabledTools) > 0 {
		args = append(args, h.DisallowArgs(req.DisabledTools)...)
	}
	if h.SkillArgs != nil && len(req.SkillDirs) > 0 {
		args = append(args, h.SkillArgs(req.SkillDirs)...)
	}
	return args, nil
}

// models returns the model ids this harness can actually serve: the CLI's own
// answer where the CLI can be asked, and the configured list only otherwise.
//
// Configuration alone was not good enough. Every model id in this repository
// was originally written from memory, and four of the five lists named models
// that do not exist — `grok-4.1` was rejected at the CLI as an unknown id, and
// `auto` for opencode came back as "Model not found: auto/.". Because Run
// validates against the advertised list before dispatch, a wrong list passes
// this server's own check and then fails at the CLI: the worst of both. §3.1
// says the same thing from the client's side — "A server MUST compute
// `available`, not assert it. Listing a model as available and then failing
// the task is the worst outcome for a client."
//
// A CLI that cannot be reached leaves configuration standing rather than
// blanking the catalogue, so a provider hiccup does not empty /v1/models.
//
// The first call blocks, because the first answer has to be the real one —
// serving the configured guess even once is the bug this exists to stop.
// Every call after that is served from cache and refreshes in the background
// when the cache goes stale. That asymmetry is deliberate: validateModel is on
// the task-submission path, and the client that happens to send the request
// which crosses the TTL should not be the one that waits for a fork.
func (h *CLIHarness) models() []string {
	if h.ModelsArgs == nil || h.ParseModels == nil {
		return h.Models
	}

	h.modelsMu.Lock()
	defer h.modelsMu.Unlock()

	if h.modelsAt.IsZero() {
		h.storeModels(h.queryModels())
		return h.modelsCache
	}
	if time.Since(h.modelsAt) >= modelsTTL && !h.modelsRefreshing {
		h.modelsRefreshing = true
		go h.refreshModels()
	}
	return h.modelsCache
}

// refreshModels re-asks the CLI without the lock held, so nothing else waits on
// it. One runs at a time: modelsRefreshing is what stops a burst of requests
// arriving on a stale cache from forking the CLI once each.
func (h *CLIHarness) refreshModels() {
	discovered := h.queryModels()

	h.modelsMu.Lock()
	defer h.modelsMu.Unlock()
	h.storeModels(discovered)
	h.modelsRefreshing = false
}

// storeModels records an answer, falling back to configuration when the CLI
// produced none. The caller holds modelsMu.
//
// The cache is replaced rather than appended to, which is what makes it safe
// for models() to hand the slice out: a reader holding the previous one keeps
// reading a list nobody is writing to.
func (h *CLIHarness) storeModels(discovered []string) {
	h.modelsCache = h.Models
	if len(discovered) > 0 {
		h.modelsCache = discovered
	}
	// Stamped even when the query failed, so a CLI that is down is retried
	// once per TTL rather than on every request.
	h.modelsAt = time.Now()
}

// queryModels asks the CLI to enumerate its models, returning nil if it cannot.
//
// An unreachable binary is not asked at all: status() has already spawned it
// once and cached the answer, so checking is free and forking a binary that is
// not installed is not.
func (h *CLIHarness) queryModels() []string {
	if h.status() != domain.HarnessReady {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), modelQueryTimeout)
	defer cancel()
	out, err := h.proc.capture(ctx, h.ModelsArgs)
	if err != nil {
		return nil
	}
	return h.ParseModels(out)
}

// DefaultModel is the model used when a task omits one: the first advertised.
func (h *CLIHarness) DefaultModel() string { return firstModel(h.models()) }

func firstModel(models []string) string {
	if len(models) == 0 {
		return ""
	}
	return models[0]
}

func (h *CLIHarness) Info() domain.Harness {
	// One read of the list, not one for `models` and another for the default.
	// Between two reads the cache can expire and come back different, and a
	// `defaultModel` that is absent from the `models` printed beside it is a
	// discovery document contradicting itself.
	models := h.models()
	return domain.Harness{
		ID:             h.ID,
		Object:         "harness",
		Name:           h.Name,
		Base:           h.Base,
		BaseLabel:      h.Name,
		DefaultModel:   firstModel(models),
		SystemPrompt:   "",
		McpServers:     []domain.McpServer{},
		Skills:         []domain.Skill{},
		DisabledTools:  []string{},
		MaxStep:        nil,
		TimeoutSeconds: nil,
		CreatedAt:      startedAtMillis,
		Models:         models,
		Capabilities:   h.Capabilities,
		Status:         h.status(),
	}
}

// Delivery reports what this runtime enforces natively, derived from which
// hooks the declaration filled in rather than asserted separately — so a
// harness cannot claim a mechanism it did not wire up.
func (h *CLIHarness) Delivery() Delivery {
	return Delivery{
		MCPServers: h.MCPArgs != nil,
		ToolBlock:  h.DisallowArgs != nil,
		Skills:     h.SkillArgs != nil,
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
//
// An empty catalogue is also allowed, and deliberately so: it means neither
// the CLI nor configuration could name a single model, so this server knows
// nothing about what the runtime serves. Refusing every model on that basis
// would be asserting knowledge it does not have — the same mistake in the
// other direction — and it would refuse requests that in fact work. Nothing is
// advertised, so nothing is promised, and the CLI answers in its own words.
func (h *CLIHarness) validateModel(model string) error {
	models := h.models()
	if model == "" || len(models) == 0 {
		return nil
	}
	for _, m := range models {
		if m == model {
			return nil
		}
	}
	return fmt.Errorf("%w %q for harness %q (supported: %v)", ErrUnsupportedModel, model, h.ID, models)
}

// passthroughParseLine treats each line of stdout as an incremental delta.
// Used by every CLI that emits plain text rather than structured events.
func passthroughParseLine(line string) []RunUpdate {
	return []RunUpdate{{Type: UpdateDelta, Delta: line + "\n"}}
}

// harnessFailure is the terminal update for a run whose CLI reported a problem
// in its own output rather than by its exit code.
//
// Three of the five need one. pi exits 0 after printing an error; claude exits
// 1 but writes the reason to stdout and leaves stderr empty; opencode exited 0
// on 1.14.41 and exits 1 on 1.18.21 (issue #13, verified by execution on both).
// That last one is why this does not hang on the exit code: an exit code says a
// run failed and cannot say why, so the reason is worth lifting out of the
// CLI's own output whether or not the code agrees. Both updates are sent and
// the supervisor keeps the first, which is the parsed one — see
// TestOpenCodeErrorBeatsTheExitCode.
//
// So the shape lives here rather than three times. The part each adapter keeps
// is the only part that differs: which field of its own event schema holds the
// words. What is shared is that the words are prefixed with the harness they
// came from, and that a failure with nothing to say still says something,
// because "the run failed" with an empty reason reads to a client as a bug in
// this server.
func harnessFailure(base, message string) RunUpdate {
	if message = strings.TrimSpace(message); message == "" {
		message = "the run failed without reporting a reason"
	}
	return RunUpdate{Type: UpdateFailed, Err: fmt.Errorf("harness: %s: %s", base, message)}
}
