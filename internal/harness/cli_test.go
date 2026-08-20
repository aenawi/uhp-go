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
			name: "opencode: --print-logs is not passed",
			h:    NewOpenCode(models),
			req:  RunRequest{Input: "hello"},
			want: []string{"run"},
			checkFn: func(t *testing.T, args []string) {
				if argvContains(args, "--print-logs") {
					t.Fatalf("--print-logs leaks harness logs to the client: %v", args)
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
