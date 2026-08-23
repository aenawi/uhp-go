package harness

import (
	"context"
	"encoding/json"
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
			want: []string{"-p=hello", "--output-format", "streaming-messages-json", "--include-partial-messages"},
		},
		{
			name: "grok: model and resume",
			h:    NewGrok(models),
			req:  RunRequest{Input: "hello", Model: "m1", NativeSessionID: "s1"},
			want: []string{"-p=hello", "--output-format", "streaming-messages-json", "--include-partial-messages", "--model", "m1", "--resume", "s1"},
		},
		{
			// Issue #34, and the same defect as claude's #14 and pi's: without
			// this flag `streaming-messages-json` emits one whole `assistant`
			// message when the run is already over. Verified by execution on
			// grok 1.0.5 — the same prompt without it produced exactly three
			// lines, `system`, `assistant`, `result`, and no delta at all.
			name: "grok: partial messages are what makes the stream progressive",
			h:    NewGrok(models),
			req:  RunRequest{Input: "hello"},
			checkFn: func(t *testing.T, args []string) {
				if !argvContains(args, "--include-partial-messages") {
					t.Fatalf("grok advertises streaming but asks for whole messages: %v", args)
				}
			},
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
			// Issue #33. `--session-id <id>` and not `--session <id>`: pi has
			// both, and only the first takes the exact id the `session` event
			// announced. `--session` matches a partial UUID or a file path,
			// which is a lookup this server has no reason to ask for when it
			// holds the whole id.
			//
			// Verified by execution on 0.84.2 by scripts/probe-pi-session.py:
			// the resumed turn arrived at the provider carrying the first
			// turn's user message and assistant reply, and the same turn run
			// without the flag arrived carrying neither.
			name: "pi: --session-id resumes, after the model",
			h:    NewPi(models),
			req:  RunRequest{Input: "hello", Model: "m1", NativeSessionID: "s1"},
			want: []string{"-p", "--mode", "json", "--model", "m1", "--session-id", "s1"},
			checkFn: func(t *testing.T, args []string) {
				// `--session` would be read as a different flag entirely, and
				// pi accepts it, so a typo here fails at the model rather than
				// at the CLI.
				if argvContains(args, "--session") {
					t.Fatalf("--session takes a partial id or a path, not the exact id: %v", args)
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

// Every codex*Event below is a verbatim line of `codex exec --json
// --skip-git-repo-check` stdout, captured 2026-08-23 from codex-cli 0.149.0 by
// `make probe-codex`. Codex's lines are short enough to keep whole, unlike
// claude's and grok's, so none of these is abridged.
//
// Issue #34 is why they exist at all. codex.go's claims were carried as
// "verified by execution" with no version beside them and no fixture in the
// tests, which is the state #13 showed has a shelf life: opencode's two
// execution-verified claims were true when written and false one minor version
// later, and nothing noticed. These are the answers 0.149.0 gave.
const (
	codexThreadStartedEvent = `{"type":"thread.started","thread_id":"01a02d69-3740-7d02-8285-5c771191dbb6"}`
	codexTurnStartedEvent   = `{"type":"turn.started"}`
	codexAgentMessageEvent  = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"ALPHA BRAVO CHARLIE"}}`
	codexTurnCompletedEvent = `{"type":"turn.completed","usage":{"input_tokens":18095,"cached_input_tokens":11008,"cache_write_input_tokens":0,"output_tokens":12,"reasoning_output_tokens":0}}`

	// The two agent messages of one run that said "Alpha.", ran a shell
	// command, then said "Gamma.". Neither carries a separator of its own.
	codexAgentMessageAlpha = `{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"Alpha."}}`
	codexAgentMessageGamma = `{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"Gamma."}}`

	// The shell call between them. It is an `item.completed` like the answer
	// is, and its output lives in `aggregated_output` rather than `text`, which
	// is the whole reason reading `item.text` does not publish tool output as
	// the answer.
	codexCommandStartedEvent   = `{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'echo HELLO_PROBE'","aggregated_output":"","exit_code":null,"status":"in_progress"}}`
	codexCommandCompletedEvent = `{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"/bin/zsh -lc 'echo HELLO_PROBE'","aggregated_output":"HELLO_PROBE\n","exit_code":0,"status":"completed"}}`

	// The three lines a failed run prints, from `--model bogus-model-xyz`, in
	// the order codex printed them. Only the last is read as a failure:
	//
	//   - codexErrorItemEvent is an `item.completed` whose item type is `error`
	//     and which carries `message` rather than `text`. It is a warning —
	//     codex attempted the run after printing it.
	//   - codexErrorEvent is the top-level one, carrying the reason verbatim.
	//   - codexTurnFailedEvent repeats that same sentence, and says in its own
	//     name that the turn is over.
	//
	// codex exits 1 as well, so the run fails either way — but an exit code has
	// no words in it, and 400s from the API are the failures a client most
	// needs the words for.
	codexErrorItemEvent  = `{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Model metadata for ` + "`bogus-model-xyz`" + ` not found. Defaulting to fallback metadata; this can degrade performance and cause issues."}}`
	codexErrorEvent      = `{"type":"error","message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'bogus-model-xyz' model is not supported when using Codex with a ChatGPT account.\"}}"}`
	codexTurnFailedEvent = `{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'bogus-model-xyz' model is not supported when using Codex with a ChatGPT account.\"}}"}}`
)

