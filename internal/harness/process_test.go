package harness

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// shHarness builds a CLIHarness that runs a shell script, so process
// behaviour can be tested without depending on any agent CLI being installed.
func shHarness(t *testing.T, script string, prompt PromptMode) *CLIHarness {
	t.Helper()
	return (&CLIHarness{
		ID:        "sh",
		Binary:    "/bin/sh",
		Prompt:    prompt,
		BuildArgs: func(RunRequest) ([]string, error) { return []string{"-c", script}, nil },
		ParseLine: passthroughParseLine,
	}).Build()
}

func drain(t *testing.T, ch <-chan RunUpdate) []RunUpdate {
	t.Helper()
	var got []RunUpdate
	for upd := range ch {
		got = append(got, upd)
	}
	return got
}

func last(t *testing.T, ups []RunUpdate) RunUpdate {
	t.Helper()
	if len(ups) == 0 {
		t.Fatal("no updates received at all")
	}
	return ups[len(ups)-1]
}

func TestRunStreamsDeltasThenCompletes(t *testing.T) {
	h := shHarness(t, "echo one; echo two", PromptArgs)
	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(t, ch)

	if l := last(t, got); l.Type != UpdateCompleted {
		t.Fatalf("last update = %q, want %q", l.Type, UpdateCompleted)
	}
	var text strings.Builder
	for _, u := range got {
		if u.Type == UpdateDelta {
			text.WriteString(u.Delta)
		}
	}
	if text.String() != "one\ntwo\n" {
		t.Fatalf("deltas = %q, want %q", text.String(), "one\ntwo\n")
	}
}

func TestNonZeroExitIsFailedAndCarriesStderr(t *testing.T) {
	h := shHarness(t, "echo boom >&2; exit 3", PromptArgs)
	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t2"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	l := last(t, drain(t, ch))
	if l.Type != UpdateFailed {
		t.Fatalf("last update = %q, want %q", l.Type, UpdateFailed)
	}
	if l.Err == nil || !strings.Contains(l.Err.Error(), "boom") {
		t.Fatalf("failure does not carry stderr: %v", l.Err)
	}
}

// PromptStdin must actually deliver the prompt. A harness declared stdin-mode
// whose CLI does not read stdin would otherwise hang silently.
func TestPromptStdinDeliversTheInput(t *testing.T) {
	h := shHarness(t, "cat", PromptStdin)
	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t3", Input: "--not-an-option"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := drain(t, ch)
	var text strings.Builder
	for _, u := range got {
		if u.Type == UpdateDelta {
			text.WriteString(u.Delta)
		}
	}
	if !strings.Contains(text.String(), "--not-an-option") {
		t.Fatalf("stdin prompt not delivered; got %q", text.String())
	}
	if l := last(t, got); l.Type != UpdateCompleted {
		t.Fatalf("last update = %q, want %q", l.Type, UpdateCompleted)
	}
}

// Cancel must terminate the whole process group, not just the direct child.
// Agent CLIs are wrappers that spawn their own children; a surviving
// grandchild holds the inherited stdout pipe open, the reader blocks in Scan
// forever, and no terminal update is ever emitted. This test fails by timing
// out if that regresses.
func TestCancelKillsTheProcessGroupPromptly(t *testing.T) {
	// The outer shell exits immediately; the inner sleep inherits stdout and
	// would keep the pipe open for 60s if only the direct child were killed.
	h := shHarness(t, "sh -c 'sleep 60' & echo started; wait", PromptArgs)

	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t4"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Wait for the process to be up before cancelling.
	first := <-ch
	if first.Type != UpdateDelta {
		t.Fatalf("first update = %q, want a delta", first.Type)
	}

	start := time.Now()
	if err := h.Cancel(context.Background(), "t4"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	done := make(chan RunUpdate, 1)
	go func() {
		var l RunUpdate
		for upd := range ch {
			l = upd
		}
		done <- l
	}()

	select {
	case l := <-done:
		elapsed := time.Since(start)
		if l.Type != UpdateCancelled {
			t.Fatalf("terminal update = %q, want %q", l.Type, UpdateCancelled)
		}
		if elapsed > 10*time.Second {
			t.Fatalf("cancel took %v; the process group is not being killed", elapsed)
		}
		t.Logf("cancelled in %v", elapsed)
	case <-time.After(20 * time.Second):
		t.Fatal("no terminal update after cancel: a grandchild is still holding stdout open")
	}
}

// After a cancelled run, nothing may be left running.
func TestCancelLeavesNoOrphanProcess(t *testing.T) {
	marker := "uhp-go-orphan-marker-" + t.Name()
	h := shHarness(t, "sh -c 'sleep 60 #"+marker+"' & echo started; wait", PromptArgs)

	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t5"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	<-ch
	if err := h.Cancel(context.Background(), "t5"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	for range ch {
	}

	// Give the kernel a moment to reap, then look for survivors.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := exec.Command("pgrep", "-f", marker).Output()
		if strings.TrimSpace(string(out)) == "" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	out, _ := exec.Command("pgrep", "-af", marker).Output()
	t.Fatalf("orphaned process survived cancellation:\n%s", out)
}

// A line longer than the scanner limit must be reported as a failure, not
// silently truncated and reported as a completed run.
func TestOversizedLineIsReportedAsFailureNotSuccess(t *testing.T) {
	// One line of 9 MiB, past the 8 MiB limit.
	h := shHarness(t, "head -c 9437184 < /dev/zero | tr '\\0' 'a'; echo", PromptArgs)
	ch, err := h.Run(context.Background(), RunRequest{TaskID: "t6"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	l := last(t, drain(t, ch))
	if l.Type != UpdateFailed {
		t.Fatalf("truncated output reported as %q; a partial answer must not be labelled complete", l.Type)
	}
	if l.Err == nil || !strings.Contains(l.Err.Error(), "truncated") {
		t.Fatalf("failure does not explain the truncation: %v", l.Err)
	}
}

// Cancelling an unknown task is an error, not a silent success.
func TestCancelUnknownTask(t *testing.T) {
	h := shHarness(t, "true", PromptArgs)
	if err := h.Cancel(context.Background(), "nope"); err == nil {
		t.Fatal("cancelling an unknown task returned nil")
	}
}
