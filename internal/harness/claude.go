package harness

import (
	"encoding/json"
	"strings"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
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
		Capabilities: []uhpgo.Capability{
			uhpgo.CapStreaming, uhpgo.CapSessions, uhpgo.CapTools,
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
			//
			// `--strict-mcp-config` is unconditional, and that is the point.
			// `--mcp-config` adds a configuration; it does not replace the set.
			// Without this flag Claude Code also loads the host's own MCP
			// configurations — user scope, the working directory's `.mcp.json`,
			// plugins — so the run's MCP surface is whatever the machine happens
			// to have plus whatever the harness configured. That is a superset
			// the operator never authorised, and it is how a server they
			// explicitly disabled gets contacted anyway (§4.1: a disabled entry
			// "MUST NOT be contacted at all"). Putting it here rather than in
			// MCPArgs covers the case MCPArgs cannot see: a harness configured
			// with *no* MCP servers, whose runs would otherwise inherit every
			// server on the box.
			//
			// Verified by execution 2026-08-23 against Claude Code 2.1.240
			// (#19). A server named only in the working directory's `.mcp.json`
			// was contacted — initialize, tools/list — and its tool advertised
			// to the model, on a run whose `--mcp-config` did not mention it.
			// With this flag the init event listed exactly the one configured
			// server and that server's request log stayed empty. Alone, with no
			// `--mcp-config` beside it, the flag is accepted and the run reports
			// `"mcp_servers":[]`.
			args := []string{
				"-p", "--output-format", "stream-json", "--verbose",
				"--include-partial-messages", "--strict-mcp-config",
			}
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
		// The `tool_use` blocks of an `assistant` message, which is claude
		// asking for the tools before any of them runs. Five narrated for five
		// files written — testdata/steps/claude.jsonl.
		Steps: StepEdgeStart,

		// Both flags were declared from Claude Code's documentation and, unlike
		// grok's and pi's, had never been run against the real binary (#19).
		// They have now, against 2.1.240 on 2026-08-23, by
		// `make probe-claude-delivery` — which does not stop at the spelling,
		// because a flag the CLI accepts and does not act on is the failure
		// worth catching:
		//
		//   - `--disallowedTools Bash` does not merely refuse the call. Bash is
		//     absent from the init event's tool list, so the model is never
		//     offered it and says so when asked to run a command. The same run
		//     without the flag used Bash and returned its output.
		//   - The list is comma-joined rather than space-separated, though
		//     `--help` allows either: the flag is variadic, so a space-separated
		//     list would spread across argv elements. `Bash,Read` removed both.
		//   - `--mcp-config` reaches the server. The generated document's
		//     `Authorization` header arrived on every request, tools/list and
		//     tools/call were served, and the model returned the secret only
		//     that server knows.
		//   - A server the document does not name is not contacted — but only
		//     because of `--strict-mcp-config` in BuildArgs. See there; it is
		//     the half of §4.1 these two flags do not cover on their own.
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

	// Model is what the `system`/`init` line says it is about to run. Read
	// only from that line, and not because the others agree with it — they do
	// not. See parseClaudeLine. UpdateModel is why it is read at all.
	Model string `json:"model"`

	// IsError is the run's own verdict, and it is not implied by anything
	// else on the result event: a failed run still reports
	// `"subtype":"success"`, and Result then holds the reason rather than an
	// answer.
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`

	// Message is the finished assistant message. Only its content blocks are
	// read, and only to count `tool_use` — the model asking for a tool, which
	// is claude's start edge and one step of a `max_step` budget (#72). Its
	// text is deliberately not read: the deltas below already carry the answer,
	// and reading both would publish every answer twice.
	Message struct {
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	} `json:"message"`

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
	if ev.Type == "system" && ev.Subtype == "init" {
		if ev.SessionID != "" {
			updates = append(updates, RunUpdate{Type: UpdateSessionID, SessionID: ev.SessionID})
		}
		// Issue #43. A task that named no model reached claude with no
		// `--model` flag, so the CLI picked its own and this line is where it
		// says which.
		//
		// claude reports two model ids per run and they are not the same
		// string. `make capture-claude` against 2.1.240 on 2026-08-23 produced
		// `"model":"claude-opus-5[1m]"` on this init line and
		// `"model":"claude-opus-5"` on every `assistant` and `message_start`
		// under it — see claudeCapturedInitEvent and
		// claudeCapturedAssistantEvent, which are that run's own two lines.
		// The suffix is Claude Code's 1M-context variant: the init line is the
		// CLI's resolved selection, the messages are the API model that served
		// them, and both are true of the same run.
		//
		// This reads the init line. It is the CLI's own answer to "what am I
		// running", it arrives once and first — so a task reports its model
		// before it produces a word — and it is the id `--model` takes back.
		// The cost is real and worth stating: `claude-opus-5[1m]` is not in
		// the list this server advertises, so validateModel would refuse it if
		// a client sent it back, and a client cannot round-trip what it is
		// told here. That is also the sharpest argument for the rule in
		// applyUpdate that this never overwrites a model the client named —
		// against a request for `claude-opus-5` it would otherwise publish
		// `model_fallback: true` for a run that fell back from nothing.
		if ev.Model != "" {
			updates = append(updates, RunUpdate{Type: UpdateModel, Model: ev.Model})
		}
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

	// One step per tool the model asked for (#72). The `assistant` message
	// carrying `tool_use` blocks is the request, before any of them runs; the
	// `user` message carrying the matching `tool_result` is the finish, and is
	// deliberately not read — a capture of five writes narrates five of each,
	// so counting both would halve every ceiling a client set.
	//
	// Blocks are counted rather than messages, because claude puts several
	// `tool_use` blocks in one message when it calls tools in parallel and each
	// of those is a call the agent made. Establised against ground truth on
	// disk: five files asked for, five files written, five blocks narrated —
	// see testdata/steps/claude.jsonl and TestCapturedToolCallsMatchGroundTruth.
	if ev.Type == "assistant" {
		for _, block := range ev.Message.Content {
			if block.Type == "tool_use" {
				updates = append(updates, RunUpdate{Type: UpdateToolCall})
			}
		}
	}

	// The result event carries the run totals.
	if ev.Type == "result" && ev.Usage != nil {
		updates = append(updates, RunUpdate{Type: UpdateUsage, Usage: &uhp.Usage{
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
