package harness

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #89. A codex run whose writes were all refused ended on
// `turn.completed`, exited 0, and reached the client as `completed` with the
// agent's apology for not doing the work as its answer. Nothing on stdout said
// otherwise; the refusal was on stderr and nowhere else.
//
// Everything asserted here is read off two captures of the same prompt — five
// files, one write each — taken minutes apart on codex-cli 0.150.1 with one
// difference between them:
//
//	testdata/steps/codex-read-only.jsonl   the shipped invocation before
//	testdata/steps/codex-read-only.stderr  ADR-0008: zero files on disk
//	testdata/steps/codex.jsonl             the shipped invocation after: five
//
// A pair rather than a single fixture, because the claim has two halves and one
// capture can only carry one of them: that a refused run is caught, *and* that a
// run which wrote is not. A detector proven only against the first is a
// detector nobody has shown to be safe.

// readCaptureLines returns a fixture's lines as the process runner would hand
// them to a watch: whole, unparsed, blanks dropped.
//
// Deliberately not readCapture. That one decodes each line into a map, which is
// the right shape for counting events and the wrong one here — a RunWatch's
// whole job is to read text the parser does not, and handing it something
// already parsed would test a different thing from the one that runs.
func readCaptureLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "steps", name))
	if err != nil {
		t.Fatalf("opening the %s capture: %v", name, err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			lines = append(lines, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the %s capture: %v", name, err)
	}
	if len(lines) == 0 {
		t.Fatalf("the %s capture is empty", name)
	}
	return lines
}

// replay feeds a whole run to a fresh watch and returns its verdict.
func replay(t *testing.T, stdout, stderr []string) string {
	t.Helper()
	w := &codexWatch{}
	for _, line := range stdout {
		w.Stdout(line)
	}
	for _, line := range stderr {
		w.Stderr(line)
	}
	return w.Failure()
}

// TestCodexRefusedRunIsAFailure is the defect, replayed.
//
// The capture is the run the issue reports: the task asked for five files and
// none was created, because every write was refused by a read-only sandbox.
// Before this the run's terminal update was UpdateCompleted.
func TestCodexRefusedRunIsAFailure(t *testing.T) {
	stdout := readCaptureLines(t, "codex-read-only.jsonl")
	stderr := readCaptureLines(t, "codex-read-only.stderr")

	got := replay(t, stdout, stderr)
	if got == "" {
		t.Fatalf("a run that was refused every write reports no failure, so it reaches the " +
			"client as `completed` with the agent's apology as its answer")
	}

	// The runtime's own words, not a sentence this server made up. Both halves
	// of the message codex logged have to survive, because between them they
	// say what was refused and why — which is the whole of what an operator
	// needs to know to fix it.
	for _, want := range []string{"patch rejected", "read-only sandbox"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure says %q, which does not carry codex's own %q", got, want)
		}
	}

	// And none of the tracing framing around them. The timestamp is a fact about
	// the capture machine and the target is a Rust module path; neither is the
	// runtime explaining itself to anyone. The field key `error=` does survive,
	// deliberately — see codexWatch.Stderr — so this is a list of what is
	// stripped rather than a claim that nothing but prose remains.
	for _, unwanted := range []string{"ERROR", "codex_core::", "2026-"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("the failure carries %q, which is tracing framing rather than the "+
				"reason: %q", unwanted, got)
		}
	}
}

