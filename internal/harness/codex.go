package harness

import (
	"encoding/json"
	"sort"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewCodex declares the Codex CLI harness.
//
// Two things here are only knowable by running the CLI:
//
//   - `--skip-git-repo-check` is required. Without it codex refuses to start
//     outside a trusted git repository ("Not inside a trusted directory"). The
//     router gives each session its own working directory, which is never a
//     git repo, so omitting this makes every codex task fail.
//   - `resume` is a subcommand, not a trailing argument:
//     `codex exec resume [OPTIONS] <SESSION_ID>`, options before positionals.
//
// Issue #34: both were re-run against codex-cli 0.149.0 on 2026-08-23 by
// `make probe-codex`, along with the rest of what this file asserts, and every
// answer still holds. The claims were never marked UNVERIFIED — but they were
// also never dated, and #13 is why that is not the same thing: two of
// opencode's execution-verified claims were true when written and false one
// minor version later, with nothing in the tests to notice. What the probe
// re-confirmed on 0.149.0:
//
//   - the prompt on stdin is read and answered, and codex says so on stderr;
//   - the same string in argv is not a prompt — `codex exec "--help"` prints
//     usage and runs nothing — which is the counterfactual PromptStdin exists
//     for. (`--` does protect codex, unlike grok. It is still not used: stdin
//     keeps the prompt out of argv altogether, which is stronger than escaping
//     it.)
//   - `thread.started` announces an id and `codex exec resume <id>` continues
//     that conversation, while the same turn without it gets a new id and does
//     not recall the first turn;
//   - codex still refuses to start without `--skip-git-repo-check`.
//
// What moved is not in that list, because none of it was ever written down:
// two `agent_message` items in one run with no separator, and a failure whose
// reason is on stdout and was being dropped. Both are in parseCodexLine.
func NewCodex(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("codex"),
		Base:   "codex",
		Name:   "Codex CLI",
		Vendor: "OpenAI",
		Binary: "codex",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapSessions, domain.CapTools,
		},
		// `codex debug models` renders the raw model catalogue as JSON, which
		// is the only place the real slugs appear — `codex --help` names none,
		// and a wrong one is not rejected locally but 400s at the API after
		// the run has started. Verified by execution, and re-run on 0.149.0
		// (2026-08-23), where the same command returned every slug the
		// 2026-08-21 fixture records at the same visibility and priority. Both
		// captures are kept — codexModelsOutput and codexModelsOutput0_149 in
		// models_test.go — and both parse to the same six ids in the same
		// order. See the second for the one entry that moved.
		ModelsArgs:  []string{"debug", "models"},
		ParseModels: parseCodexModels,

		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"exec"}
			if req.NativeSessionID != "" {
				args = append(args, "resume")
			}
			args = append(args, "--json", "--skip-git-repo-check")
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: parseCodexLine,
	}).Build()
}

// codexCatalog is the subset of `codex debug models` this server reads. The
// real document also carries each model's system prompt and reasoning levels,
// none of which a catalogue of ids needs.
type codexCatalog struct {
	Models []struct {
		Slug string `json:"slug"`

		// Visibility is "list" for a model a user may pick and "hide" for
		// codex's internal ones (`gpt-reserve`, `codex-auto-review`). Offering
		// a hidden model would advertise something no client should ask for.
		Visibility string `json:"visibility"`

		// Priority is codex's own ordering, lowest first. The first model this
		// server advertises is what a task naming none gets, so codex's idea
		// of its best model becomes ours rather than whatever order the
		// document happened to arrive in.
		Priority int `json:"priority"`
	} `json:"models"`
}

func parseCodexModels(stdout string) []string {
	var catalog codexCatalog
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		return nil
	}

	listed := make([]int, 0, len(catalog.Models))
	for i, m := range catalog.Models {
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		listed = append(listed, i)
	}
	sort.SliceStable(listed, func(a, b int) bool {
		return catalog.Models[listed[a]].Priority < catalog.Models[listed[b]].Priority
	})

	if len(listed) == 0 {
		return nil
	}
	models := make([]string, 0, len(listed))
	for _, i := range listed {
		models = append(models, catalog.Models[i].Slug)
	}
	return models
}