func TestParseCodexLine(t *testing.T) {
	t.Run("thread.started yields the native session id", func(t *testing.T) {
		got := parseCodexLine(codexThreadStartedEvent)
		if len(got) != 1 || got[0].Type != UpdateSessionID {
			t.Fatalf("got %+v, want one %s update", got, UpdateSessionID)
		}
		if got[0].SessionID != "01a02d69-3740-7d02-8285-5c771191dbb6" {
			t.Errorf("session id = %q", got[0].SessionID)
		}
	})

	t.Run("an agent message is the answer", func(t *testing.T) {
		got := parseCodexLine(codexAgentMessageEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "ALPHA BRAVO CHARLIE\n" {
			t.Errorf("delta = %q", got[0].Delta)
		}
	})

	// The same defect TestParseOpenCodeLine names, in the other adapter that
	// emits whole messages: a run that puts a tool call between two sentences
	// sends two `agent_message` items, neither ending in anything, and deltas
	// are concatenated into one output_text. Verified by execution on 0.149.0 —
	// "Alpha." and "Gamma." around one shell call — where the unseparated
	// reading answers "Alpha.Gamma.".
	t.Run("consecutive agent messages do not run together", func(t *testing.T) {
		var answer string
		for _, line := range []string{codexAgentMessageAlpha, codexAgentMessageGamma} {
			for _, upd := range parseCodexLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "Alpha.\nGamma.\n" {
			t.Errorf("answer = %q, want %q", answer, "Alpha.\nGamma.\n")
		}
	})

	t.Run("turn.completed yields the run's usage", func(t *testing.T) {
		got := parseCodexLine(codexTurnCompletedEvent)
		if len(got) != 1 || got[0].Type != UpdateUsage {
			t.Fatalf("got %+v, want one %s update", got, UpdateUsage)
		}
		u := got[0].Usage
		if u.InputTokens != 18095 || u.OutputTokens != 12 || u.TotalTokens != 18107 {
			t.Errorf("usage = %+v", u)
		}
		if u.CacheReadTokens != 11008 || u.CacheWriteTokens != 0 {
			t.Errorf("cache accounting = %+v", u)
		}
	})

	// Issue #34. On 0.149.0 a run that cannot proceed prints its reason and
	// codex exits 1. Before this the line was not read: the client was told
	// "exit status 1" and the sentence naming the actual 400 was dropped on the
	// floor. Same argument as claude's and opencode's, which is why it goes
	// through the same harnessFailure.
	t.Run("a failed turn carries the CLI's own words", func(t *testing.T) {
		got := parseCodexLine(codexTurnFailedEvent)
		if len(got) != 1 || got[0].Type != UpdateFailed {
			t.Fatalf("got %+v, want one %s update", got, UpdateFailed)
		}
		if got[0].Err == nil {
			t.Fatal("failure carries no error")
		}
		if !strings.Contains(got[0].Err.Error(), "is not supported when using Codex with a ChatGPT account") {
			t.Errorf("error drops the CLI's message: %v", got[0].Err)
		}
	})

	// `item.completed` is the answer's event type and also a tool call's and a
	// warning's, so the item type is what separates them. A parser that read
	// every completed item would publish `aggregated_output` — the shell's own
	// stdout — as the model's answer.
	//
	// The last two are the events that look like failures and are not treated
	// as ones. UpdateFailed is terminal — task_service fails the task on it,
	// ahead of the exit code — so a line read as fatal that is not kills a run
	// that would have succeeded. codexErrorItemEvent is the proof that codex
	// has a non-fatal error channel: it is a warning about a run codex went on
	// to attempt. codexErrorEvent carried the same sentence `turn.failed` did,
	// immediately before it, so dropping it costs no words. See parseCodexLine.
	t.Run("nothing is invented from events that carry no answer", func(t *testing.T) {
		for _, line := range []string{
			codexTurnStartedEvent,
			codexCommandStartedEvent,
			codexCommandCompletedEvent,
			codexErrorItemEvent,
			codexErrorEvent,
		} {
			if got := parseCodexLine(line); len(got) != 0 {
				t.Errorf("line produced %+v, want nothing:\n  %s", got, line)
			}
		}
	})

	t.Run("a non-JSON line is not answer text", func(t *testing.T) {
		for _, line := range []string{"", "not json", "Reading prompt from stdin..."} {
			if got := parseCodexLine(line); len(got) != 0 {
				t.Errorf("%q produced %+v, want nothing", line, got)
			}
		}
	})
}

// Every grok*Event below is a line of `grok -p=<prompt> --output-format
// streaming-messages-json --include-partial-messages`, captured 2026-08-23 from
// grok 1.0.5 by `make probe-grok`. The init and result lines are abridged to
// the fields this parser reads — a real init line is 6 KB of slash-command and
// skill inventory, all of it a property of the machine rather than of grok —
// and every other line is verbatim.
//
// Issue #34. None of this format existed in the harness before: grok was run in
// its default `plain` mode and its stdout passed through line by line, so there
// was nothing to capture and nothing to pin.
const (
	grokInitEvent = `{"type":"system","subtype":"init","session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760","apiKeySource":"oauth","model":"grok-4.6","cwd":"/w","permissionMode":"bypassPermissions","tools":["run_terminal_command","read_file"],"mcp_servers":[],"uuid":"71934571-c88c-40b9-a2dd-a96cf7738e2b"}`

	grokTextDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"ALPHA"}},"parent_tool_use_id":null,"session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760","uuid":"10d25a3a-ee81-475e-b974-e86f5527f285"}`

	// Reasoning, not answer. grok streams a great deal of it — thirty deltas
	// against six of answer in the captured run — so a parser that read every
	// delta would publish mostly the model's private working.
	grokThinkingDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The"}},"parent_tool_use_id":null,"session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760","uuid":"9aa5f6c3-d21e-4aa4-b270-827568bac400"}`

	// A tool call's arguments. Publishing these as output_text would tell the
	// client the shell command was part of the answer.
	grokToolInputDeltaEvent = `{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo HELLO_PROBE\",\"description\":\"Print HELLO_PROBE to stdout\"}"}},"parent_tool_use_id":null,"session_id":"01a02d79-beef-7d40-8d24-beadd71589f2","uuid":"a8f23acf-cc0d-49cc-ac06-aa91c536d16c"}`

	grokMessageStopEvent = `{"type":"stream_event","event":{"type":"message_stop"},"parent_tool_use_id":null,"session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760","uuid":"b6d03f0d-b5e3-40b0-a884-7519ab023e38"}`

	// The whole assistant message, repeated after its deltas have all arrived.
	grokAssistantEvent = `{"type":"assistant","message":{"id":"msg_0","type":"message","role":"assistant","model":"grok-4.6","content":[{"type":"text","text":"ALPHA BRAVO CHARLIE"}],"stop_reason":"end_turn"},"parent_tool_use_id":null,"session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760","uuid":"5f2eae82-ad59-4d47-9151-960d6fb296ef"}`

	// The single-message run's own totals, abridged: the real line also carries
	// `duration_api_ms`, `total_cost_usd` and a `modelUsage` breakdown, none of
	// which this parser reads.
	grokResultEvent = `{"type":"result","subtype":"success","is_error":false,"duration_ms":6071,"num_turns":1,"result":"ALPHA BRAVO CHARLIE","stop_reason":"end_turn","usage":{"input_tokens":19664,"output_tokens":43,"cache_read_input_tokens":5760,"cache_creation_input_tokens":0},"session_id":"01a02d66-1268-7ea3-af50-e5a1e1ccc760"}`

	// A failed run, from `--model bogus-model-xyz`. `result` is absent
	// entirely — where claude puts the reason in that field, grok has a
	// separate `errors` array — and the subtype stops being "success", which
	// claude's never does.
	grokErrorResultEvent = `{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":0,"num_turns":0,"stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"errors":["Couldn't set model 'bogus-model-xyz': Invalid params: \"unknown model id\". Run 'grok models' to see available models."],"session_id":"01a02d67-5c3b-7e63-9355-dfbfbd487de5"}`
)

