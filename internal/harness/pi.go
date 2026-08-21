package harness

import (
	"encoding/json"
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewPi declares the Pi harness.
//
// The previous invocation was `pi run <prompt>`, which is wrong twice: pi has
// no `run` subcommand (its usage is `pi [options] [@files...] [messages...]`,
// so "run" was silently prepended to every prompt as a word), and `-p` is
// required for non-interactive mode. Pi also has no option terminator — `pi -p
// -- "--help"` still prints help — so stdin is the only safe delivery.
func NewPi(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:           NewID("pi"),
		Base:         "pi",
		Name:         "Pi",
		Vendor:       "community",
		Binary:       "pi",
		Models:       models,
		Capabilities: []domain.Capability{domain.CapStreaming, domain.CapTools},
		// `pi --list-models` prints the models pi will route to, filtered by
		// which providers have credentials — the same "computed, not asserted"
		// answer §3.1 asks for. Verified by execution; it is what caught
		// `auto`, which pi does not reject but fuzzy-matches to an unrelated
		// provider.
		ModelsArgs:  []string{"--list-models"},
		ParseModels: parsePiModels,

		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			// `--mode json` is not optional, and issue #14 is what it fixes.
			// pi's default text mode prints nothing at all until the run is
			// over — its own `runPrintMode` waits for `session.prompt()` to
			// resolve and then writes the last assistant message — so `pi -p`
			// alone buffers exactly as grok does, whatever `streaming` in the
			// capability list above says. In json mode the same function
			// instead subscribes to the session and writes one JSON line per
			// event as it fires, which includes a `message_update` per chunk
			// of generated text.
			//
			// Read from pi 0.83.0's shipped package rather than from a
			// captured run — `dist/modes/print-mode.js` for what each mode
			// writes and when — because no provider reachable from this
			// machine would answer without hitting a rate limit first. That is
			// weaker evidence than the rest of this file's, and the fixture
			// comment above TestParsePiLine says exactly which line came from
			// where. What a run did settle is the failure path below.
			args := []string{"-p", "--mode", "json"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			return args, nil
		},
		ParseLine: parsePiLine,

		// Both verified against `pi --help`:
		//   --exclude-tools, -xt <tools>  Comma-separated denylist of tool names
		//   --skill <path>                Load a skill file or directory
		// pi is the one runtime here that loads a skill folder itself, so its
		// skills do not need the standing-instruction fallback.
		DisallowArgs: func(tools []string) []string {
			return []string{"--exclude-tools", strings.Join(tools, ",")}
		},
		SkillArgs: func(dirs []string) []string {
			args := make([]string, 0, len(dirs)*2)
			for _, dir := range dirs {
				args = append(args, "--skill", dir)
			}
			return args
		},
	}).Build()
}

// piEvent is the subset of `pi --mode json` this server reads.
//
// pi publishes its whole session event stream, one JSON object per line. Two
// of those events carry an assistant message: `message_update` while it is
// being generated, holding the fragment that just arrived, and `message_end`
// once, holding the finished message. Reading both would publish every answer
// twice, so only the fragments are read — they are the half that makes the
// stream progressive, which is the whole point of the mode.
//
// The lifecycle events here were captured from a real run; `message_update`
// and its inner `text_delta` are declared in pi 0.83.0's own
// `pi-agent-core/dist/types.d.ts` and have not been seen on the wire from this
// machine. See the fixture comment above TestParsePiLine.
type piEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Role string `json:"role"`

		// StopReason is "stop", "length", "toolUse", "aborted" or "error".
		StopReason   string `json:"stopReason"`
		ErrorMessage string `json:"errorMessage"`
	} `json:"message"`

	// AssistantMessageEvent is the model-level event that produced this
	// update: `text_delta` for the answer, `thinking_delta` for the model's
	// private working, `toolcall_delta` for a tool call's arguments. Only the
	// first is the answer; publishing the others as output_text would tell the
	// client they were part of it.
	AssistantMessageEvent *struct {
		Type  string `json:"type"`
		Delta string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

func parsePiLine(line string) []RunUpdate {
	var ev piEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}

	switch {
	case ev.Type == "message_update" && ev.AssistantMessageEvent != nil &&
		ev.AssistantMessageEvent.Type == "text_delta" && ev.AssistantMessageEvent.Delta != "":
		// The answer, a fragment at a time. No separator is added: unlike
		// opencode's whole-part events these are the model's own token
		// boundaries, and inserting anything between them would rewrite the
		// text mid-word.
		return []RunUpdate{{Type: UpdateDelta, Delta: ev.AssistantMessageEvent.Delta}}

	case ev.Type == "message_end" && ev.Message != nil &&
		ev.Message.Role == "assistant" && ev.Message.StopReason == "error":
		// pi exits 0 after printing this. Only its *text* mode turns a failed
		// run into a non-zero exit; in json mode the error is data on the
		// stream and nothing else, so a harness that ignored it would report a
		// run that never happened as completed with empty output. Verified by
		// execution: a run over a provider's per-minute token limit printed
		// this and exited 0.
		//
		// `aborted` is deliberately not treated the same way. That is what pi
		// reports when the run was cancelled, and the shared runner already
		// emits the terminal "cancelled" update for it — failing the task here
		// as well would race the two and could publish a cancellation as a
		// failure.
		return []RunUpdate{harnessFailure("pi", ev.Message.ErrorMessage)}
	}

	// Everything else is dropped, including `turn_end` and `agent_end`, which
	// repeat the failed message verbatim: reporting from all three would tell
	// the client about one failure three times.
	//
	// `session` is dropped too. It carries pi's own session id, and passing it
	// on would be the beginning of session support rather than the whole of
	// it: pi does not advertise `sessions` above, because whether
	// `--session-id` resumes a conversation has not been run against the real
	// binary. Discovering an id this server then refuses to accept back would
	// claim nothing and cost a store write per run.
	//
	// Usage is dropped for the reason opencode's is: pi reports it per
	// message, task_service applies it last-write-wins, and a multi-step run
	// would therefore publish one step's accounting as the whole task's. UHP
	// permits usage to be null; it does not permit it to be wrong.
	return nil
}

// parsePiModels reads the table `pi --list-models` prints:
//
//	provider  model                     context  max-out  thinking  images
//	groq      llama-3.3-70b-versatile   131.1K   32.8K    no        no
//
// The id `pi --model` takes is `provider/id`, so the first two columns are
// joined. The model column may itself contain a slash
// (`meta-llama/llama-4-scout-17b-16e-instruct`), which is why the columns are
// read positionally rather than by splitting an id apart afterwards.
//
// Nothing is read until the header has been seen, and then only lines as wide
// as the header was. With no catalogue to show pi prints prose to the same
// stream — `pi --list-models zzz` answers `No models matching "zzz"` — and the
// first two words of a sentence read as a provider and a model just as well as
// a row does. A message advertised as a model is published by /v1/models as
// available, which is the failure §3.1 calls the worst outcome for a client.
//
// Taking the width from the header rather than hard-coding six also means a
// column added to the table is followed rather than fatal.
func parsePiModels(stdout string) []string {
	var models []string
	width := 0

	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		// The header names its own columns, so it identifies itself.
		if len(fields) >= 2 && fields[0] == "provider" && fields[1] == "model" {
			width = len(fields)
			continue
		}
		if width == 0 || len(fields) != width {
			continue
		}
		models = append(models, fields[0]+"/"+fields[1])
	}
	return models
}
