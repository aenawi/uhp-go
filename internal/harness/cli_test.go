package harness

import (
	"context"
	"errors"
	"os"
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
			// `--strict-mcp-config` is in the base invocation rather than
			// beside `--mcp-config`, and unconditionally (#19). Without it
			// Claude Code also loads the host's own MCP configurations, so the
			// run's MCP surface is whatever the machine happens to have plus
			// whatever the harness configured — a superset the operator never
			// authorised, and the route by which a server they disabled is
			// contacted anyway. A harness with *no* MCP servers is the case
			// MCPArgs cannot cover, because it is never called for one.
			name: "claude: --verbose is mandatory with stream-json, MCP is confined to ours",
			h:    NewClaude(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config"},
		},
		{
			name: "claude: model and resume",
			h:    NewClaude(models),
			req:  RunRequest{Input: "hello", Model: "m1", NativeSessionID: "s1"},
			want: []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--strict-mcp-config", "--model", "m1", "--resume", "s1"},
		},
		{
			// Issue #14. Without it stream-json emits one event per finished
			// assistant message, so a one-message answer is a single event
			// delivered at the end — a buffered stream wearing a streaming
			// harness's capability list.
			name: "claude: partial messages are what makes the stream progressive",
			h:    NewClaude(models),
			req:  RunRequest{Input: "hello"},
			checkFn: func(t *testing.T, args []string) {
				if !argvContains(args, "--include-partial-messages") {
					t.Fatalf("claude advertises streaming but asks for whole messages: %v", args)
				}
			},
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
			// `--mode json` is not optional, for the same reason opencode's
			// `--format json` is not. Issue #14: pi's default text mode prints
			// nothing until the run is over — its own `runPrintMode` writes the
			// last assistant message only after `session.prompt()` resolves —
			// so the previous invocation advertised `streaming` and buffered.
			name: "pi: -p --mode json, and no bogus run subcommand",
			h:    NewPi(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"-p", "--mode", "json"},
			checkFn: func(t *testing.T, args []string) {
				if argvContains(args, "run") {
					t.Fatalf("pi has no run subcommand, but argv has one: %v", args)
				}
			},
		},
		{
			name: "pi: model is passed after the output mode",
			h:    NewPi(models),
			req:  RunRequest{Input: "hello", Model: "m2"},
			want: []string{"-p", "--mode", "json", "--model", "m2"},
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
				// Verified by execution on 1.14.41 and again on 1.18.21:
				// `opencode run "--help"` prints usage and runs nothing,
				// because `message` is a yargs positional and yargs parses a
				// leading hyphen as an option. `--` does protect it, but it
				// also swallows every flag after it — `opencode run --format
				// json -- "<prompt>" --model opencode/hy3-free` came back with
				// the model echoing "--model opencode/hy3-free" as part of the
				// message it had been sent. Stdin is the only form that carries
				// an arbitrary prompt without either failure.
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
// json` stdout, captured 2026-08-21 from opencode 1.14.41 unless the name says
// otherwise. Same rule as the model fixtures in models_test.go: a line written
// from memory would only move the guess issue #13 exists to remove into the
// tests and let them pass.
//
// Issue #13 was re-run against opencode 1.18.21 on 2026-08-22 and every event
// shape below still parses unchanged. The one thing that did change there is
// how a failed run exits, which no event carries — see
// TestOpenCodeErrorBeatsTheExitCode.
const (
	openCodeStepStartEvent  = `{"type":"step_start","timestamp":1787318971761,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024832d6d001FAOns2VqM4At63","messageID":"msg_024832481001VmFtd8heERcvNR","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"step-start"}}`
	openCodeTextEvent       = `{"type":"text","timestamp":1787318979538,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024834b9b001bJLaBBTnOHp9xX","messageID":"msg_0248341b4001EkO13YqlWp7fMo","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"text","text":"It printed ` + "`HELLO_PROBE`" + `.","time":{"start":1787318979483,"end":1787318979537}}}`
	openCodeStepFinishEvent = `{"type":"step_finish","timestamp":1787318979539,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"id":"prt_024834bd20019utsDe30f0ZU1x","reason":"stop","messageID":"msg_0248341b4001EkO13YqlWp7fMo","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","type":"step-finish","tokens":{"total":22547,"input":124,"output":23,"reasoning":0,"cache":{"write":0,"read":22400}},"cost":0}}`
	openCodeToolUseEvent    = `{"type":"tool_use","timestamp":1787318976945,"sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","part":{"type":"tool","tool":"bash","callID":"call_f402fe9305654968b0fa370f","state":{"status":"completed","input":{"command":"echo HELLO_PROBE"},"output":"HELLO_PROBE\n"},"id":"prt_024832e69001tPw4Ou3E3bl3vX","sessionID":"ses_fdb7cdbe1ffeFKmuIMghX7MCis","messageID":"msg_024832481001VmFtd8heERcvNR"}}`
	openCodeErrorEvent      = `{"type":"error","timestamp":1787319016730,"sessionID":"ses_fdb7c234fffeJRICyB0b8VAws7","error":{"name":"UnknownError","data":{"message":"Model not found: bogus/nope."}}}`

	// The same failure on opencode 1.18.21, captured 2026-08-22 from
	// `opencode run --format json --model bogus/nope`. The message no longer
	// names the model: `--model opencode/nonexistent-xyz` produced this same
	// sentence, differing only in `ref`. Two failures is not the whole of
	// opencode's error vocabulary, but it is enough to establish that `ref` can
	// be the only field telling one from another.
	openCodeRefErrorEvent = `{"type":"error","timestamp":1787378638332,"sessionID":"ses_fd7ee637effe22QKKO5wjJW274","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_7879a6cf"}}}`

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

	// An `error` event is what carries the reason a run failed. On 1.14.41 it
	// was also the only signal that one had: opencode exited 0 after printing
	// it, so a harness that ignored it reported a task that never ran as
	// completed with empty output. On 1.18.21 opencode exits 1 as well, so the
	// run fails either way — but an exit code has no words in it, and these are
	// the CLI's own. Verified by execution on both versions.
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

	// On 1.18.21 the message stopped being the reason. Every failure now says
	// "Unexpected server error. Check server logs for details.", so passing it
	// through alone hands the client a sentence that is true of every failure
	// and identifies none of them — including two different bad models, which
	// produced this text twice and differed only in `ref`. `ref` is also the
	// token the message's own advice needs: "check server logs" is unfollowable
	// without something to look for.
	t.Run("a 1.18 error keeps the ref that is all that distinguishes it", func(t *testing.T) {
		got := parseOpenCodeLine(openCodeRefErrorEvent)
		if len(got) != 1 || got[0].Type != UpdateFailed {
			t.Fatalf("got %+v, want one %s update", got, UpdateFailed)
		}
		msg := got[0].Err.Error()
		if !strings.Contains(msg, "Unexpected server error.") {
			t.Errorf("error drops the CLI's message: %v", msg)
		}
		if !strings.Contains(msg, "err_7879a6cf") {
			t.Errorf("error drops the ref, leaving nothing to tell this failure from any other: %v", msg)
		}
	})

	// The ref is an addition to the reason, never a replacement for it. These
	// four are the whole cross-product of (reason present?) x (ref present?),
	// because the last of them is the only branch that constructs a sentence of
	// its own and nothing observed on either version reaches it: opencode has
	// always sent a `name`, so `message` being empty falls back to that rather
	// than to nothing. It is written down because dropping a ref that arrived
	// alone is the one way this function can destroy the only identifying
	// thing it was given.
	t.Run("ref is appended to the reason, never substituted for it", func(t *testing.T) {
		for _, tc := range []struct{ name, line, want string }{
			{
				name: "reason and ref: reason leads, ref trails",
				line: `{"type":"error","error":{"name":"UnknownError","data":{"message":"Model not found: bogus/nope.","ref":"err_abc123"}}}`,
				want: "harness: opencode: Model not found: bogus/nope. (ref: err_abc123)",
			},
			{
				name: "reason, no ref: unchanged from 1.14.41",
				line: `{"type":"error","error":{"name":"UnknownError","data":{"message":"Model not found: bogus/nope."}}}`,
				want: "harness: opencode: Model not found: bogus/nope.",
			},
			{
				name: "no message, no ref: the error class is the reason",
				line: `{"type":"error","error":{"name":"UnknownError","data":{}}}`,
				want: "harness: opencode: UnknownError",
			},
			{
				name: "ref alone: borrows harnessFailure's sentence rather than a second copy",
				line: `{"type":"error","error":{"name":"","data":{"ref":"err_abc123"}}}`,
				want: "harness: opencode: " + failureWithoutReason + " (ref: err_abc123)",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := parseOpenCodeLine(tc.line)
				if len(got) != 1 || got[0].Type != UpdateFailed {
					t.Fatalf("got %+v, want one %s update", got, UpdateFailed)
				}
				if msg := got[0].Err.Error(); msg != tc.want {
					t.Errorf("error =\n  %q\nwant\n  %q", msg, tc.want)
				}
			})
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

// Every claude*Event below is a line of `claude -p --output-format stream-json
// --verbose --include-partial-messages`, abridged to the fields this parser
// reads: a real init line is 2.6 KB of tool and slash-command inventory no
// adapter looks at, and a real result line carries twenty keys of cost and
// timing telemetry alongside the four usage fields.
//
// The stream_event shape below was once the weakest claim in this repository.
// It could not be captured when it was written — no logged-in Claude Code was
// reachable — so it was read out of the 2.1.238 binary with `strings`, which
// carries both the envelope and a literal example of the event inside it. A
// wrong guess there fails silently: no line matches, the run completes, the
// client is handed "". That is issue #32.
//
// It has now been captured. `make capture-claude` against Claude Code 2.1.240
// on 2026-08-23 produced, verbatim:
//
//	{"type":"stream_event","event":{"type":"content_block_delta","index":0,
//	 "delta":{"type":"text_delta","text":"1"}},"session_id":"4fcaf1d8-…",
//	 "parent_tool_use_id":null,"uuid":"9a8b3324-…"}
//
// which is the guessed shape key for key. The guess was right, and is no longer
// a guess. The literals below keep their original session id and "Hello" text
// rather than being restamped with the capture's: what was unverified was the
// shape, the shape is what the capture settles, and rewriting the values would
// churn every assertion in this file to prove nothing further.
const (
	claudeInitEvent = `{"type":"system","subtype":"init","cwd":"/w","session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","tools":["Bash","Edit","Read"],"mcp_servers":[],"model":"claude-opus-5","permissionMode":"default"}`

	claudeTextDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}},"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","parent_tool_use_id":null,"uuid":"ebccd3b6-6744-4001-ba9c-aa2a9a65ea5a"}`

	// Reasoning, not answer. Streaming it would publish the model's private
	// working as the task's output_text.
	claudeThinkingDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"weighing it up"}},"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","parent_tool_use_id":null,"uuid":"c1a0a0f0-0000-4000-8000-000000000001"}`

	// A tool call's arguments arriving a fragment at a time.
	claudeToolInputDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\""}},"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","parent_tool_use_id":null,"uuid":"c1a0a0f0-0000-4000-8000-000000000002"}`

	// The whole assistant message, repeated after its deltas have all arrived.
	claudeAssistantEvent = `{"type":"assistant","message":{"id":"msg_01","role":"assistant","model":"claude-opus-5","type":"message","content":[{"type":"text","text":"Hello, world"}],"stop_reason":"end_turn"},"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814"}`

	claudeResultEvent = `{"type":"result","subtype":"success","is_error":false,"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","usage":{"input_tokens":12,"cache_creation_input_tokens":7,"cache_read_input_tokens":22400,"output_tokens":5},"result":"Hello, world","duration_ms":1131,"num_turns":1}`

	claudeErrorResultEvent = `{"type":"result","subtype":"success","is_error":true,"session_id":"45b84817-020e-4a0b-9c94-93a0106c5814","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0},"result":"Not logged in · Please run /login","duration_ms":113,"num_turns":1}`
)

