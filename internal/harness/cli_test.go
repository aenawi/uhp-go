package harness

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
)

// argvContains reports whether args contains the exact element s.
func argvContains(args []string, s string) bool {
	for _, a := range args {
		if a == s {
			return true
		}
	}
	return false
}

// index returns the position of s in args, or -1.
func index(args []string, s string) int {
	for i, a := range args {
		if a == s {
			return i
		}
	}
	return -1
}

// TestBuildArgs is the table the architecture review asked for: every P0 the
// project has had was an argv defect, and every one of them would have been
// caught here.
func TestBuildArgs(t *testing.T) {
	models := []string{"m1", "m2"}
	cases := []struct {
		name    string
		h       *CLIHarness
		req     RunRequest
		want    []string
		checkFn func(t *testing.T, args []string)
	}{
		{
			name: "claude: --verbose is mandatory with stream-json",
			h:    NewClaude(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--bare"},
		},
		{
			name: "claude: model and resume",
			h:    NewClaude(models),
			req:  RunRequest{Input: "hello", Model: "m1", NativeSessionID: "s1"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "m1", "--resume", "s1", "--bare"},
		},
		{
			name: "claude: prompt is never in argv",
			h:    NewClaude(models),
			req:  RunRequest{Input: "--dangerously-skip-permissions"},
			checkFn: func(t *testing.T, args []string) {
				if argvContains(args, "--dangerously-skip-permissions") {
					t.Fatalf("prompt leaked into argv: %v", args)
				}
			},
		},
		{
			name: "codex: plain exec carries --skip-git-repo-check",
			h:    NewCodex(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"exec", "--json", "--skip-git-repo-check"},
		},
		{
			name: "codex: resume is a subcommand directly after exec",
			h:    NewCodex(models),
			req:  RunRequest{Input: "hello", NativeSessionID: "s1"},
			want: []string{"exec", "resume", "--json", "--skip-git-repo-check", "s1"},
		},
		{
			name: "codex: options precede the session positional",
			h:    NewCodex(models),
			req:  RunRequest{Input: "hi", Model: "m2", NativeSessionID: "s9"},
			want: []string{"exec", "resume", "--json", "--skip-git-repo-check", "--model", "m2", "s9"},
			checkFn: func(t *testing.T, args []string) {
				if index(args, "--model") > index(args, "s9") {
					t.Fatalf("option after positional: %v", args)
				}
			},
		},
		{
			name: "grok: attached -p= form, not a bare -p value",
			h:    NewGrok(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"-p=hello"},
		},
		{
			name: "grok: a hyphen-leading prompt stays inside the -p= value",
			h:    NewGrok(models),
			req:  RunRequest{Input: "--help"},
			checkFn: func(t *testing.T, args []string) {
				// The whole point: "--help" must never be its own argv element.
				if argvContains(args, "--help") {
					t.Fatalf("prompt became a separate option: %v", args)
				}
				if args[0] != "-p=--help" {
					t.Fatalf("args[0] = %q, want %q", args[0], "-p=--help")
				}
			},
		},
		{
			name: "pi: -p, and no bogus run subcommand",
			h:    NewPi(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"-p"},
			checkFn: func(t *testing.T, args []string) {
				if argvContains(args, "run") {
					t.Fatalf("pi has no run subcommand, but argv has one: %v", args)
				}
			},
		},
		{
			name: "opencode: --format json, and --print-logs is not passed",
			h:    NewOpenCode(models),
			req:  RunRequest{Input: "hello"},
			// Without `--format json` opencode's default renderer writes ANSI
			// escapes and a `> build · <model>` banner to stdout, and prints no
			// session id anywhere — so the client is handed terminal decoration
			// as answer text and every continuation starts a new conversation.
			want: []string{"run", "--format", "json"},
			checkFn: func(t *testing.T, args []string) {
				if argvContains(args, "--print-logs") {
					t.Fatalf("--print-logs leaks harness logs to the client: %v", args)
				}
			},
		},
		{
			name: "opencode: --session resumes, options before it",
			h:    NewOpenCode(models),
			req:  RunRequest{Input: "hello", Model: "m1", NativeSessionID: "s1"},
			want: []string{"run", "--format", "json", "--model", "m1", "--session", "s1"},
		},
		{
			name: "opencode: a hyphen-leading prompt never reaches argv",
			h:    NewOpenCode(models),
			req:  RunRequest{Input: "--help"},
			checkFn: func(t *testing.T, args []string) {
				// Verified by execution: `opencode run "--help"` prints usage
				// and runs nothing, because `message` is a yargs positional and
				// yargs parses a leading hyphen as an option. `--` does protect
				// it, but it also swallows every flag after it — `opencode run
				// -- "--help" --model m1` sent `--model m1` to the model as part
				// of the message. Stdin is the only form that carries an
				// arbitrary prompt without either failure.
				if argvContains(args, "--help") {
					t.Fatalf("`opencode run --help` prints usage instead of running: %v", args)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, err := tc.h.BuildArgs(tc.req)
			if err != nil {
				t.Fatalf("BuildArgs: %v", err)
			}
			if tc.want != nil {
				if strings.Join(args, " ") != strings.Join(tc.want, " ") {
					t.Fatalf("argv =\n  %v\nwant\n  %v", args, tc.want)
				}
			}
			if tc.checkFn != nil {
				tc.checkFn(t, args)
			}
		})
	}
}

// The invariant differs by delivery mode, and stating it precisely matters:
//
//   - PromptStdin: argv must not depend on the prompt at all. Comparing argv
//     for a hostile prompt against argv for an empty one proves the prompt
//     never reaches the command line, and cannot be fooled by a flag the
//     harness legitimately passes that happens to look like the prompt.
//   - PromptArgs: the prompt must be delivered, but never as its own argv
//     element, or the CLI parses it as an option.
//
// A blanket "put -- before the prompt" rule would appear to satisfy this while
// actually leaving grok and pi injectable — verified against the real CLIs.
func TestPromptNeverBecomesAnOption(t *testing.T) {
	hostile := []string{"--help", "--dangerously-skip-permissions", "-p", "--model=evil", "-"}

	for _, h := range []*CLIHarness{
		NewClaude(nil), NewCodex(nil), NewGrok(nil), NewOpenCode(nil), NewPi(nil),
	} {
		base, err := h.BuildArgs(RunRequest{Input: ""})
		if err != nil {
			t.Fatalf("%s BuildArgs(empty): %v", h.ID, err)
		}

		for _, input := range hostile {
			args, err := h.BuildArgs(RunRequest{Input: input})
			if err != nil {
				t.Fatalf("%s BuildArgs(%q): %v", h.ID, input, err)
			}

			switch h.Prompt {
			case PromptStdin:
				if strings.Join(args, "\x00") != strings.Join(base, "\x00") {
					t.Errorf("%s declares PromptStdin but argv changed with the prompt %q:\n got  %v\n want %v",
						h.ID, input, args, base)
				}
			case PromptArgs:
				if argvContains(args, input) {
					t.Errorf("%s: prompt %q appears as its own argv element, so the CLI will parse it as an option: %v",
						h.ID, input, args)
				}
				delivered := false
				for _, a := range args {
					if strings.Contains(a, input) {
						delivered = true
						break
					}
				}
				if !delivered {
					t.Errorf("%s declares PromptArgs but prompt %q is nowhere in argv: %v", h.ID, input, args)
				}
			default:
				t.Errorf("%s has no prompt delivery mode set", h.ID)
			}
		}
	}
}

// Every openCode*Event below is a verbatim line of `opencode run --format
// json` stdout, captured 2026-08-21 from opencode 1.14.41. Same rule as the
// model fixtures in models_test.go: a line written from memory would only move
// the guess issue #13 exists to remove into the tests and let them pass.
const (
	openCodeStepStartEvent  = `{"type":"step_start","timestamp":1787318971761,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024832d6d001FAOns2VqM4At63","messageID":"msg_024832481001VmFtd8heERcvNR","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"step-start"}}`
	openCodeTextEvent       = `{"type":"text","timestamp":1787318979538,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024834b9b001bJLaBBTnOHp9xX","messageID":"msg_0248341b4001EkO13YqlWp7fMo","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"text","text":"It printed ` + "`HELLO_PROBE`" + `.","time":{"start":1787318979483,"end":1787318979537}}}`
	openCodeStepFinishEvent = `{"type":"step_finish","timestamp":1787318979539,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024834bd20019utsDe30f0ZU1x","reason":"stop","messageID":"msg_0248341b4001EkO13YqlWp7fMo","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"step-finish","tokens":{"total":22547,"input":124,"output":23,"reasoning":0,"cache":{"write":0,"read":22400}},"cost":0}}`
	openCodeToolUseEvent    = `{"type":"tool_use","timestamp":1787318976945,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"type":"tool","tool":"bash","callID":"call_f402fe9305654968b0fa370f","state":{"status":"completed","input":{"command":"echo HELLO_PROBE"},"output":"HELLO_PROBE\n"},"id":"prt_024832e69001tPw4Ou3E3bl3vX","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","messageID":"msg_024832481001VmFtd8heERcvNR"}}`
	openCodeErrorEvent      = `{"type":"error","timestamp":1787319016730,"sessionID":"ses_fdb7c234fffeJRICyB0b8VAws7","error":{"name":"UnknownError","data":{"message":"Model not found: bogus/nope."}}}`

	// The two text parts of one run that said "Alpha", ran a bash tool, then
	// said "Gamma". Neither part carries a separator of its own, and opencode's
	// own renderer printed them as two lines.
	openCodeTextEventAlpha = `{"type":"text","timestamp":1787319916361,"sessionID":"ses_fdb6e87f3ffe2R25Auz5EtTG9f","part":{"id":"prt_02491958f001oo9nHhn2lE4oD2","messageID":"msg_024917866001BhYdOvTD7p08Wf","sessionID":"ses_fdb6e87f3ffe2R25Auz5EtTG9f","type":"text","text":"Alpha","time":{"start":1787319915919,"end":1787319916359}}}`
	openCodeTextEventGamma = `{"type":"text","timestamp":1787319919562,"sessionID":"ses_fdb6e87f3ffe2R25Auz5EtTG9f","part":{"id":"prt_02491a3a60011VcKInj9WEnIDI","messageID":"msg_02491974d001NZ2Zx8KhHXi93i","sessionID":"ses_fdb6e87f3ffe2R25Auz5EtTG9f","type":"text","text":"Gamma","time":{"start":1787319919526,"end":1787319919561}}}`
)

func TestParseOpenCodeLine(t *testing.T) {
	t.Run("step_start yields the native session id", func(t *testing.T) {
		got := parseOpenCodeLine(openCodeStepStartEvent)
		if len(got) != 1 || got[0].Type != UpdateSessionID {
			t.Fatalf("got %+v, want one %s update", got, UpdateSessionID)
		}
		if got[0].SessionID != "ses_fdb7cdbe1ffeFKmuIMghX7MCis" {
			t.Errorf("session id = %q", got[0].SessionID)
		}
	})

	t.Run("text yields the answer", func(t *testing.T) {
		got := parseOpenCodeLine(openCodeTextEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "It printed `HELLO_PROBE`.\n" {
			t.Errorf("delta = %q", got[0].Delta)
		}
	})

	// A run that interleaves prose with tool calls emits one text part per
	// stretch of prose, and no part carries a separator of its own. Deltas are
	// concatenated into a single output_text, so emitting them unchanged runs
	// two sentences together — "Alpha" and "Gamma" become "AlphaGamma". The
	// separator is a newline because that is what opencode's own renderer
	// prints between the same two parts.
	t.Run("consecutive text parts do not run together", func(t *testing.T) {
		var answer string
		for _, line := range []string{openCodeTextEventAlpha, openCodeTextEventGamma} {
			for _, upd := range parseOpenCodeLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "Alpha\nGamma\n" {
			t.Errorf("answer = %q, want %q", answer, "Alpha\nGamma\n")
		}
	})

	// An `error` event is the only signal that a run failed: opencode exits 0
	// after printing one, so a harness that ignored it would report a task that
	// never ran as completed with empty output. Verified by execution —
	// `opencode run --model bogus/nope` prints this line and exits 0.
	t.Run("error fails the run, carrying the CLI's own words", func(t *testing.T) {
		got := parseOpenCodeLine(openCodeErrorEvent)
		if len(got) != 1 || got[0].Type != UpdateFailed {
			t.Fatalf("got %+v, want one %s update", got, UpdateFailed)
		}
		if got[0].Err == nil {
			t.Fatal("failure carries no error")
		}
		if !strings.Contains(got[0].Err.Error(), "Model not found: bogus/nope.") {
			t.Errorf("error drops the CLI's message: %v", got[0].Err)
		}
	})

	// step_finish is per step, not per run — a two-step run emits two, and the
	// second reported input=124 against the first's 18354. task_service applies
	// usage last-write-wins, so emitting it would publish one step's tokens as
	// the run's total. UHP allows a null usage; it does not allow a wrong one.
	t.Run("nothing is invented from events that carry no answer", func(t *testing.T) {
		for _, line := range []string{openCodeStepFinishEvent, openCodeToolUseEvent} {
			if got := parseOpenCodeLine(line); len(got) != 0 {
				t.Errorf("line produced %+v, want nothing:\n  %s", got, line)
			}
		}
	})

	t.Run("a non-JSON line is not answer text", func(t *testing.T) {
		for _, line := range []string{"", "not json", "▀▀▀▀ █▀▀▀ ▀▀▀▀"} {
			if got := parseOpenCodeLine(line); len(got) != 0 {
				t.Errorf("%q produced %+v, want nothing", line, got)
			}
		}
	})
}

func TestValidateModel(t *testing.T) {
	h := NewClaude([]string{"claude-sonnet-4.6"})

	if err := h.validateModel(""); err != nil {
		t.Errorf("empty model must mean the harness default, got %v", err)
	}
	if err := h.validateModel("claude-sonnet-4.6"); err != nil {
		t.Errorf("advertised model rejected: %v", err)
	}
	err := h.validateModel("gpt-4")
	if err == nil {
		t.Fatal("a model the harness does not advertise was accepted")
	}
	if !errors.Is(err, ErrUnsupportedModel) {
		t.Errorf("error does not wrap ErrUnsupportedModel: %v", err)
	}
}

// Run must reject an unsupported model before spawning anything, so the client
// gets a fast structured error rather than a CLI failure minutes later.
func TestRunRejectsUnsupportedModelWithoutSpawning(t *testing.T) {
	h := NewGrok([]string{"grok-4.1"})
	// A binary that does not exist: if validation is skipped, Run reaches
	// exec and fails with a different, "executable not found" error.
	h.Binary = "uhp-go-no-such-binary"
	h.Build()

	_, err := h.Run(context.Background(), RunRequest{TaskID: "t", Input: "hi", Model: "not-a-model"})
	if !errors.Is(err, ErrUnsupportedModel) {
		t.Fatalf("expected ErrUnsupportedModel before spawn, got %v", err)
	}
}

// Harness ids must be stable across process restarts: clients store them.
func TestHarnessIDsAreStableAndPrefixed(t *testing.T) {
	for _, base := range []string{"claude-code", "codex", "grok-cli", "opencode", "pi"} {
		a, b := NewID(base), NewID(base)
		if a != b {
			t.Fatalf("NewID(%q) is not deterministic: %s vs %s", base, a, b)
		}
		if !strings.HasPrefix(a, "chrn_") {
			t.Errorf("NewID(%q) = %q, must be chrn_-prefixed", base, a)
		}
	}
	if NewID("codex") == NewID("claude-code") {
		t.Fatal("different bases produced the same id")
	}
}

// A harness must be reachable by its canonical id and by its friendly base
// name, so `{"harness_id":"claude-code"}` keeps working.
func TestRegistryResolvesIDAndAlias(t *testing.T) {
	r := NewRegistry()
	h := NewClaude([]string{"m"})
	r.Register(h)

	if _, ok := r.Get(h.ID); !ok {
		t.Error("harness not reachable by its canonical chrn_ id")
	}
	if _, ok := r.Get("claude-code"); !ok {
		t.Error("harness not reachable by its base name alias")
	}
	if got, ok := r.Resolve("claude-code"); !ok || got != h.ID {
		t.Errorf("Resolve(alias) = %q,%v; want %q,true", got, ok, h.ID)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("unknown harness resolved")
	}
}

func TestRegistryListIsOrdered(t *testing.T) {
	r := NewRegistry()
	for _, h := range []*CLIHarness{
		NewPi(nil), NewClaude(nil), NewGrok(nil), NewCodex(nil), NewOpenCode(nil),
	} {
		r.Register(h)
	}
	// Repeated calls must agree; ranging a map does not.
	first := r.List()
	for i := 0; i < 20; i++ {
		got := r.List()
		for j := range got {
			if got[j].ID != first[j].ID {
				t.Fatalf("List order is not stable: %v vs %v", ids(got), ids(first))
			}
		}
	}
	// Ordered by the canonical chrn_ id, which is what a client sees.
	got := ids(first)
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("List is not sorted by id: %v", got)
		}
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 harnesses, got %d", len(got))
	}
}

func ids(hs []domain.Harness) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.ID)
	}
	return out
}

