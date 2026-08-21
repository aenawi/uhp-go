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
			// Minimal mode: skip hooks, LSP and plugins, which a router never wants.
			return append(args, "--bare"), nil
		},
		ParseLine: parseClaudeLine,

		// UNVERIFIED — both flags are documented for Claude Code, but unlike
		// grok's and pi's they have not been run against the real binary here.
		// If either is wrong the failure is loud (the CLI rejects the flag and
		// the task fails to start) rather than silent, which is the right
		// direction for an unverified claim: a harness that appeared to run
		// with its tools un-blocked would be worse.
		//
		// They are no longer the only unverified claims here. Issue #14 added
		// a second, of a worse kind — see parseClaudeLine.
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
	// UNVERIFIED, and of a worse kind than the flags above, so it is worth
	// being exact about. This replaced a path that had been captured from a
	// live run with one that has not: no logged-in Claude Code was reachable
	// when issue #14 was implemented, so the envelope and the event inside it
	// were read out of the 2.1.238 binary instead (`strings` carries both,
	// including the literal example quoted in cli_test.go). The flag itself
	// *was* run — the CLI accepted it and objected only to the login.
	//
	// If the shape is wrong the failure is silent: no deltas match, the run
	// completes, and the client is handed an empty answer. That is the
	// direction an unverified claim should never fail in, and the reason it is
	// tolerated here is that the CI conformance gate measures this harness on
	// every build — an empty answer fails T-02 and S-01 loudly. The first
	// green gate run is what turns this comment into a verified one; until
	// then the tests below only prove the parser matches the fixture, and the
	// fixture came from the same reading.
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
