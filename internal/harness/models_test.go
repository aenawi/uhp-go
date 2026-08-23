package harness

import (
	"reflect"
	"testing"
	"time"
)

// Every fixture named *ModelsOutput below is the verbatim stdout of the real
// CLI, captured by running it. That is the whole point of issue #12: the model
// ids in this repository were written from memory and four of the five were
// wrong, so a fixture invented the same way would only move the same mistake
// into the tests and let them pass.
//
// The refusal cases are the exception, and are labelled where they appear. Two
// of them are captured output; the rest are written by hand, because producing
// the real thing means logging out of a paid account. They are not claims
// about what a CLI prints — they are inputs that are definitely not a model
// list, asserting that the parser yields nothing rather than a plausible-
// looking id. Anything stricter would be a fixture written from memory.

// grokModelsOutput is `grok models`, captured 2026-08-21 and re-captured
// 2026-08-23 from grok 1.0.5 (issue #34), byte for byte the same both times —
// same banner, same default, same two ids. It gets one fixture rather than
// opencode's two because there is nothing to keep a second copy of.
const grokModelsOutput = "You are logged in with grok.com.\n" +
	"\n" +
	"Default model: grok-4.6\n" +
	"\n" +
	"Available models:\n" +
	"  * grok-4.6 (default)\n" +
	"  - grok-4.5\n"

// openCodeModelsOutput is `opencode models`, captured 2026-08-21 from opencode
// 1.14.41. Every line is a `provider/model` id, which is the form
// `opencode run --model` takes.
const openCodeModelsOutput = "opencode/big-pickle\n" +
	"opencode/deepseek-v4-flash-free\n" +
	"opencode/mimo-v2.5-free\n" +
	"opencode/nemotron-3-ultra-free\n" +
	"opencode/north-mini-code-free\n"

// openCodeModelsOutput1_18 is the same command captured 2026-08-22 from
// opencode 1.18.21 (issue #13). It is here rather than replacing the one above
// because the ids differ between the two captures and neither list is wrong:
// `opencode models` prints what this install's configured providers expose, so
// the ids are a property of the machine and only the shape is a property of the
// CLI. Keeping both is what makes that claim checkable — and it grounds
// `opencode/hy3-free`, which the probes in cli_test.go cite.
const openCodeModelsOutput1_18 = "opencode/big-pickle\n" +
	"opencode/hy3-free\n" +
	"opencode/mimo-v2.5-free\n" +
	"opencode/muse-spark-1.2-contributor-free\n" +
	"opencode/nemotron-3-ultra-free\n" +
	"opencode/nemotron-3.5-lightning-free\n" +
	"opencode/x-preview-f-free\n"

// piModelsOutput is `pi --list-models`, captured 2026-08-21. A whitespace
// table with a header row; the id `pi --model` takes is `provider/model`, and
// the model column may itself contain slashes.
const piModelsOutput = "" +
	"provider  model                                      context  max-out  thinking  images\n" +
	"groq      llama-3.1-8b-instant                       131.1K   131.1K   no        no    \n" +
	"groq      llama-3.3-70b-versatile                    131.1K   32.8K    no        no    \n" +
	"groq      meta-llama/llama-4-scout-17b-16e-instruct  131.1K   8.2K     no        yes   \n" +
	"groq      qwen/qwen3-32b                             131.1K   41.0K    yes       no    \n"

// codexModelsOutput has the shape of `codex debug models`, with the slugs,
// visibilities and priorities the real command returned on 2026-08-21. Only
// the fields this parser reads are kept: the real document is ~325 KB because
// every entry embeds its own system prompt, and none of that is under test.
// The order is deliberately not priority order, because the real document's
// order is not a promise and the default model is whichever has priority 1.
//
// It keeps `gpt-reserve`, which codex-cli 0.149.0 no longer returns, because it
// was there on 2026-08-21 and because it is the second hidden entry — one
// hidden entry would let a visibility filter pass on a single case.
const codexModelsOutput = `{"models":[
  {"slug":"gpt-5.4","visibility":"list","priority":16},
  {"slug":"gpt-reserve","visibility":"hide","priority":3},
  {"slug":"gpt-5.6-sol","visibility":"list","priority":1},
  {"slug":"gpt-5.4-mini","visibility":"list","priority":23},
  {"slug":"gpt-5.6-terra","visibility":"list","priority":2},
  {"slug":"codex-auto-review","visibility":"hide","priority":43},
  {"slug":"gpt-5.6-luna","visibility":"list","priority":3},
  {"slug":"gpt-5.5","visibility":"list","priority":7}
]}`