func TestParseClaudeLine(t *testing.T) {
	t.Run("init yields the native session id, once", func(t *testing.T) {
		got := parseClaudeLine(claudeInitEvent)
		if len(got) != 1 || got[0].Type != UpdateSessionID {
			t.Fatalf("got %+v, want one %s update", got, UpdateSessionID)
		}
		if got[0].SessionID != "45b84817-020e-4a0b-9c94-93a0106c5814" {
			t.Errorf("session id = %q", got[0].SessionID)
		}
	})

	t.Run("a text delta is the answer arriving", func(t *testing.T) {
		got := parseClaudeLine(claudeTextDeltaEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "Hello" {
			t.Errorf("delta = %q, want %q", got[0].Delta, "Hello")
		}
	})

	// The whole point of issue #14. `--include-partial-messages` does not
	// replace the finished `assistant` message, it precedes it — so a parser
	// that reads both publishes every answer twice. Deltas are the progressive
	// half, so they are the half that is read.
	t.Run("the finished assistant message is not answer text as well", func(t *testing.T) {
		var answer string
		for _, line := range []string{
			claudeTextDeltaEvent, claudeTextDeltaEvent, claudeAssistantEvent,
		} {
			for _, upd := range parseClaudeLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "HelloHello" {
			t.Errorf("answer = %q, want %q — the finished message is being read on top of its own deltas",
				answer, "HelloHello")
		}
	})

	t.Run("thinking and tool arguments are not answer text", func(t *testing.T) {
		for _, line := range []string{claudeThinkingDeltaEvent, claudeToolInputDeltaEvent} {
			if got := parseClaudeLine(line); len(got) != 0 {
				t.Errorf("line produced %+v, want nothing:\n  %s", got, line)
			}
		}
	})

	t.Run("the result event carries the run totals", func(t *testing.T) {
		got := parseClaudeLine(claudeResultEvent)
		if len(got) != 1 || got[0].Type != UpdateUsage || got[0].Usage == nil {
			t.Fatalf("got %+v, want one %s update", got, UpdateUsage)
		}
		want := domain.Usage{
			InputTokens: 12, OutputTokens: 5, TotalTokens: 17,
			CacheReadTokens: 22400, CacheWriteTokens: 7,
		}
		if *got[0].Usage != want {
			t.Errorf("usage = %+v, want %+v", *got[0].Usage, want)
		}
	})

	// Reading the deltas rather than the finished message costs the one thing
	// the finished message used to supply on a failed run: its words. claude
	// exits 1 here, so the run already fails — but with an empty stderr and
	// therefore "exit status 1" as the whole explanation. The result event is
	// where the CLI actually says what went wrong.
	t.Run("a failed result fails the run in the CLI's own words", func(t *testing.T) {
		got := parseClaudeLine(claudeErrorResultEvent)
		var failed *RunUpdate
		for i := range got {
			if got[i].Type == UpdateFailed {
				failed = &got[i]
			}
		}
		if failed == nil {
			t.Fatalf("got %+v, want a %s update", got, UpdateFailed)
		}
		if failed.Err == nil || !strings.Contains(failed.Err.Error(), "Not logged in") {
			t.Errorf("failure drops the CLI's message: %v", failed.Err)
		}
	})

	t.Run("a non-JSON line is not answer text", func(t *testing.T) {
		for _, line := range []string{"", "not json", "[2026-08-21] warming up"} {
			if got := parseClaudeLine(line); len(got) != 0 {
				t.Errorf("%q produced %+v, want nothing", line, got)
			}
		}
	})
}

// The delta shape above cannot be verified by `go test`: it needs a logged-in
// Claude Code, which a test process does not have. scripts/capture-claude-
// stream.py is that verification, run by hand on a maintainer's machine
// (`make capture-claude`).
//
// This is the one part of it a test *can* hold: that the probe runs the argv
// uhpd ships. A probe measuring a different invocation would report a healthy
// stream for a command nothing sends, which is a more confident version of the
// gap #32 is about — so the two are pinned together here rather than by a
// comment asking someone to remember.
func TestClaudeProbeRunsTheShippedInvocation(t *testing.T) {
	args, err := NewClaude([]string{"m1"}).BuildArgs(RunRequest{Input: "hello"})
	if err != nil {
		t.Fatalf("BuildArgs: %v", err)
	}
	want := append([]string{"claude"}, args...)

	// Both probes, because both launch the CLI themselves and either one can go
	// stale alone.
	for _, probe := range []string{
		"../../scripts/capture-claude-stream.py",
		"../../scripts/probe-claude-delivery.py",
	} {
		src, err := os.ReadFile(probe)
		if err != nil {
			t.Fatalf("a claude probe is missing: %v", err)
		}
		got := pythonStringList(t, string(src), "HARNESS_ARGV")
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s measures an invocation uhpd does not send:\n  probe: %v\n  uhpd:  %v",
				probe, got, want)
		}
	}
}