// The three usage-bearing lines of one two-message run — session
// 01a02d79-beef-7d40-8d24-beadd71589f2, the `echo HELLO_PROBE` run whose text
// deltas grokToolInputDeltaEvent also comes from. They are grouped here, and
// all from the same session, because the point they make is an arithmetic one
// and it is only checkable if the numbers belong to the same run:
//
//	19672 + 166 = 19838   input
//	   64 +  33 =    97   output
//	 5760 + 25344 = 31104 cache read
//
// The two `message_delta` lines are per message; only the `result` is the
// run's. All three carry the same four field names, which is what makes
// reading the wrong one easy and silent — and the second message's 166 is what
// a client would be told the whole task cost. Both message_deltas are verbatim;
// the result is abridged the same way grokResultEvent is.
const (
	grokMessageDeltaEvent       = `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"input_tokens":19672,"output_tokens":64,"cache_read_input_tokens":5760,"cache_creation_input_tokens":0}},"parent_tool_use_id":null,"session_id":"01a02d79-beef-7d40-8d24-beadd71589f2","uuid":"6fc6d7bf-888b-4be7-a08d-d662e40c6791"}`
	grokMessageDeltaSecondEvent = `{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":166,"output_tokens":33,"cache_read_input_tokens":25344,"cache_creation_input_tokens":0}},"parent_tool_use_id":null,"session_id":"01a02d79-beef-7d40-8d24-beadd71589f2","uuid":"10e6b332-97a8-466b-9451-6f451a0557dd"}`
	grokToolRunResultEvent      = `{"type":"result","subtype":"success","is_error":false,"duration_ms":12023,"num_turns":2,"result":"It printed:\n\n` + "`HELLO_PROBE`" + `","stop_reason":"end_turn","usage":{"input_tokens":19838,"output_tokens":97,"cache_read_input_tokens":31104,"cache_creation_input_tokens":0},"session_id":"01a02d79-beef-7d40-8d24-beadd71589f2"}`
)

