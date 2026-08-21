package harness

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewOpenCode declares the OpenCode harness (`opencode run --format json`).
//
// Issue #13: this invocation used to be the one guess in the set. It has now
// been run against opencode 1.14.41 (2026-08-21), through the same four probes
// the other four harnesses were held to:
//
//   - A prompt on stdin is read and answered, so PromptStdin stands. It was
//     the conservative default; it turns out to also be the correct one.
//   - `opencode run "--help"` prints usage and runs nothing. `message` is a
//     yargs positional and yargs parses a leading hyphen as an option, so a
//     bare argv prompt is injectable exactly as claude's was.
//   - `--` does protect the prompt, unlike grok and pi — but it also swallows
//     every flag after it: `opencode run -- "--help" --model X` sent
//     `--model X` to the model as message text. Argv delivery would therefore
//     mean the prompt must be last, for nothing stdin does not already give.
//   - `--format json` prints one JSON event per line carrying `sessionID`, and
//     `--session <id>` resumes that conversation: a second turn asked for a
//     word given only in the first and answered with it.
//
// `--format json` is not optional. opencode's default renderer writes ANSI
// escapes and a `> build · <model>` banner to stdout, which the passthrough
// parser streamed to clients as answer text, and it prints no session id at
// all, so nothing could ever have been resumed.
//
// `--print-logs` was removed: it is not part of the documented invocation and
// its output was being streamed to clients verbatim as answer text.
func NewOpenCode(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("opencode"),
		Base:   "opencode",
		Name:   "OpenCode",
		Vendor: "sst.dev",
		Binary: "opencode",
		Models: models,
		// `sessions` is claimed again, and only now that both halves exist:
		// parseOpenCodeLine discovers the id from the CLI's own output and
		// BuildArgs passes it back. Before, only the flag was here — the id was
		// never discovered, so the branch was unreachable and every
		// continuation quietly started a new conversation.
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapSessions, domain.CapTools,
		},
		// `opencode models` prints the models this install's configured
		// providers actually expose, which is the only honest source: there is
		// no universal opencode model id, only whatever the operator has
		// logged in to. Verified by execution; it is what caught `auto`, which
		// opencode rejects with "Model not found: auto/.".
		ModelsArgs:  []string{"models"},
		ParseModels: parseOpenCodeModels,

		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"run", "--format", "json"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, "--session", req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: parseOpenCodeLine,
	}).Build()
}

// openCodeEvent is the subset of `opencode run --format json` this server
// reads. Captured from the real CLI: every event carries `sessionID` at the top
// level, answer text arrives as `{"type":"text","part":{"text":…}}`, and a run
// that could not proceed prints `{"type":"error","error":{…}}`.
type openCodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text string `json:"text"`
	} `json:"part"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

func parseOpenCodeLine(line string) []RunUpdate {
	var ev openCodeEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	switch {
	case ev.Type == "step_start" && ev.SessionID != "":
		// The native session id, which is what makes `--session` actually
		// resume. opencode repeats it on every step rather than announcing it
		// once, so this fires once per step of a multi-step run; the value is
		// the same each time and the store writes it idempotently.
		return []RunUpdate{{Type: UpdateSessionID, SessionID: ev.SessionID}}

	case ev.Type == "text" && ev.Part.Text != "":
		// One event per completed text part, carrying that part's whole text
		// rather than a growing snapshot of it, so parts concatenate.
		//
		// The newline is the separator opencode omits and the client needs. A
		// run that interleaves prose with tool calls emits one part per stretch
		// of prose — a captured run produced "Alpha" and "Gamma" around a bash
		// call — and deltas are appended into a single output_text, so passing
		// them through unchanged would answer "AlphaGamma". A newline is what
		// opencode's own renderer prints between those same two parts.
		return []RunUpdate{{Type: UpdateDelta, Delta: ev.Part.Text + "\n"}}

	case ev.Type == "error":
		// opencode exits 0 after printing this, so the shared runner would
		// otherwise report a run that never happened as completed with empty
		// output. The supervisor settles on the first terminal update and
		// drains the rest, so the trailing "completed" is correctly ignored.
		return []RunUpdate{{Type: UpdateFailed, Err: fmt.Errorf("harness: opencode: %s", openCodeErrorMessage(ev))}}
	}

	// Everything else — step_finish, tool_use, and any event type a later
	// opencode adds — is deliberately dropped.
	//
	// step_finish carries tokens, and they are not this run's totals: it fires
	// once per step, and in a captured two-step run the second reported
	// input=124 against the first's 18354. Usage is applied last-write-wins, so
	// emitting it would publish one step's accounting as the whole task's. UHP
	// permits usage to be null; it does not permit it to be wrong. Reporting it
	// honestly needs a per-run accumulator, and ParseLine is stateless and
	// shared by every concurrent run of this harness.
	return nil
}

// openCodeErrorMessage picks the most specific words opencode gave for a
// failure, so the client is told "Model not found: bogus/nope." rather than
// that something went wrong.
func openCodeErrorMessage(ev openCodeEvent) string {
	if ev.Error != nil {
		if msg := strings.TrimSpace(ev.Error.Data.Message); msg != "" {
			return msg
		}
		if name := strings.TrimSpace(ev.Error.Name); name != "" {
			return name
		}
	}
	return "the run failed without reporting a reason"
}

// parseOpenCodeModels reads `opencode models`, which prints one id per line.
//
// A line only counts as a model if it has the shape opencode's own `--model`
// takes — `provider/model`, no spaces. Without that check a banner, a prompt
// to log in, or an error message would each be advertised as a model, and
// `/v1/models` would publish it as available.
func parseOpenCodeModels(stdout string) []string {
	var models []string
	for _, line := range strings.Split(stdout, "\n") {
		id := strings.TrimSpace(line)
		if id == "" || strings.ContainsAny(id, " \t") || !strings.Contains(id, "/") {
			continue
		}
		models = append(models, id)
	}
	return models
}