// codexModelsOutput0_149 is the same command re-captured 2026-08-23 from
// codex-cli 0.149.0 (issue #34), and it is here for the reason the issue gives
// for opencode's second capture: the ids are not the CLI's to promise, so
// recording what 0.149.0 actually returned is the only thing that makes "the
// shape did not change" a checkable claim rather than a sentence in a comment.
//
// It is a narrower re-capture than opencode's, and the difference is worth
// naming: `opencode models` prints what this install's configured providers
// expose, so the two captures there are evidence about two machines. `codex
// debug models` is served by the API against one account, so this is the same
// account two days later. What it settles is the shape and the ordering, which
// is what the parser reads.
//
// Every entry the 2026-08-21 capture holds came back at the same visibility and
// the same priority. The one change is `gpt-reserve`, absent here entirely,
// which is why the priority-3 slot is now `gpt-5.6-luna`'s alone. Both fixtures
// therefore parse to the same six ids in the same order.
const codexModelsOutput0_149 = `{"models":[
  {"slug":"gpt-5.6-sol","visibility":"list","priority":1},
  {"slug":"gpt-5.6-terra","visibility":"list","priority":2},
  {"slug":"gpt-5.6-luna","visibility":"list","priority":3},
  {"slug":"gpt-5.5","visibility":"list","priority":7},
  {"slug":"gpt-5.4","visibility":"list","priority":16},
  {"slug":"gpt-5.4-mini","visibility":"list","priority":23},
  {"slug":"codex-auto-review","visibility":"hide","priority":43}
]}`