func TestParseGrokLine(t *testing.T) {
	t.Run("init yields the native session id", func(t *testing.T) {
		got := onlyOfType(t, parseGrokLine(grokInitEvent), UpdateSessionID)
		if got.SessionID != "01a02d66-1268-7ea3-af50-e5a1e1ccc760" {
			t.Errorf("session id = %q", got.SessionID)
		}
	})

	// Issue #43. A task that names no model is run with no `--model` flag, so
	// which model grok picked is grok's to say — and it says so on this same
	// line. Without reading it the response can only report the router's
	// advertised default, which is a guess that happens to be right.
	t.Run("init also names the model that is running", func(t *testing.T) {
		got := onlyOfType(t, parseGrokLine(grokInitEvent), UpdateModel)
		if got.Model != "grok-4.6" {
			t.Errorf("model = %q, want %q — the model grok said it was running", got.Model, "grok-4.6")
		}
	})

	// The id is repeated on every `assistant` message one level down. Reading
	// those too would rewrite the task's model once per message for no gain,
	// and would put the parser one field rename away from doing it wrongly.
	t.Run("the model is named once, not from every line that repeats it", func(t *testing.T) {
		assertModelNotRepublished(t, parseGrokLine, grokAssistantEvent, grokTextDeltaEvent)
	})

	t.Run("a text delta is the answer, a fragment at a time", func(t *testing.T) {
		got := parseGrokLine(grokTextDeltaEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "ALPHA" {
			t.Errorf("delta = %q, want %q — a token-level delta is passed through unpadded", got[0].Delta, "ALPHA")
		}
	})

	// grok is the third adapter to hit this, and the first where the separator
	// cannot hang on the text event: its deltas are token-level, so a newline
	// per delta would break every word apart. `message_stop` is the one
	// boundary a stateless parser can see. Verified by execution on 1.0.5 — a
	// run that called one tool said "I'll run that command and tell you what it
	// printed." and then "It printed:\n\n`HELLO_PROBE`", which concatenate to
	// "…printed.It printed:…" without this.
	t.Run("consecutive messages do not run together", func(t *testing.T) {
		var answer string
		lines := []string{
			`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Alpha."}}}`,
			grokMessageStopEvent,
			`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Gamma."}}}`,
			grokMessageStopEvent,
		}
		for _, line := range lines {
			for _, upd := range parseGrokLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "Alpha.\nGamma.\n" {
			t.Errorf("answer = %q, want %q", answer, "Alpha.\nGamma.\n")
		}
	})

	// The result event's totals, not `message_delta`'s. Both carry the same
	// four field names, which is what makes reading the wrong one easy and
	// silent: usage is applied last-write-wins, so a two-message run would
	// publish its *second* message's 166 input tokens as the whole task's.
	//
	// All three lines are from the one run, so the arithmetic is checkable
	// rather than asserted: the per-message numbers really do sum to the total
	// the parser is required to publish instead of them.
	t.Run("usage comes from the run's totals, not a message's", func(t *testing.T) {
		usage := onlyUsage(t, parseGrokLine(grokToolRunResultEvent))
		if usage.InputTokens != 19838 || usage.OutputTokens != 97 || usage.TotalTokens != 19935 {
			t.Errorf("usage = %+v", usage)
		}
		if usage.CacheReadTokens != 31104 || usage.CacheWriteTokens != 0 {
			t.Errorf("cache accounting = %+v", usage)
		}

		// The fixtures' own claim, checked rather than trusted: if these ever
		// stop summing, one of them was edited by hand and the comment above
		// them is fiction.
		for _, line := range []string{grokMessageDeltaEvent, grokMessageDeltaSecondEvent} {
			if got := parseGrokLine(line); len(got) != 0 {
				t.Errorf("a message's own usage was published as the run's: %+v", got)
			}
		}
		first, second := messageDeltaUsage(t, grokMessageDeltaEvent), messageDeltaUsage(t, grokMessageDeltaSecondEvent)
		if first.InputTokens+second.InputTokens != usage.InputTokens {
			t.Errorf("the two messages' input tokens (%d + %d) do not sum to the run's %d",
				first.InputTokens, second.InputTokens, usage.InputTokens)
		}
		if first.OutputTokens+second.OutputTokens != usage.OutputTokens {
			t.Errorf("the two messages' output tokens (%d + %d) do not sum to the run's %d",
				first.OutputTokens, second.OutputTokens, usage.OutputTokens)
		}

		// The single-message run, for the plain case.
		single := onlyUsage(t, parseGrokLine(grokResultEvent))
		if single.InputTokens != 19664 || single.OutputTokens != 43 || single.TotalTokens != 19707 {
			t.Errorf("single-message usage = %+v", single)
		}
	})

	// grok exits 1 on a failed run, so the shared runner fails the task either
	// way. What it cannot do is say why, and grok's `errors` array is the only
	// place the reason appears on stdout. Read after the usage, so a failed run
	// still reports what it spent.
	t.Run("a failed result carries the CLI's own words", func(t *testing.T) {
		got := parseGrokLine(grokErrorResultEvent)
		var failure *RunUpdate
		for i := range got {
			if got[i].Type == UpdateFailed {
				failure = &got[i]
			}
		}
		if failure == nil {
			t.Fatalf("got %+v, want a %s update", got, UpdateFailed)
		}
		if !strings.Contains(failure.Err.Error(), "unknown model id") {
			t.Errorf("error drops the CLI's message: %v", failure.Err)
		}
	})

	// `--include-partial-messages` adds the deltas; it does not replace the
	// finished message. Reading both would publish every answer twice, and the
	// deltas are the half that makes the stream progressive.
	t.Run("nothing is invented from events that carry no answer", func(t *testing.T) {
		for _, line := range []string{
			grokThinkingDeltaEvent,
			grokToolInputDeltaEvent,
			grokAssistantEvent,
			grokMessageDeltaEvent,
		} {
			for _, upd := range parseGrokLine(line) {
				if upd.Type == UpdateDelta {
					t.Errorf("line produced answer text %q, want none:\n  %s", upd.Delta, line)
				}
			}
		}
	})

	t.Run("a non-JSON line is not answer text", func(t *testing.T) {
		for _, line := range []string{"", "not json", "Error: Failed to restore session from remote"} {
			if got := parseGrokLine(line); len(got) != 0 {
				t.Errorf("%q produced %+v, want nothing", line, got)
			}
		}
	})
}

// onlyUsage returns the single usage update in a parse result, failing if
// there is not exactly one. A parser that emitted two would be publishing one
// of them as the run's total by accident.
func onlyUsage(t *testing.T, updates []RunUpdate) *domain.Usage {
	t.Helper()
	return onlyOfType(t, updates, UpdateUsage).Usage
}

// onlyOfType returns the single update of a given type in a parse result,
// failing if there is not exactly one. It exists because one line can now
// legitimately produce several kinds of update — an init event names both the
// session and the model — so asserting on the length of the whole slice tests
// the wrong thing.
func onlyOfType(t *testing.T, updates []RunUpdate, typ UpdateType) RunUpdate {
	t.Helper()
	var found *RunUpdate
	for i := range updates {
		if updates[i].Type != typ {
			continue
		}
		if found != nil {
			t.Fatalf("two %s updates from one line: %+v", typ, updates)
		}
		found = &updates[i]
	}
	if found == nil {
		t.Fatalf("got %+v, want a %s update", updates, typ)
	}
	return *found
}

// Issue #43 has two halves, and this is the one that is a claim about absence.
// codex.go and opencode.go each say in a comment that no line the CLI prints
// names a model, which is why those two harnesses report the router's guess
// instead of an observation. A comment cannot be checked; the fixtures can.
//
// Both assertions are here on purpose. The parser check is the behaviour, and
// the field check is the evidence under it: if a later capture of either CLI
// does carry a model, the second half goes red and points at the comment that
// has become false, rather than leaving a guess in place that no longer has to
// be one.
func TestTheAdaptersThatNameNoModelHaveNoModelToName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func(string) []RunUpdate
		lines []string
	}{
		{"codex", parseCodexLine, []string{
			codexThreadStartedEvent, codexTurnStartedEvent, codexAgentMessageEvent,
			codexTurnCompletedEvent, codexAgentMessageAlpha, codexAgentMessageGamma,
			codexCommandStartedEvent, codexCommandCompletedEvent, codexErrorItemEvent,
			codexErrorEvent, codexTurnFailedEvent,
		}},
		{"opencode", parseOpenCodeLine, []string{
			openCodeStepStartEvent, openCodeTextEvent, openCodeStepFinishEvent,
			openCodeToolUseEvent, openCodeErrorEvent, openCodeRefErrorEvent,
			openCodeTextEventAlpha, openCodeTextEventGamma,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertModelNotRepublished(t, tc.parse, tc.lines...)

			for _, line := range tc.lines {
				var any map[string]json.RawMessage
				if err := json.Unmarshal([]byte(line), &any); err != nil {
					t.Fatalf("fixture is not JSON: %v", err)
				}
				// Top level only, which is where both CLIs put everything
				// their parsers read. `codexErrorItemEvent` proves why a
				// substring search would not do: its `message` is the sentence
				// "Model metadata for `bogus-model-xyz` not found", which
				// contains the word and no model field.
				if _, ok := any["model"]; ok {
					t.Errorf("%s names a model after all — the comment in the adapter is now false:\n  %s",
						tc.name, line)
				}
			}
		})
	}
}