// TestClaudeDeliveryProbeRunsTheShippedFlags pins scripts/probe-claude-
// delivery.py to the two hooks it exists to verify, for the same reason
// TestClaudeProbeRunsTheShippedInvocation pins the stream probe to BuildArgs.
//
// Issue #19: `--disallowedTools` and `--mcp-config` were declared from
// documentation and never executed. They are executed now, but only by a probe
// on a maintainer's machine — so the thing a test can hold is that the probe
// spells the flags the way the adapter does. A probe that verified
// `--disallowed-tools` (grok's spelling) would report a working block for a
// flag uhpd never sends.
//
// The placeholders are what the probe substitutes at run time, and they go
// through the real hooks so a change to either — a reordering, an added flag, a
// different separator — lands here rather than in a silently stale probe.
func TestClaudeDeliveryProbeRunsTheShippedFlags(t *testing.T) {
	const probe = "../../scripts/probe-claude-delivery.py"

	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("the claude delivery probe is missing: %v", err)
	}
	h := NewClaude([]string{"m1"})

	for _, tc := range []struct {
		list string
		want []string
	}{
		{"MCP_ARGV", h.MCPArgs("<config>")},
		{"DISALLOW_ARGV", h.DisallowArgs([]string{"<tools>"})},
	} {
		got := pythonStringList(t, string(src), tc.list)
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: %s measures flags uhpd does not send:\n  probe: %v\n  uhpd:  %v",
				tc.list, probe, got, tc.want)
		}
	}
}

