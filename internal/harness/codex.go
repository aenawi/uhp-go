package harness

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/aenawi/uhp-go/uhp"
	"github.com/aenawi/uhp-go/uhp/uhpgo"
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
//   - `-c sandbox_mode=workspace-write` is required for the agent to write
//     anything at all. Without it codex's workspace is read-only and every
//     write is refused — and the run still ends on `turn.completed` and exits
//     0, so the refusal reaches a client as a success. That is issue #89, and
//     it is two things: see codexSandboxMode for why this argument and this
//     form, and codexWatch for the reporting hole, which is fixed separately
//     because it swallows every reason a tool is refused and not only this one.
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
		Capabilities: []uhpgo.Capability{
			uhpgo.CapStreaming, uhpgo.CapSessions, uhpgo.CapTools,
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
			args = append(args, "-c", codexSandboxMode)
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: parseCodexLine,
		// `item.started` on a tool item — the model asking, before the tool
		// runs. Five narrated for five files written, on 0.150.1 and under the
		// invocation ADR-0008 settled: testdata/steps/codex.jsonl.
		Steps:    StepEdgeStart,
		NewWatch: func() RunWatch { return &codexWatch{} },
	}).Build()
}

// codexSandboxMode grants the agent write access to the directory the router
// gave the session, and nothing outside it.
//
// ADR-0008 is the decision; this is the mechanism, and the mechanism is
// measured rather than picked. Two forms of it exist on codex-cli 0.150.1 and
// only one of them works on both invocations this adapter builds:
//
//   - `--sandbox workspace-write` is the documented flag, and `codex exec
//     resume` does not accept it: "error: unexpected argument '--sandbox'
//     found". A resumed turn would silently fall back to read-only, which is
//     issue #89 again on every turn after the first.
//   - `-c sandbox_mode=workspace-write` is the same setting as a config
//     override, and `-c` is on both `exec` and `exec resume`. Verified by
//     execution on 2026-08-29: a fresh turn wrote its file, and a resume of
//     that thread wrote another.
//
// So the config form is used on both, and the value is passed exactly as
// measured. `-c` parses the value as TOML and falls back to the raw string when
// that fails, so the unquoted word is what codex actually received in the run
// that worked.
//
// A resume was also measured *without* it, and wrote its file anyway: the
// thread remembers the mode it was started in. It is still passed, because that
// inheritance is undocumented, is not something codex promises, and costs
// nothing to stop depending on — while depending on it puts every continued
// task one codex release away from silently going read-only.
const codexSandboxMode = "sandbox_mode=workspace-write"

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

	case ev.Type == "item.started" && codexToolItems[ev.Item.Type]:
		// One step (#72). `item.started` is the model asking for a tool;
		// `item.completed` is the same tool finishing, and is not counted —
		// counting both would halve every ceiling a client set.
		//
		// The item type is checked rather than the event alone, because
		// `item.started` also fires for `agent_message` and `reasoning`, which
		// are the answer being written rather than a tool being used. A run
		// whose only work was talking would otherwise spend a client's whole
		// step budget on its own prose.
		return []RunUpdate{{Type: UpdateToolCall}}

	case ev.Type == "turn.completed" && ev.Usage != nil:
		return []RunUpdate{{Type: UpdateUsage, Usage: &uhp.Usage{
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
	// Everything else — turn.started, item.started for an item that is not a
	// tool, and any event type a later codex adds — is deliberately dropped.
	// Two of those are worth naming,
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

// The two halves of the refusal signal, and the whole of what this adapter
// claims to recognise.
//
// codex writes tracing lines to stderr as `<timestamp> ERROR <target>:
// <message>`. codexErrorLevel is that level marker; codexWriteRefused is the
// span of the message that names the refusal, from the capture in
// testdata/steps/codex-read-only.stderr.
//
// Both are narrower than they could be, deliberately, and the narrowness is
// load-bearing rather than cautious. codex has a second refusal that reads
// almost identically and must *not* be matched — measured 2026-08-29 on 0.150.1
// by asking a `workspace-write` run to write outside its directory:
//
//	error=patch rejected: writing outside of the project; …
//	error=patch rejected: writing is blocked by read-only sandbox; …
//
// The first is one call being told no, in a run that went on to write its file
// inside the workspace and finish. The second is the workspace being read-only
// for the whole run. Same tracing target, same level, same six opening words,
// opposite meanings — so `codexWriteRefused` is the span where they part.
// Matching on the target, or on "rejected", or on every ERROR line, would fail
// a run that did its work, which #89's acceptance criteria forbid.
//
// Two limits, both measured and neither hidden:
//
//   - A codex release rewording the sentence stops the detection. That breaks
//     **open** — back to the `completed` this server reported before, which is a
//     known defect with an open issue — never closed onto a run that did its
//     work.
//   - A read-only run whose writes are all attempted through the *shell* is not
//     detected at all, because codex logs nothing for it. Measured the same day:
//     a run told to write with `printf` had both attempts blocked, created no
//     file, emitted no ERROR line, and emitted no `command_execution` item
//     either — the refusal survives only in the agent's own prose. There is
//     nothing there to read, and prose is not a signal this server will match.
//     ADR-0008's argument removes the cause; this reads the one route that
//     leaves a trace.
const (
	codexErrorLevel   = " ERROR "
	codexWriteRefused = "writing is blocked"
)

// codexFileChangeItem is the item type codex uses for a write that landed.
// Verified 2026-08-29 on 0.150.1: five `item.started`/`item.completed` pairs of
// this type for five files that appeared on disk.
const codexFileChangeItem = "file_change"

// codexToolItems are the item types that are a tool doing something, as against
// the model talking: `agent_message` and `reasoning` are the answer being
// written, and are not steps.
//
// The list is a whitelist rather than a "not the two talking types" test, and
// that direction is the safe one for a budget: an item type a later codex adds
// goes uncounted, which spends a client's ceiling more slowly than it should,
// where the inverse would spend it on something that was never a tool call.
// step_capture_test.go reads the same set, against the capture.
var codexToolItems = map[string]bool{
	codexFileChangeItem: true,
	"command_execution": true,
	"mcp_tool_call":     true,
	"web_search":        true,
}

// codexWatch decides whether a codex run that ended cleanly nevertheless could
// not do the work it was asked to do.
//
// Issue #89: under a read-only sandbox codex refuses every write, and says so
// **only on stderr**. Its stdout is indistinguishable from a run that succeeded
// — a captured refusal has no item for the rejected patch at all, ends on
// `turn.completed` rather than `turn.failed`, and exits 0. So a task in which
// nothing could be written reached the client as `completed`, with the agent's
// apology for not doing the work as its answer.
//
// The reading is two facts, not one, and needs both:
//
//   - a write was refused (stderr), and
//   - no write ever succeeded (stdout).
//
// The second is the guard against failing a run that recovered, and it is
// specifically a *write* rather than any tool: in the captured refusal the agent
// went on to run a shell command that succeeded, so "some tool worked" would
// have cleared the very run this exists to catch. A write against a write is the
// only pairing that answers both directions.
//
// **It is also, on codex 0.150.1, unreachable — and it stays.** The two facts
// cannot both hold today: under the sandbox that produces this refusal nothing
// can be written by any route, shell included, so no `file_change` completes;
// and the per-call refusal a run *can* recover from carries different words and
// never reaches here (see codexWriteRefused). Both were measured on 2026-08-29.
//
// Keeping an unreachable guard is a deliberate trade and #13 is the argument
// for it: two of opencode's execution-verified claims were true when written and
// false one minor version later, with nothing in the tests to notice.
// "Unreachable" here is a fact about one version of one CLI, not about the
// design. If a later codex reuses this wording for a refusal a run survives, the
// guard is the only thing standing between that and a finished run reported as
// failed — which is the worst outcome available here, and the one an argument in
// a comment cannot prevent on its own.
//
// It is order-free on purpose. stdout and stderr are scanned by two goroutines
// with no ordering between them, so "did a write succeed *after* the refusal" is
// a question this cannot honestly answer. "Did a write succeed at all" it can.
type codexWatch struct {
	// Written from the stderr goroutine and the stdout goroutine respectively,
	// read from neither until both have finished. Guarded anyway: the
	// concurrency is a property of the caller, and a mutex here is cheaper than
	// a comment asking every future reader to re-derive that it is safe.
	mu      sync.Mutex
	refusal string
	wrote   bool
}

// Stdout records that a write landed. Only the completion counts: a request is
// not a write, and the refused patch never produced an item of any kind.
//
// `item.status` is deliberately not read. A `file_change` that completed
// unsuccessfully would clear the refusal here, which is the same open direction
// everything else in this file errs towards — while requiring the field would
// mean a codex that stopped sending it silently started failing runs that wrote.
// No such item has been observed, and it is the better signal if one ever is:
// stdout saying a write failed would replace this whole mechanism.
func (w *codexWatch) Stdout(line string) {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	if ev.Type == "item.completed" && ev.Item.Type == codexFileChangeItem {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.wrote = true
	}
}

// Stderr keeps the first refusal codex logged, in codex's own words.
//
// The first rather than the last, because a run refused repeatedly is refused
// for one reason and the client needs it once.
//
// The timestamp and the tracing target are dropped: neither is the runtime
// telling anyone why, and the timestamp is a fact about the capture machine.
// Everything after them is passed through as codex wrote it — including the
// tracing field key `error=`, which is framing too. It is left in rather than
// cut because cutting it means guessing at a syntax nothing here has measured,
// and a stray four characters in front of the reason is a smaller cost than a
// rule that could one day take the reason with it.
func (w *codexWatch) Stderr(line string) {
	_, message, atLevel := strings.Cut(line, codexErrorLevel)
	if !atLevel || !strings.Contains(line, codexWriteRefused) {
		return
	}
	if _, afterTarget, named := strings.Cut(message, ": "); named {
		message = afterTarget
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refusal == "" {
		w.refusal = strings.TrimSpace(message)
	}
}

func (w *codexWatch) Failure() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.wrote {
		return ""
	}
	return w.refusal
}