// TestCodexRunThatWroteIsNotAFailure is the other half, and the one that makes
// the first half safe to ship.
//
// A signal that fails a run which did its work is worse than the silence it
// replaces, so the refusal is paired with its own inverse: a write that landed.
// The pairing is not decoration. In the refused capture the agent went on to run
// a shell command that *succeeded*, so "some tool worked afterwards" would have
// cleared the very run above — only "a write worked" separates the two.
func TestCodexRunThatWroteIsNotAFailure(t *testing.T) {
	wrote := readCaptureLines(t, "codex.jsonl")

	t.Run("a clean run", func(t *testing.T) {
		if got := replay(t, wrote, nil); got != "" {
			t.Fatalf("a run that created every file it was asked for is reported failed: %q", got)
		}
	})

	// The mechanical guard, exercised against the refusal that produced #89.
	//
	// This pairing does not occur on codex 0.150.1 and the test says so rather
	// than implying otherwise: under the sandbox that logs this line nothing can
	// be written by any route, so no `file_change` completes beside it. What is
	// asserted is that if the two ever do arrive together, the run that wrote is
	// the one believed. See codexWatch on why an unreachable guard is kept.
	t.Run("a refusal beside a write that landed", func(t *testing.T) {
		refusal := readCaptureLines(t, "codex-read-only.stderr")
		if got := replay(t, wrote, refusal); got != "" {
			t.Fatalf("a run refused one write and completing five others is reported "+
				"failed: %q", got)
		}
	})
}

// codexOutsideProjectRefusal is the refusal a run *can* recover from, and the
// reason `codexWriteRefused` is the span of the sentence it is.
//
// Measured 2026-08-29 on codex-cli 0.150.1, verbatim: a `workspace-write` run
// was asked to write one file outside its directory and one inside. The first
// was refused with this line, the second was written, and the run finished. Same
// tracing target as the read-only refusal, same level, same six opening words.
const codexOutsideProjectRefusal = "2026-08-29T06:31:32.456331Z ERROR codex_core::tools::router: " +
	"error=patch rejected: writing outside of the project; rejected by user approval settings"

// TestCodexPerCallRefusalIsNotARunFailure is the acceptance criterion #89 states
// most sharply — *"whatever signal is read must not fail a run that succeeded"* —
// tested against the case that actually threatens it.
//
// Everything else here pairs the read-only refusal with a fixture. This is the
// one place two *different* refusals are told apart, and it is the discriminator
// the whole design rests on: one names a workspace that cannot be written and
// the other names one call that was not allowed. Matching the tracing target, or
// the word "rejected", or every ERROR line would collapse them, and every one of
// those was a plausible way to write this.
func TestCodexPerCallRefusalIsNotARunFailure(t *testing.T) {
	// Against a run with no write in it at all, which is the hostile version:
	// the `wrote` guard cannot be what saves this one, so a pass means the
	// stderr match itself declined the line.
	quiet := []string{`{"type":"turn.started"}`, `{"type":"turn.completed"}`}
	if got := replay(t, quiet, []string{codexOutsideProjectRefusal}); got != "" {
		t.Fatalf("a refusal of one call fails the whole run: %q", got)
	}

	// And the two really are as close as claimed — otherwise this test passes
	// for a reason weaker than the one it is documenting.
	captured := readCaptureLines(t, "codex-read-only.stderr")
	shared := "ERROR codex_core::tools::router: error=patch rejected: "
	var refusal string
	for _, line := range captured {
		if strings.Contains(line, shared) {
			refusal = line
		}
	}
	if refusal == "" {
		t.Fatalf("the captured refusal no longer contains %q, so the two lines this test "+
			"separates are no longer the near-identical pair it exists for", shared)
	}
	if !strings.Contains(codexOutsideProjectRefusal, shared) {
		t.Fatalf("the per-call refusal no longer shares %q with the read-only one", shared)
	}
}