// TestClaudeDisabledToolsAreOneCommaJoinedValue pins the separator.
//
// `claude --help` documents "Comma or space-separated", and the two are not
// interchangeable here: `--disallowedTools <tools...>` is variadic, so
// space-separating would spread the list across argv elements and let the
// variadic keep eating until the next option. Comma-joining keeps the whole
// list in one element, which is why a tool name is never mistaken for the
// value of a flag that follows. Verified by execution 2026-08-23 against
// 2.1.240: `--disallowedTools Bash,Read` removed both.
func TestClaudeDisabledToolsAreOneCommaJoinedValue(t *testing.T) {
	got := NewClaude([]string{"m1"}).DisallowArgs([]string{"Bash", "Read"})
	want := []string{"--disallowedTools", "Bash,Read"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("DisallowArgs = %v, want %v", got, want)
	}
}

// TestClaudeMcpConfigIsTheOnlyMcpConfig is issue #19's §4.1 half.
//
// The generated document holds the enabled servers and only those — the service
// filters the disabled ones out before writing it (TestOnlyEnabledMcpServersReach
// TheConfig). That is necessary and, on its own, not sufficient: `--mcp-config`
// *adds* a configuration rather than replacing the set, so without
// `--strict-mcp-config` the host's own MCP servers are connected too. Verified
// by execution 2026-08-23: a server named only in the working directory's
// `.mcp.json` was contacted (initialize, tools/list) and its tool advertised to
// the model, on a run whose `--mcp-config` did not mention it. With the flag,
// that server's log stayed empty.
//
// So the two flags have to travel together, and this checks the composed argv
// rather than MCPArgs alone: argsFor is where they meet.
func TestClaudeMcpConfigIsTheOnlyMcpConfig(t *testing.T) {
	args, err := NewClaude([]string{"m1"}).argsFor(RunRequest{
		Input:         "hello",
		McpConfigPath: "/w/.uhp/mcp.json",
	})
	if err != nil {
		t.Fatalf("argsFor: %v", err)
	}
	if i := index(args, "--mcp-config"); i < 0 || i+1 >= len(args) || args[i+1] != "/w/.uhp/mcp.json" {
		t.Fatalf("the generated config never reaches the CLI: %v", args)
	}
	if !argvContains(args, "--strict-mcp-config") {
		t.Fatalf("the host's MCP servers are connected alongside the configured ones: %v", args)
	}
}

