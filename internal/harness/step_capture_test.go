package harness

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The evidence half of #72. `max_step` is a budget on agent steps, and this
// server can only claim it on a base whose output a step can be counted from —
// so what each base narrates is established here, by replaying a capture taken
// against a known ground truth, rather than asserted from a vendor doc. That is
// the same rule #32/#33/#34 exist for.
//
// The counter itself is service.supervise's. What is here is everything the
// counter is entitled to assume: which edge each base narrates, that it
// narrates every call, that each adapter turns that edge into exactly one step,
// and that a base cannot be registered without having been probed.
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
			// pi announces `toolcall_start` when the model asks for a tool,
			// then `toolcall_delta`, `toolcall_end`, `tool_execution_start` and
			// `tool_execution_end` for the same call — five events, of which
			// only the first is the start edge.
			//
			// This row is new, and #91 is why it could not exist before: pi
			// routes through whichever provider the machine is logged in to,
			// and the only one reachable capped at 8,000 tokens per minute
			// against a 71,166-token request. `make probe-pi-steps` answers
			// from a loopback provider instead, which is pi's own layer
			// unchanged — the same argument probe-pi-session.py makes for #33 —
			// with the five files on disk as the ground truth either way.
			base: "pi",
			calls: func(ev map[string]any) int {
				if ev["type"] != "message_update" {
					return 0
				}
				ame, ok := ev["assistantMessageEvent"].(map[string]any)
				if !ok {
					return 0
				}
				if ame["type"] == "toolcall_start" {
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

// TestEveryRegisteredBaseCanBeBounded is the gate a sixth harness has to pass,
// and the reason it is a test rather than a note: `max_step` is a bound, and
// ADR-0007's rule is that a bound holds on every base or is not claimed at all.
// Adding an adapter that narrates nothing countable would silently un-honour
// the field for every task naming it — the ceiling would be accepted, reported
// on `metadata.max_step`, and never fire.
//
// It enumerates the constructors uhpd compiles in, with **no allowlist**, which
// is the point: an allowlist is how a gate rots. A base that genuinely cannot
// be counted is not excused here — it either bounds itself, like grok, or the
// answer is that it cannot carry the field, and StartTask refuses the task
// rather than accepting a bound nobody holds.
//
// Each base must also have the capture its claim rests on. A declaration is a
// sentence anyone can write; the fixture is the run that happened.
func TestEveryRegisteredBaseCanBeBounded(t *testing.T) {
	models := []string{"<model>"}
	// The fixture each declaration rests on, named per base rather than derived
	// from it: the capture files are the probes' names for these runtimes
	// ("claude") and the adapters' are the harness ids ("claude-code"), and a
	// rule that guessed one from the other would go looking for the wrong file
	// the first time a base is renamed.
	//
	// grok's entry is a different question's answer, and deliberately so. Its
	// exemption does not rest on narrating every call — it rests on its own
	// stop being distinguishable from a success, which is what
	// grok-max-turns.jsonl captured.
	fixtures := map[string]string{
		"claude-code": "claude.jsonl",
		"codex":       "codex.jsonl",
		"opencode":    "opencode.jsonl",
		"pi":          "pi.jsonl",
		"grok-cli":    "grok-max-turns.jsonl",
	}

	for _, h := range []*CLIHarness{
		NewClaude(models),
		NewCodex(models),
		NewGrok(models),
		NewOpenCode(models),
		NewPi(models),
	} {
		t.Run(h.Base, func(t *testing.T) {
			edge := h.StepEdge()
			if edge == StepEdgeNone {
				t.Fatalf("%s declares no step edge, so a max_step budget on it would be "+
					"accepted and never enforced. Probe it — `make probe-steps` — and either "+
					"record which edge it narrates or establish that it bounds itself",
					h.Base)
			}

			fixture, named := fixtures[h.Base]
			if !named {
				t.Fatalf("%s is registered and has no capture named here: a base joins this "+
					"map by being probed, and there is no entry that means `skip`", h.Base)
			}
			if _, err := os.Stat(filepath.Join("testdata", "steps", fixture)); err != nil {
				t.Fatalf("%s declares step edge %q with no capture behind it (%s): a base's "+
					"narration is established by running it, never by saying so", h.Base, edge, err)
			}
		})
	}
}

// TestAZeroCeilingSurvivesARunThatCallsNothing is the case that is easy to get
// wrong in the direction nobody notices: `max_step: 0` must not break a task
// that answers without touching anything.
//
// It is asserted against the parsers rather than a live CLI, because that is
// where the rule lives — a turn with no tool call in it emits no
// UpdateToolCall, so no ceiling can be spent by an agent that only talks. If
// this broke, every "what does this file do" question on a bounded harness
// would come back `incomplete`.
func TestAZeroCeilingSurvivesARunThatCallsNothing(t *testing.T) {
	for _, tc := range []struct {
		base  string
		parse func(string) []RunUpdate
		lines []string
	}{
		{"claude", parseClaudeLine, []string{claudeInitEvent, claudeTextDeltaEvent, claudeResultEvent}},
		{"opencode", parseOpenCodeLine, []string{openCodeStepStartEvent, openCodeTextEvent}},
		{"codex", parseCodexLine, []string{codexThreadStartedEvent, codexAgentMessageEvent}},
		{"grok-cli", parseGrokLine, []string{grokInitEvent, grokTextDeltaEvent}},
		{"pi", parsePiLine, []string{piSessionEvent, piTextDeltaEvent}},
	} {
		t.Run(tc.base, func(t *testing.T) {
			for _, line := range tc.lines {
				for _, upd := range tc.parse(line) {
					if upd.Type == UpdateToolCall {
						t.Errorf("a run that called no tool narrated a step:\n  %s", line)
					}
				}
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

// The counting half of #72, asserted per adapter against the shape each CLI
// actually emits. TestCapturedToolCallsMatchGroundTruth already proves the
// *captures* can be counted; these prove the parsers do it, which is a
// different claim and the one a client's budget rests on.
//
// They live here rather than in cli_test.go because they are one subject rather
// than four, and reading them side by side is what shows the edge is a per-base
// fact and not an inconsistency.

// TestEveryAdapterEmitsOneStepPerToolCall replays each base's own start-edge
// line and requires exactly one step from it. One is the whole assertion: zero
// leaves the ceiling unfireable, and two halves it.
func TestEveryAdapterEmitsOneStepPerToolCall(t *testing.T) {
	for _, tc := range []struct {
		base  string
		parse func(string) []RunUpdate
		line  string
	}{
		{
			"claude", parseClaudeLine,
			`{"type":"assistant","message":{"id":"msg_1","role":"assistant","type":"message",` +
				`"content":[{"type":"tool_use","id":"toolu_1","name":"Write","input":{}}],` +
				`"stop_reason":"tool_use"},"session_id":"s"}`,
		},
		{"opencode", parseOpenCodeLine, openCodeToolUseEvent},
		{"codex", parseCodexLine, codexCommandStartedEvent},
		{
			"pi", parsePiLine,
			`{"type":"message_update","assistantMessageEvent":` +
				`{"type":"toolcall_start","contentIndex":0,"id":"call_0","toolName":"write"}}`,
		},
	} {
		t.Run(tc.base, func(t *testing.T) {
			steps := 0
			for _, upd := range tc.parse(tc.line) {
				if upd.Type == UpdateToolCall {
					steps++
				}
			}
			if steps != 1 {
				t.Errorf("%s narrated %d steps for one tool call — a ceiling counted from "+
					"this is %s", tc.base, steps,
					map[bool]string{true: "unfireable", false: "spent too fast"}[steps == 0])
			}
		})
	}
}

// TestClaudeCountsBlocksRatherThanMessages is claude's own case, and it is not
// pedantry: claude puts several `tool_use` blocks in one `assistant` message
// when it calls tools in parallel, and each is a call the agent made. Counting
// messages would let `max_step: 1` cover a message asking for twenty tools.
func TestClaudeCountsBlocksRatherThanMessages(t *testing.T) {
	line := `{"type":"assistant","message":{"id":"msg_1","role":"assistant","type":"message",` +
		`"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{}},` +
		`{"type":"text","text":"and also"},` +
		`{"type":"tool_use","id":"toolu_2","name":"Write","input":{}}],` +
		`"stop_reason":"tool_use"},"session_id":"s"}`

	steps, deltas := 0, 0
	for _, upd := range parseClaudeLine(line) {
		switch upd.Type {
		case UpdateToolCall:
			steps++
		case UpdateDelta:
			deltas++
		}
	}
	if steps != 2 {
		t.Errorf("steps = %d, want 2 — one message can ask for several tools", steps)
	}
	// The text block beside them is not the answer here: the answer arrives as
	// `stream_event` deltas, and reading the finished message as well would
	// publish every answer twice.
	if deltas != 0 {
		t.Errorf("the message produced %d deltas — its text is the deltas' own, repeated", deltas)
	}
}

// TestGrokIsNotCountedAndReportsItsOwnStop is the exemption in code rather than
// in prose. grok narrates `tool_use` blocks exactly as claude does, so counting
// it would be easy and is deliberately not done: its runtime holds the ceiling,
// and a router that also counted would spend the budget twice over on a base
// that had already been told the number.
func TestGrokIsNotCountedAndReportsItsOwnStop(t *testing.T) {
	t.Run("a tool call is not a step", func(t *testing.T) {
		line := `{"type":"assistant","message":{"id":"msg_1","role":"assistant","type":"message",` +
			`"content":[{"type":"tool_use","id":"toolu_1","name":"run_terminal_command","input":{}}],` +
			`"stop_reason":"tool_use"},"session_id":"s"}`
		for _, upd := range parseGrokLine(line) {
			if upd.Type == UpdateToolCall {
				t.Fatal("grok narrated a step: it enforces --max-turns itself, and a second " +
					"count would stop a run at a ceiling grok had already been given")
			}
		}
	})

	// The mapping #90 established, and the ordering is the assertion: the same
	// line carries `is_error: true`, so a parser reading that first reports a
	// truncated run as `failed`. Lifecycle §3 requires `incomplete` for a
	// budget and forbids it for an error.
	t.Run("its own budget stop is incomplete, not failed", func(t *testing.T) {
		var terminal string
		for _, ev := range readCaptureLines(t, "grok-max-turns.jsonl") {
			if strings.Contains(ev, `"type":"result"`) {
				terminal = ev
			}
		}
		if terminal == "" {
			t.Fatal("the grok capture has no `result` event to replay")
		}

		var got RunUpdate
		var found bool
		for _, upd := range parseGrokLine(terminal) {
			if upd.Type.Terminal() {
				got, found = upd, true
			}
		}
		if !found {
			t.Fatal("a --max-turns stop produced no terminal update, so the run would be " +
				"reported by its exit code alone")
		}
		if got.Type != UpdateIncomplete {
			t.Errorf("terminal = %q, want %q — a run somebody's ceiling truncated is work "+
				"worth continuing, not work that could not be done",
				got.Type, UpdateIncomplete)
		}
		if got.Reason != ReasonMaxStep {
			t.Errorf("reason = %q, want %q", got.Reason, ReasonMaxStep)
		}
		if got.Err != nil {
			t.Errorf("the stop carries an error (%v) — `incomplete` has no error object, "+
				"and grok's own `errors` array is about the ceiling rather than a fault",
				got.Err)
		}
	})

	// An ordinary grok failure must still be a failure. Without this the test
	// above passes just as well against a parser that called every terminal
	// line incomplete.
	t.Run("an ordinary failure is still a failure", func(t *testing.T) {
		line := `{"type":"result","subtype":"error","is_error":true,` +
			`"errors":["Model not found"],"session_id":"s"}`
		var got RunUpdate
		for _, upd := range parseGrokLine(line) {
			if upd.Type.Terminal() {
				got = upd
			}
		}
		if got.Type != UpdateFailed {
			t.Errorf("terminal = %q, want %q — only `error_max_turns` is a budget stop",
				got.Type, UpdateFailed)
		}
	})
}

// TestGrokIsGivenTheCeilingItEnforces pins the other half of the exemption. A
// base excused from being counted is only bounded if it is actually told the
// number, and nothing else in the tree would notice if the flag stopped being
// sent.
func TestGrokIsGivenTheCeilingItEnforces(t *testing.T) {
	h := NewGrok([]string{"<model>"})

	withBudget, err := h.BuildArgs(RunRequest{Input: "x", MaxStep: 5})
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	if !slices.Contains(withBudget, "--max-turns") {
		t.Fatalf("argv = %v, carries no --max-turns: grok is exempt from counting "+
			"*because* it enforces its own ceiling, so an argv without one is a task "+
			"reported as bounded and run unbounded", withBudget)
	}
	if i := slices.Index(withBudget, "--max-turns"); i+1 >= len(withBudget) || withBudget[i+1] != "5" {
		t.Errorf("argv = %v, want --max-turns 5", withBudget)
	}

	// And not otherwise. Every other test in this tree builds grok's argv
	// without a budget, and a flag that appeared anyway would change what uhpd
	// sends on every ordinary task.
	unbounded, err := h.BuildArgs(RunRequest{Input: "x"})
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	if slices.Contains(unbounded, "--max-turns") {
		t.Errorf("argv = %v, carries --max-turns for a task that asked for no budget", unbounded)
	}
}

// TestPiStepProbeRunsTheShippedInvocation is the same guard the other probes
// carry. A probe that measured an argv uhpd does not send would report a
// healthy narration for a server that does not exist — which is exactly what
// the first pass of probe-steps.py did, and the reason every probe here is
// pinned.
func TestPiStepProbeRunsTheShippedInvocation(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "probe-pi-steps.py"))
	if err != nil {
		t.Fatalf("the pi step probe is missing: %v", err)
	}

	h := NewPi([]string{"<model>"})
	if h.Prompt != PromptStdin {
		t.Fatalf("pi takes its prompt as %q, not on stdin — the probe's argv is either "+
			"missing whatever carries it now or carrying one it should not", h.Prompt)
	}
	args, err := h.BuildArgs(RunRequest{Input: "<prompt>", Model: "<model>"})
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := strings.Join(append([]string{"pi"}, args...), " ")
	got := strings.Join(pythonStringList(t, string(src), "HARNESS_ARGV"), " ")
	if got != want {
		t.Errorf("HARNESS_ARGV measures an invocation uhpd does not send:\n  probe: %v\n  uhpd:  %v",
			got, want)
	}
}