// The capability list is no longer decoration: the router refuses a cancel for
// a harness that does not advertise `cancellation` and a continuation for one
// that does not advertise `sessions`. So a declaration that under-claims now
// costs a client a feature that works, and one that over-claims costs it a
// promise that does not — and both are checkable from the declaration itself.

// Cancellation belongs to the shared runner, not to any CLI: every harness is
// started in its own process group and stopped by killing it. Build adds it for
// that reason, and this is the check that it reaches what a client actually
// sees — Info, not the declaration — because a declaration that lost it would
// refuse a cancel that would in fact have worked.
func TestEveryCLIHarnessAdvertisesCancellation(t *testing.T) {
	for _, h := range allCLIHarnesses() {
		if !h.Info().HasCapability(domain.CapCancellation) {
			t.Errorf("%s does not advertise %q, but the shared runner cancels it by killing its process group",
				h.Base, domain.CapCancellation)
		}
	}
}

// Resuming needs the native session id to reach argv. A harness that advertises
// `sessions` and builds identical arguments with and without one cannot resume,
// and every continuation sent to it silently starts a new conversation.
func TestAdvertisedSessionsReachArgv(t *testing.T) {
	for _, h := range allCLIHarnesses() {
		if !domain.HasCapability(h.Capabilities, domain.CapSessions) {
			continue
		}
		fresh, err := h.BuildArgs(RunRequest{Input: "hello"})
		if err != nil {
			t.Fatalf("%s BuildArgs: %v", h.Base, err)
		}
		resumed, err := h.BuildArgs(RunRequest{Input: "hello", NativeSessionID: "s1"})
		if err != nil {
			t.Fatalf("%s BuildArgs(resume): %v", h.Base, err)
		}
		if strings.Join(fresh, "\x00") == strings.Join(resumed, "\x00") {
			t.Errorf("%s advertises %q but its argv is unchanged by a native session id: %v",
				h.Base, domain.CapSessions, resumed)
		}
	}
}

func allCLIHarnesses() []*CLIHarness {
	return []*CLIHarness{NewClaude(nil), NewCodex(nil), NewGrok(nil), NewOpenCode(nil), NewPi(nil)}
}