// pythonStringList reads a module-level list of string literals out of a Python
// source file. Deliberately dumb: it handles the one literal form the probe
// uses and fails loudly on anything else, rather than quietly matching less.
func pythonStringList(t *testing.T, src, name string) []string {
	t.Helper()

	start := strings.Index(src, name+" = [")
	if start < 0 {
		t.Fatalf("no %s list found", name)
	}
	body := src[start+len(name+" = ["):]
	end := strings.Index(body, "]")
	if end < 0 {
		t.Fatalf("%s list is not terminated", name)
	}

	var out []string
	for _, field := range strings.Split(body[:end], ",") {
		field = strings.TrimSpace(field)
		// Comment or trailing-comma blank; the probe has neither inside the
		// list, so anything here is a form this parser was not built for.
		if field == "" {
			continue
		}
		if len(field) < 2 || field[0] != '"' || field[len(field)-1] != '"' {
			t.Fatalf("%s holds something this parser cannot read: %q", name, field)
		}
		out = append(out, field[1:len(field)-1])
	}
	return out
}

// Every pi*Event below is shaped from pi 0.83.0's own event definitions and
// from a captured `pi -p --mode json` run on 2026-08-21.
//
// The lifecycle lines — session, agent_start, message_start, message_end,
// turn_end — are verbatim from that run, abridged to the fields this parser
// reads. The run itself failed at the provider (a groq per-minute token limit),
// which is why it is also the source of the error fixture and why no
// message_update line could be captured from it: pi emits those only once the
// model starts producing text.
//
// message_update is therefore read from the shipped package instead.
// `dist/modes/print-mode.js` writes one JSON line per session event as it
// fires, and `pi-agent-core/dist/types.d.ts` declares that event as
// `{type:"message_update", message, assistantMessageEvent}` where the inner
// event is `{type:"text_delta", contentIndex, delta, partial}`.
//
// The same two files are what settle the mode change: in text mode
// `runPrintMode` writes nothing until `await session.prompt()` has returned and
// then prints the last assistant message, so `pi -p` alone cannot stream
// whatever the harness advertises.
const (
	piSessionEvent   = `{"type":"session","version":3,"id":"01a024aa-0d2e-7755-82ec-18e89a44e099","timestamp":"2026-08-21T14:11:59.406Z","cwd":"/w"}`
	piAgentStart     = `{"type":"agent_start"}`
	piTurnStart      = `{"type":"turn_start"}`
	piUserMessageEnd = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Count from 1 to 30."}],"timestamp":1787321519450}}`

	piTextDeltaEvent = `{"type":"message_update","message":{"role":"assistant","content":[{"type":"text","text":"Al"}]},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Al","partial":{"role":"assistant","content":[{"type":"text","text":"Al"}]}}}`

	piThinkingDeltaEvent = `{"type":"message_update","message":{"role":"assistant","content":[]},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"weighing it up","partial":{"role":"assistant","content":[]}}}`

	// The finished assistant message, carrying the whole answer its deltas
	// already delivered.
	piAssistantMessageEnd = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Alpha"}],"provider":"groq","model":"openai/gpt-oss-20b","stopReason":"stop","timestamp":1787321519971}}`

	// Verbatim from the failed run. pi exits 0 after printing this in json
	// mode — only its text mode turns an error into a non-zero exit — so a
	// harness that ignored it would report a run that never happened as
	// completed with empty output.
	piErrorMessageEnd = `{"type":"message_end","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"groq","model":"openai/gpt-oss-20b","stopReason":"error","timestamp":1787321519971,"errorMessage":"413: Request too large for model ` + "`openai/gpt-oss-20b`" + ` on tokens per minute (TPM): Limit 8000, Requested 66050"}}`

	// The same failure, repeated on the events that follow it.
	piErrorTurnEnd = `{"type":"turn_end","message":{"role":"assistant","content":[],"provider":"groq","model":"openai/gpt-oss-20b","stopReason":"error","timestamp":1787321519971,"errorMessage":"413: Request too large"},"toolResults":[]}`
)

