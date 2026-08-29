package harness

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The evidence half of #72. `max_step` is a budget on agent steps, and this
// server can only claim it on a base whose output a step can be counted from —
// so what each base narrates is established here, by replaying a capture taken
// against a known ground truth, rather than asserted from a vendor doc. That is
// the same rule #32/#33/#34 exist for.
//
// Nothing here is the counter. The supervisor's counting path lands when #72's
// blockers clear. What lands now is the measurement, because it is the
// expensive part and it holds whichever way those go.
//
// What is asserted is the **invariant the budget depends on**: the number of
// tool calls a base narrates equals the number that demonstrably happened. What
// is deliberately *not* asserted is how a base groups those calls into turns.
// opencode was captured twice and grouped the same five writes into five steps
// once and one step the other, so any assertion about grouping pins one
// afternoon's nondeterminism and fails later for a reason that is not a
// regression. That instability is also why a step is counted as a tool call
// rather than as a round — see testdata/steps/README.md.

// capturedCalls is the ground truth every fixture was taken against: the run
// was asked for five files, one tool call each, and every run produced exactly
// five files on disk.
//
// A base narrating five narrated every call. A base narrating fewer
// under-counts, which is the single failure a step budget cannot survive — a
// caller told it has a ceiling of five while the agent quietly takes twenty.
const capturedCalls = 5

// readCapture decodes one fixture into loosely-typed events.
//
// Deliberately not the adapters' own event structs: those parse the handful of
// fields each adapter acts on today, and a `tool_use` block is not among them.
// Decoding into maps is what lets this test see the whole line the CLI actually
// wrote, which is the point of keeping a capture at all.
func readCapture(t *testing.T, base string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "steps", base+".jsonl"))
	if err != nil {
		t.Fatalf("opening the %s capture: %v", base, err)
	}
	defer f.Close()

	var events []map[string]any
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("the %s capture stopped being JSON: %v", base, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the %s capture: %v", base, err)
	}
	if len(events) == 0 {
		t.Fatalf("the %s capture is empty", base)
	}
	return events
}

// TestCapturedToolCallsMatchGroundTruth is the whole claim in one table: on
// every base this server would have to count, the number of tool calls narrated
// equals the number that demonstrably happened.
//
// Both counts read the **start** of a call — the model asking for the tool,
// before its work happens. Each base also narrates a finish, and counting both
// would double every number here; starting is also what makes "allow N, stop
// before the N+1th" exact rather than approximate.
func TestCapturedToolCallsMatchGroundTruth(t *testing.T) {
	cases := []struct {
		base string
		// calls counts the tool calls one event announces, which is not always
		// one: claude can put several `tool_use` blocks in a single message.
		calls func(map[string]any) int
	}{
		{
			// The `assistant` message carrying `tool_use` blocks is the model
			// asking for those tools. The matching finish is the `user` message
			// carrying the `tool_result`.
			base: "claude",
			calls: func(ev map[string]any) int {
				if ev["type"] != "assistant" {
					return 0
				}
				return blockCount(ev, "tool_use")
			},
		},
		{
			// opencode announces one event per tool part, whatever step it
			// decided to group that part into.
			base: "opencode",
			calls: func(ev map[string]any) int {
				if ev["type"] == "tool_use" {
					return 1
				}
				return 0
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			got := 0
			for _, ev := range readCapture(t, tc.base) {
				got += tc.calls(ev)
			}
			if got != capturedCalls {
				t.Fatalf("%s narrated %d tool calls for %d that actually happened — a budget "+
					"counted from this under-counts, and a client is told it has a ceiling it "+
					"does not have", tc.base, got, capturedCalls)
			}
		})
	}
}

// TestCountingBothEdgesDoublesTheCount pins the reason only the start edge is
// counted. It is not a style preference: a counter that increments on whatever
// arrives halves the effective budget, and would do so silently.
func TestCountingBothEdgesDoublesTheCount(t *testing.T) {
	finishes := 0
	for _, ev := range readCapture(t, "claude") {
		if ev["type"] == "user" {
			finishes += blockCount(ev, "tool_result")
		}
	}
	if finishes != capturedCalls {
		t.Fatalf("claude tool_result count = %d, want %d — one result per call, which is "+
			"exactly why counting results as well as requests would double the count",
			finishes, capturedCalls)
	}
}

// TestStepProbeRunsTheShippedInvocation is the same guard
// TestCodexAndGrokProbesRunTheShippedInvocation puts on the other probes, and it
// exists because the step probe already failed it once: its first pass added
// `--permission-mode bypassPermissions`, `--auto` and `--sandbox
// workspace-write` and passed the prompt as argv, none of which uhpd does. The
// captures it produced described a server that does not exist, and nothing but
// a second reading caught it.
//
// The prompt is absent from every list here on purpose: four of the five
// adapters declare PromptStdin, so a prompt appearing in argv would itself be
// the defect.
func TestStepProbeRunsTheShippedInvocation(t *testing.T) {
	src, err := os.ReadFile("../../scripts/probe-steps.py")
	if err != nil {
		t.Fatalf("the step probe is missing: %v", err)
	}

	models := []string{"<model>"}
	for _, tc := range []struct {
		list    string
		binary  string
		harness *CLIHarness
	}{
		{"CLAUDE_ARGV", "claude", NewClaude(models)},
		{"OPENCODE_ARGV", "opencode", NewOpenCode(models)},
		{"CODEX_ARGV", "codex", NewCodex(models)},
	} {
		t.Run(tc.binary, func(t *testing.T) {
			if tc.harness.Prompt != PromptStdin {
				t.Fatalf("%s no longer takes its prompt on stdin, so the probe's argv is "+
					"missing whatever carries it now", tc.binary)
			}

			args, err := tc.harness.BuildArgs(RunRequest{Input: "<prompt>"})
			if err != nil {
				t.Fatalf("BuildArgs: %v", err)
			}
			want := strings.Join(append([]string{tc.binary}, args...), " ")
			got := strings.Join(pythonStringList(t, string(src), tc.list), " ")
			if got != want {
				t.Errorf("%s measures an invocation uhpd does not send:\n  probe: %v\n  uhpd:  %v",
					tc.list, got, want)
			}
		})
	}
}

// blockCount returns how many of ev's message content blocks have the given
// type — how both claude edges are recognised.
func blockCount(ev map[string]any, blockType string) int {
	msg, ok := ev["message"].(map[string]any)
	if !ok {
		return 0
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return 0
	}
	n := 0
	for _, b := range content {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if block["type"] == blockType {
			n++
		}
	}
	return n
}