func TestParseModels(t *testing.T) {
	cases := []struct {
		name  string
		parse func(string) []string
		out   string
		want  []string
	}{
		{
			name:  "grok: the starred entry leads, and the marker is not part of the id",
			parse: parseGrokModels,
			out:   grokModelsOutput,
			want:  []string{"grok-4.6", "grok-4.5"},
		},
		{
			name:  "grok: the default leads even when the CLI does not list it first",
			parse: parseGrokModels,
			out:   "Available models:\n  - grok-4.5\n  * grok-4.6 (default)\n",
			want:  []string{"grok-4.6", "grok-4.5"},
		},
		{
			name:  "grok: the default leads with more than two to reorder",
			parse: parseGrokModels,
			out:   "Available models:\n  - a\n  - b\n  * c (default)\n  - d\n",
			// c is lifted to the front; a, b and d keep grok's order. A swap
			// with whatever was first would have produced c, b, a, d.
			want: []string{"c", "a", "b", "d"},
		},
		{
			// Constructed, not captured: prose with no list in it.
			name:  "grok: prose outside the list is not a model",
			parse: parseGrokModels,
			out:   "You are not logged in.\n",
			want:  nil,
		},
		{
			name:  "opencode: one provider/model id per line",
			parse: parseOpenCodeModels,
			out:   openCodeModelsOutput,
			want: []string{
				"opencode/big-pickle",
				"opencode/deepseek-v4-flash-free",
				"opencode/mimo-v2.5-free",
				"opencode/nemotron-3-ultra-free",
				"opencode/north-mini-code-free",
			},
		},
		{
			// Issue #13's re-probe. The install changed and the ids with it;
			// the parse did not, which is the whole claim.
			name:  "opencode 1.18.21: same shape, different ids",
			parse: parseOpenCodeModels,
			out:   openCodeModelsOutput1_18,
			want: []string{
				"opencode/big-pickle",
				"opencode/hy3-free",
				"opencode/mimo-v2.5-free",
				"opencode/muse-spark-1.2-contributor-free",
				"opencode/nemotron-3-ultra-free",
				"opencode/nemotron-3.5-lightning-free",
				"opencode/x-preview-f-free",
			},
		},
		{
			// Constructed: a message rather than a list of ids.
			name:  "opencode: a banner or a message is not a model id",
			parse: parseOpenCodeModels,
			out:   "\nError: not logged in\nrun `opencode providers` to log in\n",
			want:  nil,
		},
		{
			name:  "pi: header dropped, provider and model joined",
			parse: parsePiModels,
			out:   piModelsOutput,
			want: []string{
				"groq/llama-3.1-8b-instant",
				"groq/llama-3.3-70b-versatile",
				"groq/meta-llama/llama-4-scout-17b-16e-instruct",
				"groq/qwen/qwen3-32b",
			},
		},
		{
			// Captured: `pi --list-models zzzznotamodel`, 2026-08-21. pi
			// answers with prose and exit 0 when it has no table to print,
			// which is also what it does with no provider credentials.
			name:  "pi: prose with no header is not a model",
			parse: parsePiModels,
			out:   "No models matching \"zzzznotamodel\"\n",
			want:  nil,
		},
		{
			// Constructed, and the reason the header sets the width: this is
			// a sentence with exactly as many words as the table has columns.
			// Counting fields alone would have published `No/models`.
			name:  "pi: a sentence as wide as the table is still not a model",
			parse: parsePiModels,
			out:   "No models available. Use /login to log in\n",
			want:  nil,
		},
		{
			name:  "codex: listed slugs in priority order, hidden ones withheld",
			parse: parseCodexModels,
			out:   codexModelsOutput,
			want: []string{
				"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
				"gpt-5.5", "gpt-5.4", "gpt-5.4-mini",
			},
		},
		{
			// Issue #34's re-probe, and the mirror of opencode's above: the
			// catalogue lost an entry and gained nothing, the parse did not
			// change, and both captures yield the same six ids in the same
			// order. The `want` is deliberately identical to the case above.
			name:  "codex 0.149.0: same shape, one hidden slug gone",
			parse: parseCodexModels,
			out:   codexModelsOutput0_149,
			want: []string{
				"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
				"gpt-5.5", "gpt-5.4", "gpt-5.4-mini",
			},
		},
		{
			name:  "codex: output that is not the catalogue yields nothing, not garbage",
			parse: parseCodexModels,
			out:   "ERROR failed to load models cache\n",
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.parse(tc.out)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The five declarations must agree with what was verified by execution: four
// CLIs can enumerate their own models and Claude Code cannot. A declaration
// that quietly grew or lost the hook would change what the server advertises.
func TestWhichHarnessesCanBeAsked(t *testing.T) {
	cases := []struct {
		h        *CLIHarness
		wantArgs []string
	}{
		{NewClaude(nil), nil}, // no listing command; `claude --help` has none
		{NewCodex(nil), []string{"debug", "models"}},
		{NewGrok(nil), []string{"models"}},
		{NewOpenCode(nil), []string{"models"}},
		{NewPi(nil), []string{"--list-models"}},
	}
	for _, tc := range cases {
		t.Run(tc.h.Base, func(t *testing.T) {
			if !reflect.DeepEqual(tc.h.ModelsArgs, tc.wantArgs) {
				t.Errorf("ModelsArgs = %v, want %v", tc.h.ModelsArgs, tc.wantArgs)
			}
			if (tc.h.ParseModels == nil) != (tc.wantArgs == nil) {
				t.Errorf("ParseModels presence does not match ModelsArgs %v", tc.h.ModelsArgs)
			}
		})
	}
}

// echoHarness is a CLIHarness whose "CLI" is /bin/echo, so the discovery path
// can be exercised end to end — spawn, capture, parse — without depending on
// any agent CLI being installed.
func echoHarness(t *testing.T, listed string, configured ...string) *CLIHarness {
	t.Helper()
	return (&CLIHarness{
		ID:          NewID("echo-harness"),
		Base:        "echo-harness",
		Binary:      "echo",
		Models:      configured,
		Prompt:      PromptStdin,
		BuildArgs:   func(RunRequest) ([]string, error) { return nil, nil },
		ParseLine:   passthroughParseLine,
		ModelsArgs:  []string{listed},
		ParseModels: parseOpenCodeModels,
	}).Build()
}

// A harness that can be asked is asked: what the CLI reports wins over what
// configuration guessed. This is the whole of issue #12 in one assertion.
func TestModelsPreferTheCLIOverConfiguration(t *testing.T) {
	h := echoHarness(t, "real/one\nreal/two", "configured/guess")
	got := h.models()
	want := []string{"real/one", "real/two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models() = %v, want the CLI's answer %v", got, want)
	}
	if h.DefaultModel() != "real/one" {
		t.Errorf("DefaultModel() = %q, want the first model the CLI reported", h.DefaultModel())
	}
	if err := h.validateModel("configured/guess"); err == nil {
		t.Error("a configured model the CLI does not report was still accepted")
	}
}

// A CLI that cannot be reached leaves configuration standing, rather than
// blanking the catalogue on a transient failure.
func TestModelsFallBackToConfigurationWhenTheCLICannotAnswer(t *testing.T) {
	h := echoHarness(t, "irrelevant", "configured/guess")
	h.Binary = "uhp-go-no-such-binary"
	h.Build()

	got := h.models()
	if !reflect.DeepEqual(got, []string{"configured/guess"}) {
		t.Fatalf("models() = %v, want the configured fallback", got)
	}
}

// A harness with no listing command never spawns anything: configuration is
// the only answer available, and it is returned as-is.
func TestModelsAreConfigurationWhenThereIsNoListingCommand(t *testing.T) {
	h := NewClaude([]string{"claude-sonnet-5", "claude-opus-5"})
	got := h.models()
	if !reflect.DeepEqual(got, []string{"claude-sonnet-5", "claude-opus-5"}) {
		t.Fatalf("models() = %v, want the configured list unchanged", got)
	}
}

// Discovery is called on every request, so the answer is cached rather than
// re-derived by forking a CLI each time.
func TestModelsAreCached(t *testing.T) {
	h := echoHarness(t, "real/one", "configured/guess")

	h.modelsMu.Lock()
	h.modelsCache = []string{"cached/answer"}
	h.modelsAt = time.Now()
	h.modelsMu.Unlock()

	if got := h.models(); !reflect.DeepEqual(got, []string{"cached/answer"}) {
		t.Fatalf("models() = %v, want the cached answer; the CLI was consulted again", got)
	}
}

// A stale cache is served, not waited on. validateModel is on the
// task-submission path, so the client whose request happens to cross the TTL
// must not be the one that pays for a fork; the refresh happens behind it.
func TestAStaleCacheIsRefreshedInTheBackground(t *testing.T) {
	h := echoHarness(t, "real/one", "configured/guess")

	h.modelsMu.Lock()
	h.modelsCache = []string{"stale/answer"}
	h.modelsAt = time.Now().Add(-2 * modelsTTL)
	h.modelsMu.Unlock()

	if got := h.models(); !reflect.DeepEqual(got, []string{"stale/answer"}) {
		t.Fatalf("models() = %v, want the stale answer served without blocking", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := h.models(); reflect.DeepEqual(got, []string{"real/one"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background refresh never replaced the stale answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// With no list from either source the harness knows nothing about models, and
// refusing every model would be asserting knowledge it does not have. The CLI
// gets the request and answers in its own words.
func TestValidateModelMakesNoClaimWithoutAList(t *testing.T) {
	h := NewClaude(nil)
	if err := h.validateModel("anything-at-all"); err != nil {
		t.Errorf("a harness advertising no models rejected one anyway: %v", err)
	}
}
