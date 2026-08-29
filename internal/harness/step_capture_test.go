package harness

import (
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
// wrote, which is the point of keeping a capture at all. The reading of the file
// itself is readCaptureLines', which serves the tests that need the raw line.
func readCapture(t *testing.T, base string) []map[string]any {
	t.Helper()
	lines := readCaptureLines(t, base+".jsonl")

	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("the %s capture stopped being JSON: %v", base, err)
		}
		events = append(events, ev)
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
		{
			// codex announces `item.started` when the model asks for a tool and
			// `item.completed` when it finishes, so only the first is counted —
			// the same start edge as the other two.
			//
			// This row is new, and #89 is why it could not exist before: under
			// the invocation uhpd used to send, codex was refused every write
			// and took no countable tool call at all. ADR-0008 changed the
			// invocation and the capture is of the new one.
			base: "codex",
			calls: func(ev map[string]any) int {
				if ev["type"] != "item.started" {
					return 0
				}
				item, ok := ev["item"].(map[string]any)
				if !ok {
					return 0
				}
				switch item["type"] {
				// A tool doing something, as against the model talking:
				// `agent_message` and `reasoning` are the answer being written.
				// The same set the probe counts.
				case codexFileChangeItem, "command_execution", "mcp_tool_call", "web_search":
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

// grokMaxTurnsSubtype is what grok 1.0.13 put on the terminal `result` event of
// a run its own `--max-turns` ceiling stopped. It is quoted here, in the
// README, and nowhere else: the fixture is the evidence and this is the reading
// of it.
const grokMaxTurnsSubtype = "error_max_turns"

// TestGrokReportsItsOwnMaxTurnsStop is the fact #72's grok exemption rests on.
//
// grok is the one base this server does not count, because it bounds its own
// steps with `--max-turns`. A flag that stops a run is half of a budget; the
// other half is that the stopped run *says* it stopped. Without that half a
// truncated run reaches a client as `completed`, and — unlike every
// counting risk in #72 — the router cannot repair it, having neither done the
// stopping nor anything to relabel.
//
// So the discriminator is pinned against the success line rather than merely
// spelled out. A future grok that collapses the two subtypes fails here instead
// of silently reintroducing the truncated-run-reported-as-completed risk.
func TestGrokReportsItsOwnMaxTurnsStop(t *testing.T) {
	// The success subtype is read out of cli_test.go's own fixture rather than
	// written down twice, so "distinct from a success" cannot quietly become a
	// comparison against a value grok stopped using.
	var success grokStreamEvent
	if err := json.Unmarshal([]byte(grokResultEvent), &success); err != nil {
		t.Fatalf("the grok success fixture stopped being JSON: %v", err)
	}

	events := readCapture(t, "grok-max-turns")
	var terminal map[string]any
	for _, ev := range events {
		if ev["type"] == "result" {
			terminal = ev
		}
	}
	if terminal == nil {
		t.Fatalf("the grok capture has no `result` event, so the run said nothing about " +
			"why it ended")
	}

	if got := terminal["subtype"]; got != grokMaxTurnsSubtype {
		t.Errorf("subtype = %v, want %q — the README quotes this value as the observed one",
			got, grokMaxTurnsSubtype)
	}
	if terminal["subtype"] == success.Subtype {
		t.Fatalf("a --max-turns stop and a finished run both report subtype %q, so a "+
			"truncated run is indistinguishable from a completed one. grok can no longer "+
			"be exempted from counting on the strength of enforcing natively",
			success.Subtype)
	}

	// Recorded, not merely tolerated. grok reports its own budget stop *as an
	// error*, and parseGrokLine reads `is_error` and nothing else off this
	// line — so the mapping #72 lands must read `subtype` first. Lifecycle §3
	// requires `incomplete` for a budget and forbids it for an error, and a
	// budget stop surfaced as `failed` is the wrong one of the two.
	if terminal["is_error"] != true {
		t.Errorf("is_error = %v, want true — if grok has stopped labelling its budget "+
			"stop an error then the ordering parseGrokLine needs is no longer the "+
			"subject it was", terminal["is_error"])
	}

	// The run was genuinely truncated, and the capture says so on its own: one
	// tool call narrated against a task that could not be done in fewer than
	// five, because each file's contents depend on reading the one before it.
	// The ground truth this pairs with is on disk at probe time — one file of
	// five — and is recorded in the README.
	calls := 0
	for _, ev := range events {
		if ev["type"] == "assistant" {
			calls += blockCount(ev, "tool_use")
		}
	}
	if calls == 0 || calls >= capturedCalls {
		t.Errorf("the capture narrates %d tool calls for a chained task needing %d — a "+
			"run that took none, or took them all, is not a measurement of a stop",
			calls, capturedCalls)
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
// The prompt is absent from every stdin list on purpose: four of the five
// adapters declare PromptStdin, so a prompt appearing in argv would itself be
// the defect. grok is the fifth and the exception — its prompt is the value of
// `-p`, so `<prompt>` has to be in its pinned list for the pin to cover it.
//
// `--max-turns` is deliberately absent from GROK_ARGV. uhpd does not send it,
// and the probe appends it from a list of its own; pinning it here would make
// this test assert the opposite of what is true.
func TestStepProbeRunsTheShippedInvocation(t *testing.T) {
	models := []string{"<model>"}
	for _, tc := range []struct {
		probe   string
		list    string
		binary  string
		harness *CLIHarness
		// prompt is where this adapter's Input is expected to travel. Pinned
		// per case rather than assumed, because a base that quietly moved its
		// prompt from stdin into argv would otherwise pass this test while the
		// pinned list had stopped carrying the whole invocation.
		prompt PromptMode
	}{
		{"probe-steps.py", "CLAUDE_ARGV", "claude", NewClaude(models), PromptStdin},
		{"probe-steps.py", "OPENCODE_ARGV", "opencode", NewOpenCode(models), PromptStdin},
		{"probe-steps.py", "CODEX_ARGV", "codex", NewCodex(models), PromptStdin},
		{"probe-grok-max-turns.py", "GROK_ARGV", "grok", NewGrok(models), PromptArgs},
	} {
		t.Run(tc.binary, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("..", "..", "scripts", tc.probe))
			if err != nil {
				t.Fatalf("the %s probe is missing: %v", tc.binary, err)
			}

			if tc.harness.Prompt != tc.prompt {
				t.Fatalf("%s takes its prompt as %q, not %q — the probe's argv is either "+
					"missing whatever carries it now or carrying one it should not",
					tc.binary, tc.harness.Prompt, tc.prompt)
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