// assertModelNotRepublished fails if any of the given lines produces a model
// update. Every adapter that reads a model reads it from one line and has
// others repeating something model-shaped underneath; this is what pins the
// "one line, once" half of that for each of them.
func assertModelNotRepublished(t *testing.T, parse func(string) []RunUpdate, lines ...string) {
	t.Helper()
	for _, line := range lines {
		for _, upd := range parse(line) {
			if upd.Type == UpdateModel {
				t.Errorf("model %q republished from a line that is not the one it is read from:\n  %s",
					upd.Model, line)
			}
		}
	}
}

// messageDeltaUsage reads a `message_delta` line's own usage, which parseGrokLine
// deliberately does not. It exists so the fixtures' arithmetic can be checked
// against the numbers grok actually printed rather than against a comment.
func messageDeltaUsage(t *testing.T, line string) domain.Usage {
	t.Helper()
	var ev struct {
		Event struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"event"`
	}
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("fixture is not JSON: %v", err)
	}
	return domain.Usage{
		InputTokens:  ev.Event.Usage.InputTokens,
		OutputTokens: ev.Event.Usage.OutputTokens,
	}
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

	// These two are one run's own lines, kept together because the pair is the
	// point: claude does NOT report one model id, it reports two, and they are
	// not the same string. From the `make capture-claude` run against Claude
	// Code 2.1.240 on 2026-08-23, abridged the same way as the rest of this
	// block — the init line's 2.6 KB of tool and slash-command inventory and
	// the assistant line's usage and telemetry are dropped, and the answer text
	// is cut to its first character. Every field below is verbatim.
	//
	// The init line says `claude-opus-5[1m]`, the CLI's own resolved selection
	// including the 1M-context variant. Every message underneath it says
	// `claude-opus-5`, the underlying API model. parseClaudeLine reports the
	// first — see there for why, and for what it costs.
	claudeCapturedInitEvent = `{"type":"system","subtype":"init","cwd":"/Users/aenawi/Workspaces/Development/side-projects/uhp-go","session_id":"e116749a-bd87-483d-acd9-6058cbdf7a6d","model":"claude-opus-5[1m]","permissionMode":"default","apiKeySource":"none","claude_code_version":"2.1.240"}`

	claudeCapturedAssistantEvent = `{"type":"assistant","message":{"model":"claude-opus-5","id":"msg_011CeJxf3Es88ctHnmiKkyQw","type":"message","role":"assistant","content":[{"type":"text","text":"1"}],"stop_reason":null},"session_id":"e116749a-bd87-483d-acd9-6058cbdf7a6d"}`
)

func TestParseClaudeLine(t *testing.T) {
	t.Run("init yields the native session id, once", func(t *testing.T) {
		got := onlyOfType(t, parseClaudeLine(claudeInitEvent), UpdateSessionID)
		if got.SessionID != "45b84817-020e-4a0b-9c94-93a0106c5814" {
			t.Errorf("session id = %q", got.SessionID)
		}
	})

	// Issue #43.
	t.Run("init also names the model that is running", func(t *testing.T) {
		got := onlyOfType(t, parseClaudeLine(claudeInitEvent), UpdateModel)
		if got.Model != "claude-opus-5" {
			t.Errorf("model = %q, want %q — the model claude said it was running", got.Model, "claude-opus-5")
		}
	})

	// The two ids one run reports, from that run's own two lines. They differ,
	// which is the whole reason parseClaudeLine has to choose rather than treat
	// the init line as a convenient copy of what the messages say. It reports
	// the init line's, suffix and all.
	t.Run("the init line's model is reported, not the messages' different one", func(t *testing.T) {
		got := onlyOfType(t, parseClaudeLine(claudeCapturedInitEvent), UpdateModel)
		if got.Model != "claude-opus-5[1m]" {
			t.Errorf("model = %q, want %q — the id is forwarded as claude spelled it, variant suffix and all",
				got.Model, "claude-opus-5[1m]")
		}

		// The fixture pair's own claim, checked rather than trusted: if these
		// two ever carry the same id, the paragraph in parseClaudeLine that
		// weighs one against the other is describing a choice that no longer
		// exists.
		var messageModel struct {
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(claudeCapturedAssistantEvent), &messageModel); err != nil {
			t.Fatalf("fixture is not JSON: %v", err)
		}
		if messageModel.Message.Model == got.Model {
			t.Errorf("the captured pair no longer disagrees (%q); parseClaudeLine's reasoning is stale",
				got.Model)
		}
	})

	t.Run("the model is named once, not from every line that repeats it", func(t *testing.T) {
		assertModelNotRepublished(t, parseClaudeLine,
			claudeAssistantEvent, claudeTextDeltaEvent, claudeCapturedAssistantEvent)
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

// Every pi*Event below was taken off the wire from pi 0.84.2 on 2026-08-23, and
// each says below it which run produced it. None is declared. Some are
// abridged, and only ever by deleting a field this parser never reads —
// `usage`, `cost`, `cacheRead`/`cacheWrite`, `cwd`. Nothing the parser looks at
// has been edited, `errorMessage` included: it is here with the provider's own
// JSON body still embedded in it, because that is what pi passes through and
// what a client would be shown.
//
// Most come from `make probe-pi` — scripts/probe-pi-session.py, which needs no
// credentials — so a reader can reproduce them. The two groq lines cannot be
// reproduced that way and say so.
//
// Issue #33: the message_update fixtures used to be the exception. They were
// read out of pi 0.83.0's `pi-agent-core/dist/types.d.ts` rather than off the
// wire, and a declared shape that no line matches is the silent failure — every
// delta is dropped, the run completes, and the client is handed an empty answer.
// The declaration turned out to be right about the two fields the parser reads
// and wrong about the rest: on 0.84.2 a message_update carries no `message` and
// no `partial`, only `usage` and `assistantMessageEvent`. Nothing depended on
// the wrong half, which is luck rather than evidence, and is why these are now
// captured.
const (
	// The id `--session-id` takes back. Verified round-trip, not inferred from
	// the field name: the probe reads it off this event, hands it back, and
	// watches the resumed turn reach the provider carrying the first turn.
	// `cwd` shortened; the parser reads only `id`.
	piSessionEvent = `{"type":"session","version":3,"id":"01a02d39-dbd8-70b0-b19d-d785a81fa64f","timestamp":"2026-08-23T06:06:01.688Z","cwd":"/w"}`

	piAgentStart     = `{"type":"agent_start"}`
	piTurnStart      = `{"type":"turn_start"}`
	piUserMessageEnd = `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"Say exactly: Alpha Bravo Charlie"}],"timestamp":1787465161758}}`

	// The first of the run's three text deltas. `contentIndex` is 1 rather than
	// 0 because the thinking below took index 0 — the probe asks for both in
	// one run, so these are the same run's lines and not a composite.
	piTextDeltaEvent = `{"type":"message_update","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"Alpha"}}`

	// text_start and text_end bracket the deltas. text_end repeats the whole
	// text in `content`, so a parser that read it would answer twice over —
	// the same trap message_end sets, one nesting level down.
	piTextStartEvent = `{"type":"message_update","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"assistantMessageEvent":{"type":"text_start","contentIndex":1}}`
	piTextEndEvent   = `{"type":"message_update","usage":{"input":11,"output":3,"cacheRead":0,"cacheWrite":0,"reasoning":0,"totalTokens":14,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"assistantMessageEvent":{"type":"text_end","contentIndex":1,"content":"Alpha Bravo Charlie"}}`

	// The model's private working, from the same run: the probe's provider
	// sends `reasoning_content` ahead of the answer so this event exists to be
	// captured rather than assumed.
	piThinkingDeltaEvent = `{"type":"message_update","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"weighing it up"}}`

	// The finished assistant message, carrying the whole answer its deltas
	// already delivered — and the thinking as well, which is the second reason
	// not to read it.
	piAssistantMessageEnd = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"weighing it up","thinkingSignature":"reasoning_content"},{"type":"text","text":"Alpha Bravo Charlie"}],"api":"openai-completions","provider":"probe","model":"probe-model","stopReason":"stop","timestamp":1787465161802,"responseId":"probe-1","rawStopReason":"stop"}}`

	// A refused run, from the probe's fourth check. pi exits 0 after printing
	// this in json mode — only its text mode turns an error into a non-zero
	// exit — so a harness that ignored it would report a run that never
	// happened as completed with empty output.
	piErrorMessageEnd = `{"type":"message_end","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"probe","model":"probe-model","stopReason":"error","timestamp":1787465179874,"errorMessage":"400: {\"message\":\"probe: the provider refused this run\",\"type\":\"invalid_request_error\"}"}}`

	// The same failure, repeated on the event that follows it.
	piErrorTurnEnd = `{"type":"turn_end","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"probe","model":"probe-model","stopReason":"error","timestamp":1787465179874,"errorMessage":"400: {\"message\":\"probe: the provider refused this run\",\"type\":\"invalid_request_error\"}"},"toolResults":[]}`

	// And on the event *before* it. A failed run's message_start already
	// carries `stopReason: "error"` and the whole errorMessage, so a parser
	// that matched on the stop reason alone rather than on message_end would
	// report the failure twice before turn_end even arrived.
	piErrorMessageStart = `{"type":"message_start","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"probe","model":"probe-model","stopReason":"error","timestamp":1787465179874,"errorMessage":"400: {\"message\":\"probe: the provider refused this run\",\"type\":\"invalid_request_error\"}"}}`

	// The one failure a loopback provider cannot stage: a real provider's own
	// refusal, in its own words. Captured on 0.84.2 from `pi -p --mode json
	// --model groq/openai/gpt-oss-20b`, which 413s because groq counts a
	// model's max output tokens against its per-minute limit — 65.5K against a
	// ceiling of 8K, so every catalogued model on that account exceeds it. It
	// is here because it is the case the client is actually told about, and
	// because `make probe-pi` cannot produce it: reproducing this one needs
	// credentials.
	piProviderErrorMessageEnd = `{"type":"message_end","message":{"role":"assistant","content":[],"api":"openai-completions","provider":"groq","model":"openai/gpt-oss-20b","stopReason":"error","timestamp":1787463832537,"errorMessage":"413: Request too large for model ` + "`openai/gpt-oss-20b`" + ` on tokens per minute (TPM): Limit 8000, Requested 66068"}}`
)