func TestParsePiLine(t *testing.T) {
	t.Run("a text delta is the answer arriving", func(t *testing.T) {
		got := parsePiLine(piTextDeltaEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "Al" {
			t.Errorf("delta = %q, want %q", got[0].Delta, "Al")
		}
	})

	// pi announces each assistant message twice over: as a stream of deltas
	// and again, whole, at message_end. Reading both doubles every answer.
	t.Run("the finished assistant message is not answer text as well", func(t *testing.T) {
		var answer string
		for _, line := range []string{piTextDeltaEvent, piTextDeltaEvent, piAssistantMessageEnd} {
			for _, upd := range parsePiLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "AlAl" {
			t.Errorf("answer = %q, want %q — the finished message is being read on top of its own deltas",
				answer, "AlAl")
		}
	})

	t.Run("thinking is not answer text", func(t *testing.T) {
		if got := parsePiLine(piThinkingDeltaEvent); len(got) != 0 {
			t.Errorf("thinking produced %+v, want nothing", got)
		}
	})

	t.Run("an errored message fails the run, carrying pi's own words", func(t *testing.T) {
		got := parsePiLine(piErrorMessageEnd)
		if len(got) != 1 || got[0].Type != UpdateFailed {
			t.Fatalf("got %+v, want one %s update", got, UpdateFailed)
		}
		if got[0].Err == nil || !strings.Contains(got[0].Err.Error(), "Limit 8000") {
			t.Errorf("error drops pi's message: %v", got[0].Err)
		}
	})

	// The same failure is repeated on turn_end and agent_end. Only one update
	// is emitted for it, from message_end, so a client is not told three times.
	t.Run("the failure is reported once, not on every event repeating it", func(t *testing.T) {
		if got := parsePiLine(piErrorTurnEnd); len(got) != 0 {
			t.Errorf("turn_end produced %+v, want nothing — message_end already reported it", got)
		}
	})

	t.Run("nothing is invented from lifecycle events", func(t *testing.T) {
		for _, line := range []string{piSessionEvent, piAgentStart, piTurnStart, piUserMessageEnd} {
			if got := parsePiLine(line); len(got) != 0 {
				t.Errorf("line produced %+v, want nothing:\n  %s", got, line)
			}
		}
	})

	t.Run("a non-JSON line is not answer text", func(t *testing.T) {
		for _, line := range []string{"", "not json", "  Loading extensions…"} {
			if got := parsePiLine(line); len(got) != 0 {
				t.Errorf("%q produced %+v, want nothing", line, got)
			}
		}
	})
}

// Three adapters now report a failure their CLI printed rather than exited
// with, so the shape lives in one place. What it must never produce is an
// error with nothing in it: "harness: pi: " reads to a client as a bug in this
// server rather than as something the harness said.
func TestHarnessFailureAlwaysSaysSomething(t *testing.T) {
	for _, message := range []string{"", "   ", "\n\t"} {
		upd := harnessFailure("pi", message)
		if upd.Type != UpdateFailed {
			t.Fatalf("type = %s, want %s", upd.Type, UpdateFailed)
		}
		if upd.Err == nil {
			t.Fatalf("%q produced a failure carrying no error", message)
		}
		if !strings.Contains(upd.Err.Error(), "without reporting a reason") {
			t.Errorf("%q produced %q, want a stated reason", message, upd.Err)
		}
	}

	upd := harnessFailure("opencode", "  Model not found: bogus/nope.  ")
	if got := upd.Err.Error(); got != "harness: opencode: Model not found: bogus/nope." {
		t.Errorf("error = %q", got)
	}
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

// The other side of the same rule: a capability somebody else delivers is one
// no declaration may claim.
//
// Three are in that position, for two different reasons.
//
// `cancellation` is the shared runner's, and Build adds it (above). A
// declaration that lists it as well is harmless today, because Build dedupes —
// which is exactly why it needs a test: it is the line a sixth harness copies
// from the fifth, and it reads as though the CLI is the one that implements it.
//
// `files_in` and `files_out` are the router's. service.materializeAttachments
// writes a task's attachments into the session working directory before the
// run, and service.captureArtifacts diffs that directory afterwards; neither
// consults an adapter, so a declaration cannot answer for either. Nor could a
// static one answer honestly: both need a workspace, and a deployment started
// without `UHP_WORKSPACE` delivers neither. The router computes them from the
// same answer the discovery document reports — see
// service.withRouterCapabilities — and this test is what stops a declaration
// contradicting it, which is what `grok-cli` and `pi` used to do by claiming
// they could not produce artifacts while producing them.
//
// It reads what the declaration asked for rather than the finished list,
// because the finished list is where the additions land and a test that read it
// could not tell a claim from a grant.
func TestNoCLIHarnessDeclaresACapabilityItDoesNotDeliver(t *testing.T) {
	notTheirs := []domain.Capability{domain.CapCancellation, domain.CapFilesIn, domain.CapFilesOut}
	for _, h := range allCLIHarnesses() {
		for _, c := range notTheirs {
			if domain.HasCapability(h.declared, c) {
				t.Errorf("%s declares %q, which it does not deliver: %s", h.Base, c,
					"cancellation comes from the shared runner and the file capabilities from the router")
			}
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
