package harness

import (
	"encoding/json"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewGrok declares the Grok CLI harness.
//
// Grok is the one harness that must put the prompt in argv: it does not read a
// prompt from stdin (`grok -p` with a piped prompt still reports "a value is
// required for '--single <PROMPT>'"). Nor does `--` help, because the prompt is
// the value of `-p` rather than a positional, and the parser rejects a value
// beginning with a hyphen. The attached form `-p=<prompt>` is the only shape
// that carries an arbitrary prompt safely.
//
// Issue #34: all four of those answers were re-run against grok 1.0.5 on
// 2026-08-23 by `make probe-grok` and all four still hold — stdin still
// refused, `-p "--help"` still an argument error, `-p -- "--help"` still the
// same error, `-p=--help` still a prompt. What did not hold was everything
// this adapter said around them, because 1.0.5 has two things the version
// behind it did not:
//
//   - `--output-format streaming-messages-json` prints NDJSON in the Anthropic
//     Messages API wire format, so the answer can be read as events instead of
//     as whatever grok's renderer put on stdout. The old invocation ran grok in
//     its default `plain` mode and passed every line through as answer text.
//   - `-r/--resume <id>` resumes a conversation, and every line of the stream
//     carries the `session_id` to pass back. `sessions` was withheld for the
//     usual honest reason — nothing discovered an id — and #13 is the precedent
//     for what that costs: opencode's resume was withheld on the same
//     reasoning and turned out to work all along.
func NewGrok(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("grok-cli"),
		Base:   "grok-cli",
		Name:   "Grok CLI",
		Vendor: "xAI",
		Binary: "grok",
		Models: models,
		// `sessions` is claimed only now that both halves exist: parseGrokLine
		// discovers the id from the CLI's own output and BuildArgs passes it
		// back. Verified by execution on 1.0.5, and the evidence is `grok
		// export <id>` rather than the model's answer — see the probe, where a
		// second turn *without* `--resume` also produced the first turn's word,
		// having found it by reading the probe's own files off disk.
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapSessions, domain.CapTools,
		},
		// `grok models` prints the models this login can actually use, so the
		// advertised list is computed rather than guessed. Verified by
		// execution; it is what caught `grok-4.1`, which never existed.
		// Re-captured on 1.0.5 (2026-08-23) byte for byte identical to the
		// 2026-08-21 capture — same banner, same default, same two ids.
		ModelsArgs:  []string{"models"},
		ParseModels: parseGrokModels,

		Prompt: PromptArgs,
		BuildArgs: func(req RunRequest) ([]string, error) {
			// `--output-format streaming-messages-json` is not optional, for
			// the same reason opencode's `--format json` is not. grok's default
			// `plain` renderer prints the finished answer and nothing a parser
			// can key on: no session id, no usage, and no way to tell the
			// model's answer from a progress line, all of which the old
			// passthrough streamed to clients verbatim as answer text.
			//
			// `--include-partial-messages` is what makes it progressive, and is
			// issue #14 in its third harness. Without it the same prompt
			// produced exactly three lines — `system`, `assistant`, `result` —
			// one whole message delivered when the run was already over, which
			// is indistinguishable at the socket from a harness that buffers.
			// With it, the answer arrives as `text_delta` events during the run.
			// Verified by execution on 1.0.5, both ways, one minute apart.
			args := []string{
				"-p=" + req.Input,
				"--output-format", "streaming-messages-json",
				"--include-partial-messages",
			}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				// `--resume`, not `--session-id`. They read as near-synonyms in
				// `--help` and do opposite things: `--session-id` names the id
				// for a *new* conversation and refuses one that already exists,
				// so using it to continue would fail every second turn.
				args = append(args, "--resume", req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: parseGrokLine,

		// Verified against `grok --help`: "--disallowed-tools <TOOLS>  Built-in
		// tools to remove (comma-separated)". A real block, so the router does
		// not fall back to asking the model nicely. Still spelled this way on
		// 1.0.5, where `--deny <RULE>` now carries `--disallowedTools` as a
		// compat alias — a different flag with a different grammar, not a
		// rename of this one.
		DisallowArgs: func(tools []string) []string {
			return []string{"--disallowed-tools", strings.Join(tools, ",")}
		},

		// No MCPArgs: grok configures MCP through its own `grok mcp`
		// subcommand, which writes global state rather than taking a per-run
		// file. Declaring one here would advertise support this server cannot
		// deliver for a single turn, which §4.1 forbids.
	}).Build()
}