// TestCodexWatchClaimsOnlyTheRefusalItMeasured pins the narrowness of the match.
//
// codex logs at ERROR for things it recovers from, and this server has measured
// exactly one refusal. Matching every ERROR line would fail runs that finished —
// the same mistake parseCodexLine refuses to make with the top-level `error`
// event, arriving by a different door.
//
// The direction of every miss here is the point. A codex release that rewords
// the sentence falls out of this match and the run is reported `completed`
// again: back to a known defect with an open issue, rather than forward into a
// finished run reported as failed.
func TestCodexWatchClaimsOnlyTheRefusalItMeasured(t *testing.T) {
	// Shaped like the real line — same tracing layout, same level — and about
	// something else entirely.
	unrelated := "2026-08-29T05:30:16.734044Z ERROR codex_core::client: " +
		"error=stream disconnected before completion; retrying 1/5"

	if got := replay(t, readCaptureLines(t, "codex-read-only.jsonl"), []string{unrelated}); got != "" {
		t.Fatalf("an ERROR this server has never measured fails the run: %q", got)
	}
}

// TestCodexWatchKeepsTheFirstRefusal. A run refused repeatedly was refused for
// one reason, and a client needs it once. Nothing enforces which one that would
// be, so it is stated: the first.
func TestCodexWatchKeepsTheFirstRefusal(t *testing.T) {
	first := readCaptureLines(t, "codex-read-only.stderr")
	second := "2026-08-29T05:31:02.000000Z ERROR codex_core::tools::router: " +
		"error=patch rejected: writing is blocked by a second reason nobody has seen"

	got := replay(t, nil, append(append([]string{}, first...), second))
	if strings.Contains(got, "a second reason") {
		t.Fatalf("a later refusal displaced the first: %q", got)
	}
	if !strings.Contains(got, "read-only sandbox") {
		t.Fatalf("the first refusal was not kept: %q", got)
	}
}

// TestCodexRefusalBeatsACleanExit runs the whole thing through the process
// runner, which is where the two halves of #89 actually meet: codex exits 0,
// says nothing terminal on stdout, and the run must still fail.
//
// The script is the capture: its stdout is the refused run's own lines and its
// stderr is the refused run's own stderr, so what is being asserted is the
// captured run's terminal update rather than a shape invented for a test.
func TestCodexRefusalBeatsACleanExit(t *testing.T) {
	script := &strings.Builder{}
	for _, line := range readCaptureLines(t, "codex-read-only.jsonl") {
		fmt.Fprintf(script, "printf '%%s\\n' %s\n", shellQuote(line))
	}
	for _, line := range readCaptureLines(t, "codex-read-only.stderr") {
		fmt.Fprintf(script, "printf '%%s\\n' %s >&2\n", shellQuote(line))
	}
	// The exit code the real run had, and the reason the run was reported
	// completed: nothing else about it looks like a failure.
	script.WriteString("exit 0\n")

	h := shHarnessWatching(t, script.String(), PromptArgs, parseCodexLine,
		func() RunWatch { return &codexWatch{} })

	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t-codex-refused"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(t, ch)

	l := last(t, got)
	if l.Type != UpdateFailed {
		t.Fatalf("terminal update = %q, want %q — a run in which every write was refused "+
			"is reported to the client as a success", l.Type, UpdateFailed)
	}
	if l.Err == nil || !strings.Contains(l.Err.Error(), "read-only sandbox") {
		t.Fatalf("the failure does not carry codex's reason: %v", l.Err)
	}
	// Named, so a client reading a failure can tell which of five harnesses
	// produced it. The name here is the stub's, because that is the harness that
	// ran; on the real adapter the same field is `codex`.
	if !strings.HasPrefix(l.Err.Error(), "harness: "+h.Binary+": ") {
		t.Errorf("the failure does not name the harness that produced it: %v", l.Err)
	}

	// Exactly one terminal update. UpdateFailed replaces the UpdateCompleted
	// that used to be sent here rather than racing it — a run that reported both
	// would leave which one a client saw up to ordering.
	terminals := 0
	for _, u := range got {
		if u.Type.Terminal() {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("%d terminal updates, want 1: %+v", terminals, got)
	}
}

// shellQuote wraps a line so /bin/sh hands it back byte for byte. The captures
// are JSON full of double quotes and backslashes, so single quotes are the only
// form that survives, with the one escape single quoting cannot express spelled
// out.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