// codexEvent is the subset of `codex exec --json` this server reads. Captured
// from the real CLI on 0.149.0 (#34); the fixtures are in cli_test.go.
type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		// Type is what separates the answer from everything else wearing the
		// same event name. `item.completed` fires for `agent_message`, for
		// `command_execution`, and for `error`, and only the first is the
		// model talking to the client.
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`

	// Error is where `turn.failed` puts the reason a run could not proceed.
	// The top-level `error` event's own `message` is deliberately absent from
	// this struct rather than decoded and ignored — see parseCodexLine for why
	// that line is not read.
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`

	Usage *struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		CacheWriteTokens  int `json:"cache_write_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
}

func parseCodexLine(line string) []RunUpdate {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	switch {
	case ev.Type == "thread.started" && ev.ThreadID != "":
		// The native session id, which is what makes `codex exec resume`
		// actually resume. Verified by execution on 0.149.0: a second turn
		// asked for a word given only in the first and answered with it, while
		// the same turn without `resume` got a new thread id and said
		// "Unknown".
		return []RunUpdate{{Type: UpdateSessionID, SessionID: ev.ThreadID}}

	case ev.Type == "item.completed" && ev.Item.Type == "agent_message" && ev.Item.Text != "":
		// One event per finished assistant message, carrying that message's
		// whole text. Codex sends several per run — a captured run said
		// "Alpha.", ran a shell command, then said "Gamma." — and they arrive
		// during the run rather than after it, which is what the `streaming`
		// claim rests on.
		//
		// The newline is the separator codex omits and the client needs.
		// Deltas are appended into a single output_text, so passing the text
		// through unchanged answers "Alpha.Gamma.". Same defect and same fix as
		// opencode's text parts, including the same cost: it goes on every
		// message rather than only between them, because ParseLine is stateless
		// and cannot know which message is the last, so every answer ends in a
		// newline it did not have. That is the trade opencode already made, and
		// a trailing newline is the harmless end of it.
		//
		// The item type is checked rather than just the text, so that a future
		// item type carrying a `text` field cannot become answer text by
		// default. On 0.149.0 `command_execution` puts the shell's own stdout
		// in `aggregated_output` and `error` puts its sentence in `message`, so
		// neither reaches this today — but that is the CLI's choice of field
		// name, not a property of this parser.
		return []RunUpdate{{Type: UpdateDelta, Delta: ev.Item.Text + "\n"}}

	case ev.Type == "turn.completed" && ev.Usage != nil:
		return []RunUpdate{{Type: UpdateUsage, Usage: &domain.Usage{
			InputTokens:      ev.Usage.InputTokens,
			OutputTokens:     ev.Usage.OutputTokens,
			TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			CacheReadTokens:  ev.Usage.CachedInputTokens,
			CacheWriteTokens: ev.Usage.CacheWriteTokens,
		}}}

	case ev.Type == "turn.failed" && ev.Error != nil:
		// Issue #34. A run that cannot proceed prints its reason and codex
		// exits 1. The exit code was all a client used to get — "exit status
		// 1", with the sentence naming the actual 400 ("The 'x' model is not
		// supported when using Codex with a ChatGPT account") dropped on the
		// floor. See harnessFailure for why this does not hang on the exit
		// code.
		return []RunUpdate{harnessFailure("codex", ev.Error.Message)}
	}

	// No UpdateModel, and that is a gap rather than a decision (issue #43).
	// Nothing on `codex exec --json` names the model: not thread.started, not
	// turn.started, not turn.completed. So a task that named no model gets the
	// router's guess — `firstModel(parseCodexModels(…))`, the lowest-priority
	// number in codex's own catalogue, which is codex's own idea of its best
	// listed model and therefore a good guess. It stays a guess: the CLI is
	// invoked with no `--model` flag in that case and nothing on its output
	// confirms what it chose. If a later codex names the model on an event,
	// reading it here is a strictly better answer than the catalogue.
	//
	// Everything else — turn.started, item.started, and any event type a later
	// codex adds — is deliberately dropped. Two of those are worth naming,
	// because both look like failures and neither may be treated as one:
	//
	//   - The top-level `{"type":"error","message":…}`. On the captured failure
	//     it carried the same sentence `turn.failed` did, immediately before
	//     it, so reading it adds no words. What it could add is a false
	//     terminal: UpdateFailed fails the task outright, ahead of the exit
	//     code, so an `error` codex emits and then recovers from would kill a
	//     run that was going to succeed. One observation is not enough to rule
	//     that out, and `turn.failed` says in its own name that the turn is
	//     over.
	//   - `item.completed` with an item type of `error`. That one is
	//     demonstrably *not* fatal: the captured run's was "Model metadata for
	//     `bogus-model-xyz` not found. Defaulting to fallback metadata", a
	//     warning about a run codex went on to attempt. It is also the evidence
	//     for the paragraph above — codex has a separate channel for
	//     non-terminal problems, which is a reason to think the top-level
	//     `error` is terminal, and not a reason to bet a client's run on it.
	return nil
}