// grokStreamEvent is the subset of `grok --output-format
// streaming-messages-json --include-partial-messages` this server reads.
//
// grok's own `--help` calls this format "NDJSON in the Anthropic Messages API
// wire format", and against claude's stream-json it is that claim key for key:
// the same `system`/`init` opener, the same `stream_event` envelope around a
// verbatim Messages API event, the same `result` closer.
//
// It duplicates claudeStreamEvent rather than sharing it, and the honest
// version of that reasoning distinguishes two halves:
//
//   - The **envelope** is each CLI's own, and the two already disagree where it
//     matters most. On a failed run claude reports the reason in `result` and
//     keeps `"subtype":"success"`; grok leaves `result` absent, changes the
//     subtype, and puts the reason in an `errors` array claude has no field
//     for. Sharing that would make each CLI's next format change a decision
//     about the other one, which #13 is the standing argument against.
//   - The inner `event` object is **not** claude's format — it is the Anthropic
//     Messages API, which both CLIs name and neither owns. The duplication
//     there is real and the argument above does not cover it. Extracting it,
//     along with the four-field usage mapping the two share, is the right
//     change and is deliberately not made here: it edits claude.go, which
//     issue #34 did not probe, and an adapter is not worth refactoring on the
//     strength of a neighbour's verification. Recorded so the next person does
//     not have to rediscover it.
//
// codex is not in this discussion: its usage fields are spelled
// `cached_input_tokens` and `cache_write_input_tokens`, so it shares the shape
// and not the names.
type grokStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`

	// Model is what the `system`/`init` line says it is about to run. It is
	// read only from that line: the `assistant` events repeat the same id one
	// level down in `message.model`, and there is nothing to gain from
	// republishing it per message. See UpdateModel for why this is worth
	// reading at all.
	Model string `json:"model"`

	// IsError is the run's own verdict. grok also exits 1, so this is not what
	// fails the task; it is what puts words on the failure.
	IsError bool     `json:"is_error"`
	Errors  []string `json:"errors"`

	// Event is the raw Anthropic Messages API streaming event. Only a content
	// block's text deltas are read: `thinking_delta` is the model's private
	// working — thirty of them against six of answer in a captured run — and
	// `input_json_delta` is a tool call's arguments.
	Event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`

	// Usage is read only from the `result` event. The identically-shaped one on
	// `message_delta` is a single message's accounting, not the run's — see
	// parseGrokLine.
	Usage *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func parseGrokLine(line string) []RunUpdate {
	var ev grokStreamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	var updates []RunUpdate

	// Emitted once, from the init event, rather than from every line that
	// happens to repeat the id — which here is every line without exception.
	if ev.Type == "system" && ev.Subtype == "init" {
		if ev.SessionID != "" {
			updates = append(updates, RunUpdate{Type: UpdateSessionID, SessionID: ev.SessionID})
		}
		// Issue #43. A task that named no model reached grok with no `--model`
		// flag, so which one it picked is grok's to say — and it says so here,
		// on the same line as the session id. Without this the response can
		// only report the router's advertised default, which is a guess that
		// happens to be right rather than the answer.
		if ev.Model != "" {
			updates = append(updates, RunUpdate{Type: UpdateModel, Model: ev.Model})
		}
	}

	if ev.Type == "stream_event" {
		switch ev.Event.Type {
		case "content_block_delta":
			// The answer, a fragment at a time, passed through unpadded: these
			// are token-level, so " BRA" and "VO" are two halves of one word.
			//
			// The finished `assistant` message that follows them is deliberately
			// not read as well. `--include-partial-messages` adds the deltas, it
			// does not replace the message, so a parser taking both would
			// publish every answer twice.
			if ev.Event.Delta.Type == "text_delta" && ev.Event.Delta.Text != "" {
				updates = append(updates, RunUpdate{Type: UpdateDelta, Delta: ev.Event.Delta.Text})
			}

		case "message_stop":
			// The separator between two assistant messages of one run, and the
			// third time this repository has needed one. A run that interleaves
			// prose with tool calls sends a message per stretch of prose, and no
			// event carries a separator: a captured run said "I'll run that
			// command and tell you what it printed." and then "It printed:
			// `HELLO_PROBE`", which concatenate into "…printed.It printed:…".
			//
			// It hangs on `message_stop` rather than on the text, unlike
			// opencode's, because grok's deltas are token-level — a newline per
			// delta would break every word apart. `content_block_stop` would be
			// the tighter boundary but carries only an `index`, and ParseLine is
			// stateless, so there is no way from here to know which block that
			// index was. The cost is a trailing newline on the answer, and a
			// lone one after any message that had no text in it at all.
			updates = append(updates, RunUpdate{Type: UpdateDelta, Delta: "\n"})
		}
	}

	if ev.Type == "result" {
		// The run's totals. `message_delta` carries the same four field names
		// per message, and reading those instead would be wrong in the silent
		// direction: usage is applied last-write-wins, so a run that called one
		// tool would publish its second message's 166 input tokens as the whole
		// task's against a real total of 19838. UHP permits usage to be null;
		// it does not permit it to be wrong.
		if ev.Usage != nil {
			updates = append(updates, RunUpdate{Type: UpdateUsage, Usage: &domain.Usage{
				InputTokens:      ev.Usage.InputTokens,
				OutputTokens:     ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
				CacheReadTokens:  ev.Usage.CacheReadTokens,
				CacheWriteTokens: ev.Usage.CacheCreationTokens,
			}})
		}

		// Read after the usage, so a failed run still reports what it spent.
		// grok exits 1, so the task fails either way; `errors` is the only
		// place on stdout the reason appears. Joined rather than picking one,
		// because nothing observed says the array holds only ever one.
		if ev.IsError {
			updates = append(updates, harnessFailure("grok-cli", strings.Join(ev.Errors, "; ")))
		}
	}

	return updates
}

// parseGrokModels reads the list `grok models` prints:
//
//	You are logged in with grok.com.
//
//	Default model: grok-4.6
//
//	Available models:
//	  * grok-4.6 (default)
//	  - grok-4.5
//
// Only lines inside the list are read, so the prose above it — which is where
// "you are not logged in" appears — can never become a model id. The starred
// entry is moved to the front, leaving the rest in the order grok printed
// them, because the first model advertised is the one a task that names none
// gets.
func parseGrokModels(stdout string) []string {
	var models []string
	def := -1
	inList := false

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Available models:") {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		isDefault := strings.HasPrefix(line, "* ")
		if !isDefault && !strings.HasPrefix(line, "- ") {
			continue
		}
		id := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[2:]), "(default)"))
		if id == "" {
			continue
		}
		if isDefault {
			def = len(models)
		}
		models = append(models, id)
	}

	if def > 0 {
		// Lifted out and put back at the front, not swapped with whatever was
		// first: a swap would also move that one to the middle of the list.
		promoted := models[def]
		models = append(models[:def], models[def+1:]...)
		models = append([]string{promoted}, models...)
	}
	return models
}