func TestParsePiLine(t *testing.T) {
	t.Run("a text delta is the answer arriving", func(t *testing.T) {
		got := parsePiLine(piTextDeltaEvent)
		if len(got) != 1 || got[0].Type != UpdateDelta {
			t.Fatalf("got %+v, want one %s update", got, UpdateDelta)
		}
		if got[0].Delta != "Alpha" {
			t.Errorf("delta = %q, want %q", got[0].Delta, "Alpha")
		}
	})

	// Issue #33. pi announces its session id once, on the first line of the
	// run, and `--session-id` takes that same id back — verified round-trip by
	// scripts/probe-pi-session.py, which reads the id off this event and then
	// watches the resumed turn arrive at the provider carrying the first turn's
	// messages. Without this the `--session-id` branch in BuildArgs is
	// unreachable and every continuation quietly starts a new conversation,
	// which is the half of #13 opencode shipped alone and had to fix.
	t.Run("the session id is discovered, so a continuation can resume", func(t *testing.T) {
		got := parsePiLine(piSessionEvent)
		if len(got) != 1 || got[0].Type != UpdateSessionID {
			t.Fatalf("got %+v, want one %s update", got, UpdateSessionID)
		}
		if got[0].SessionID != "01a02d39-dbd8-70b0-b19d-d785a81fa64f" {
			t.Errorf("session id = %q, want the id pi announced", got[0].SessionID)
		}
	})

	// A session event with no id in it is not a session id. Publishing "" would
	// overwrite a good id on the task with nothing, and the next continuation
	// would start a new conversation with no sign anything went wrong.
	t.Run("an id-less session event is not a session id", func(t *testing.T) {
		if got := parsePiLine(`{"type":"session","version":3,"cwd":"/w"}`); len(got) != 0 {
			t.Errorf("got %+v, want nothing", got)
		}
	})

	// pi announces each assistant message three times over: as a stream of
	// deltas, whole in text_end's `content`, and whole again at message_end.
	// Reading any of the other two triples the answer, so the whole run is
	// replayed here rather than the deltas alone.
	t.Run("the answer is read once, not from every event carrying it", func(t *testing.T) {
		var answer string
		for _, line := range []string{
			piTextStartEvent, piTextDeltaEvent, piTextDeltaEvent, piTextEndEvent,
			piAssistantMessageEnd,
		} {
			for _, upd := range parsePiLine(line) {
				answer += upd.Delta
			}
		}
		if answer != "AlphaAlpha" {
			t.Errorf("answer = %q, want %q — something other than the deltas is being read as answer text",
				answer, "AlphaAlpha")
		}
	})

	t.Run("thinking is not answer text", func(t *testing.T) {
		if got := parsePiLine(piThinkingDeltaEvent); len(got) != 0 {
			t.Errorf("thinking produced %+v, want nothing", got)
		}
	})

	t.Run("an errored message fails the run, carrying pi's own words", func(t *testing.T) {
		for _, tc := range []struct{ line, want string }{
			{piErrorMessageEnd, "the provider refused this run"},
			// The real-provider failure, which is the one a client actually
			// meets. Its reason is a sentence worth forwarding whole — an
			// adapter that reported only "the run failed" would drop the only
			// part that tells the operator what to change.
			{piProviderErrorMessageEnd, "Limit 8000"},
		} {
			got := onlyOfType(t, parsePiLine(tc.line), UpdateFailed)
			if got.Err == nil || !strings.Contains(got.Err.Error(), tc.want) {
				t.Errorf("error drops pi's message %q: %v", tc.want, got.Err)
			}
		}
	})

	// Issue #43. pi resolves `provider/model` itself when a task names none, so
	// message_end is the only place the answer exists — the router's guess is
	// the first row of `pi --list-models`, which is not what pi picks.
	t.Run("the finished message names the model that produced it", func(t *testing.T) {
		got := onlyOfType(t, parsePiLine(piAssistantMessageEnd), UpdateModel)
		if got.Model != "probe-model" {
			t.Errorf("model = %q, want %q", got.Model, "probe-model")
		}
	})

	// A run the provider refused still ran on a model, and the client reading
	// the terminal response should not be told less about a failure than about
	// a success. The model is reported before the failure, so a terminal update
	// cannot land first and close the task ahead of it.
	t.Run("a failed message names its model, ahead of the failure", func(t *testing.T) {
		got := parsePiLine(piProviderErrorMessageEnd)
		if len(got) != 2 || got[0].Type != UpdateModel || got[1].Type != UpdateFailed {
			t.Fatalf("got %+v, want a %s update then a %s one", got, UpdateModel, UpdateFailed)
		}
		if got[0].Model != "openai/gpt-oss-20b" {
			t.Errorf("model = %q, want %q", got[0].Model, "openai/gpt-oss-20b")
		}
	})

	// The same failure is carried by message_start before it and repeated by
	// turn_end and agent_end after. Only one update is emitted for it, from
	// message_end, so a client is not told four times.
	t.Run("the failure is reported once, not on every event carrying it", func(t *testing.T) {
		for _, line := range []string{piErrorMessageStart, piErrorTurnEnd} {
			if got := parsePiLine(line); len(got) != 0 {
				t.Errorf("produced %+v, want nothing — message_end already reported it:\n  %s",
					got, line)
			}
		}
	})

	t.Run("nothing is invented from lifecycle events", func(t *testing.T) {
		for _, line := range []string{piAgentStart, piTurnStart, piUserMessageEnd} {
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

// The two things TestParsePiLine cannot verify are that pi still emits these
// events and that `--session-id` still resumes. scripts/probe-pi-session.py is
// that verification (`make probe-pi`), and unlike the claude probes it needs no
// credentials: pi's models.json can declare a provider outright, so the probe
// answers from a loopback server of its own.
//
// This is the part a test can hold: that the probe runs the argv uhpd ships.
// Issue #33 is what happens without it — a shape declared from the binary that
// nothing on the wire matches — and a probe measuring a different invocation
// would be a more confident version of the same mistake.
//
// Both forms, because the resume flag is inside BuildArgs rather than behind a
// hook of its own, so the fresh argv alone would not pin it. `<model>` and
// `<session>` are what the probe substitutes at run time.
func TestPiProbeRunsTheShippedInvocation(t *testing.T) {
	const probe = "../../scripts/probe-pi-session.py"

	src, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("the pi session probe is missing: %v", err)
	}
	h := NewPi([]string{"<model>"})

	for _, tc := range []struct {
		list string
		req  RunRequest
	}{
		{"HARNESS_ARGV", RunRequest{Input: "hello"}},
		{"RESUME_ARGV", RunRequest{Input: "hello", Model: "<model>", NativeSessionID: "<session>"}},
	} {
		args, err := h.BuildArgs(tc.req)
		if err != nil {
			t.Fatalf("BuildArgs(%s): %v", tc.list, err)
		}
		want := append([]string{"pi"}, args...)
		got := pythonStringList(t, string(src), tc.list)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %s measures an invocation uhpd does not send:\n  probe: %v\n  uhpd:  %v",
				tc.list, probe, got, want)
		}
	}
}

// Issue #34's two probes, pinned the same way and for the same reason: a probe
// that reports a healthy CLI while measuring an argv uhpd does not send is
// worse than no probe, because it is evidence for a claim nobody checked.
//
// Both forms each, because the resume flag lives inside BuildArgs rather than
// behind a hook of its own, so a fresh argv alone would not pin it. `<model>`
// and `<session>` are what each probe substitutes at run time — and `<prompt>`
// as well for grok, which is the one harness whose prompt is in argv at all.
func TestCodexAndGrokProbesRunTheShippedInvocation(t *testing.T) {
	for _, h := range []struct {
		probe  string
		binary string
		build  func() *CLIHarness
		// prompt is what the harness's Input becomes in the pinned lists.
		// Codex sends its prompt over stdin, so nothing of it reaches argv and
		// any value does; grok's lands in `-p=<prompt>` and must match.
		prompt string
	}{
		{"../../scripts/probe-codex-session.py", "codex", func() *CLIHarness { return NewCodex([]string{"<model>"}) }, "hello"},
		{"../../scripts/probe-grok-session.py", "grok", func() *CLIHarness { return NewGrok([]string{"<model>"}) }, "<prompt>"},
	} {
		t.Run(h.binary, func(t *testing.T) {
			src, err := os.ReadFile(h.probe)
			if err != nil {
				t.Fatalf("the %s probe is missing: %v", h.binary, err)
			}
			harness := h.build()

			for _, tc := range []struct {
				list string
				req  RunRequest
			}{
				{"HARNESS_ARGV", RunRequest{Input: h.prompt}},
				{"RESUME_ARGV", RunRequest{Input: h.prompt, Model: "<model>", NativeSessionID: "<session>"}},
			} {
				args, err := harness.BuildArgs(tc.req)
				if err != nil {
					t.Fatalf("BuildArgs(%s): %v", tc.list, err)
				}
				want := append([]string{h.binary}, args...)
				got := pythonStringList(t, string(src), tc.list)
				if strings.Join(got, " ") != strings.Join(want, " ") {
					t.Errorf("%s: %s measures an invocation uhpd does not send:\n  probe: %v\n  uhpd:  %v",
						tc.list, h.probe, got, want)
				}
			}
		})
	}
}

// Five adapters now report a failure their CLI printed rather than exited
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
