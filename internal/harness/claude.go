package harness

import (
	"encoding/json"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewClaude declares the Claude Code harness (`claude -p --output-format
// stream-json`). The prompt goes over stdin: verified by execution that
// `claude -p "--help"` prints usage, while the same string on stdin is
// answered as a prompt.
func NewClaude(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("claude-code"),
		Base:   "claude-code",
		Name:   "Claude Code",
		Vendor: "Anthropic",
		Binary: "claude",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapSessions, domain.CapCancellation, domain.CapTools,
		},
		// No ModelsArgs: Claude Code is the one harness here that cannot
		// enumerate its own models. `claude --help` has no listing command and
		// no subcommand prints one; an unknown `--model` is a warning rather
		// than a refusal, so probing tells you nothing either. The configured
		// list is therefore all there is, which makes keeping it current a
		// standing obligation rather than a one-off fix — as of 2026-08-21
		// `claude-sonnet-4.6` and `claude-opus-4.6` are recognised but retired,
		// which the CLI says out loud when they are used.
		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			// `--verbose` is mandatory: Claude Code rejects
			// `--print --output-format=stream-json` without it.
			//
			// `--include-partial-messages` is what makes the stream
			// progressive, and issue #14 is that it was missing. Without it
			// stream-json emits one `assistant` event per *finished* message,
			// so an answer that is one message is one event delivered when the
			// run is already over — indistinguishable at the socket from a
			// harness that buffers, which is the deployment error Streaming §1
			// exists to catch. Accepted by Claude Code 2.1.238 (verified by
			// execution: the CLI ran with the flag and complained about the
			// login, not the option).
			args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, "--resume", req.NativeSessionID)
			}
			// No `--bare`. It was here for the half of its description a router
			// does want — skip hooks, LSP and plugins — and shipped the other
			// half unread: under `--bare` "Anthropic auth is strictly
			// ANTHROPIC_API_KEY or apiKeyHelper via --settings (OAuth and
			// keychain are never read)". A Pro or Max subscription is OAuth, so
			// every subscription user got `{"is_error":true,"result":"Not logged
			// in · Please run /login"}` and, because no line of that carries a
			// text_delta, an empty answer with a success — the same silent path
			// as #32, reached through the argv instead of the parser.
			//
			// Verified by execution 2026-08-23 against Claude Code 2.1.240, one
			// shell, one minute apart: with the flag, is_error=true and "Not
			// logged in"; without it, is_error=false, "OK", two text deltas.
			//
			// If the isolation is wanted back it has to be built from parts that
			// leave auth alone — `--settings` and `--strict-mcp-config` — not
			// from the flag that bundles auth in.
			return args, nil
		},
		ParseLine: parseClaudeLine,

		// UNVERIFIED — both flags are documented for Claude Code, but unlike
		// grok's and pi's they have not been run against the real binary here.
		// If either is wrong the failure is loud (the CLI rejects the flag and
		// the task fails to start) rather than silent, which is the right
		// direction for an unverified claim: a harness that appeared to run
		// with its tools un-blocked would be worse.
		//
		// They are now the only unverified claims here: the delta shape, which
		// was the worse kind because it failed silently, is checked off the
		// wire by `make capture-claude`. These two are tracked in #19.
		MCPArgs: func(configPath string) []string {
			return []string{"--mcp-config", configPath}
		},
		DisallowArgs: func(tools []string) []string {
			return []string{"--disallowedTools", strings.Join(tools, ",")}
		},
	}).Build()
}

// claudeStreamEvent is the subset of Claude Code's stream-json schema we need.
//
// The stream opens with {"type":"system","subtype":"init","session_id":"…"},
// carries the answer as `stream_event` envelopes, and closes with a result
// event carrying usage totals. Captured from the real CLI, except for the
// envelope's contents — see the fixture comment in cli_test.go for exactly
// which line came from where.
type claudeStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`

	// IsError is the run's own verdict, and it is not implied by anything
	// else on the result event: a failed run still reports
	// `"subtype":"success"`, and Result then holds the reason rather than an
	// answer.
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`

	// Event is the raw Anthropic Messages API streaming event, carried
	// verbatim inside a `stream_event` envelope. Only the text deltas of a
	// content block are read: `thinking_delta` is the model's private working
	// and `input_json_delta` is a tool call's arguments, and publishing either
	// as output_text would tell the client they were part of the answer.
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`

	Usage *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func parseClaudeLine(line string) []RunUpdate {
	var ev claudeStreamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	var updates []RunUpdate

	// Emitted once, from the init event, rather than from every line that
	// happens to repeat the id.
	if ev.Type == "system" && ev.Subtype == "init" && ev.SessionID != "" {
		updates = append(updates, RunUpdate{Type: UpdateSessionID, SessionID: ev.SessionID})
	}

	// The answer, a fragment at a time. The finished `assistant` message that
	// follows these is deliberately not read as well: `--include-partial-
	// messages` adds the deltas, it does not replace the message, so a parser
	// that took both would publish every answer twice. ParseLine is stateless
	// and shared by every concurrent run of this harness, so "deltas if any
	// arrived, otherwise the message" is not available to it — and of the two,
	// the deltas are the half that makes the stream progressive.
	//
	// VERIFIED against Claude Code 2.1.240 on 2026-08-23, and worth saying how,
	// because for two issues it was not. This shape was read out of the 2.1.238
	// binary with `strings` rather than captured, no logged-in CLI being
	// reachable at the time, and the CI conformance gate that was supposed to
	// catch a wrong reading never ran once (#32, 60135aa). If the shape were
	// wrong the failure would be silent — no deltas match, the run completes,
	// the client is handed "" — which is the one direction an unverified claim
	// must never fail in.
	//
	// `make capture-claude` now runs the shipped invocation against a logged-in
	// CLI and checks this shape off the wire, alongside the four other things
	// the adapter assumes. The reading was correct: the captured envelope
	// matches key for key. It stays a standing obligation rather than a settled
	// fact, though — `go test` still cannot reach a logged-in CLI, so the probe
	// is a maintainer's command, to be run after every Claude Code upgrade.
	if ev.Type == "stream_event" &&
		ev.Event.Type == "content_block_delta" &&
		ev.Event.Delta.Type == "text_delta" &&
		ev.Event.Delta.Text != "" {
		updates = append(updates, RunUpdate{Type: UpdateDelta, Delta: ev.Event.Delta.Text})
	}

	// The result event carries the run totals.
	if ev.Type == "result" && ev.Usage != nil {
		updates = append(updates, RunUpdate{Type: UpdateUsage, Usage: &domain.Usage{
			InputTokens:      ev.Usage.InputTokens,
			OutputTokens:     ev.Usage.OutputTokens,
			TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			CacheReadTokens:  ev.Usage.CacheReadTokens,
			CacheWriteTokens: ev.Usage.CacheCreationTokens,
		}})
	}

	// A failed run is already loud — claude exits 1, so the shared runner
	// fails the task either way. What it is not is informative: claude writes
	// the reason to stdout as part of this event and leaves stderr empty, so
	// without this the client is told "exit status 1" and nothing else. Read
	// after the usage above so a failed run still reports what it spent.
	// `result` is where claude puts its own words for a failure, so the client
	// is told "Not logged in · Please run /login" rather than that something
	// went wrong.
	if ev.Type == "result" && ev.IsError {
		updates = append(updates, harnessFailure("claude", ev.Result))
	}

	return updates
}
