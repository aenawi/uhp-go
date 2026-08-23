package harness

import (
	"encoding/json"
	"sort"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewCodex declares the Codex CLI harness.
//
// Two things here are only knowable by running the CLI:
//
//   - `--skip-git-repo-check` is required. Without it codex refuses to start
//     outside a trusted git repository ("Not inside a trusted directory"). The
//     router gives each session its own working directory, which is never a
//     git repo, so omitting this makes every codex task fail.
//   - `resume` is a subcommand, not a trailing argument:
//     `codex exec resume [OPTIONS] <SESSION_ID>`, options before positionals.
func NewCodex(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("codex"),
		Base:   "codex",
		Name:   "Codex CLI",
		Vendor: "OpenAI",
		Binary: "codex",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapSessions, domain.CapTools,
		},
		// `codex debug models` renders the raw model catalogue as JSON, which
		// is the only place the real slugs appear — `codex --help` names none,
		// and a wrong one is not rejected locally but 400s at the API after
		// the run has started. Verified by execution.
		ModelsArgs:  []string{"debug", "models"},
		ParseModels: parseCodexModels,

		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"exec"}
			if req.NativeSessionID != "" {
				args = append(args, "resume")
			}
			args = append(args, "--json", "--skip-git-repo-check")
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: parseCodexLine,
	}).Build()
}

// codexCatalog is the subset of `codex debug models` this server reads. The
// real document also carries each model's system prompt and reasoning levels,
// none of which a catalogue of ids needs.
type codexCatalog struct {
	Models []struct {
		Slug string `json:"slug"`

		// Visibility is "list" for a model a user may pick and "hide" for
		// codex's internal ones (`gpt-reserve`, `codex-auto-review`). Offering
		// a hidden model would advertise something no client should ask for.
		Visibility string `json:"visibility"`

		// Priority is codex's own ordering, lowest first. The first model this
		// server advertises is what a task naming none gets, so codex's idea
		// of its best model becomes ours rather than whatever order the
		// document happened to arrive in.
		Priority int `json:"priority"`
	} `json:"models"`
}

func parseCodexModels(stdout string) []string {
	var catalog codexCatalog
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		return nil
	}

	listed := make([]int, 0, len(catalog.Models))
	for i, m := range catalog.Models {
		if m.Slug == "" || m.Visibility != "list" {
			continue
		}
		listed = append(listed, i)
	}
	sort.SliceStable(listed, func(a, b int) bool {
		return catalog.Models[listed[a]].Priority < catalog.Models[listed[b]].Priority
	})

	if len(listed) == 0 {
		return nil
	}
	models := make([]string, 0, len(listed))
	for _, i := range listed {
		models = append(models, catalog.Models[i].Slug)
	}
	return models
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		CacheWriteTokens  int `json:"cache_write_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
}

func parseCodexLine(line string) []RunUpdate {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	switch {
	case ev.Type == "thread.started" && ev.ThreadID != "":
		// The native session id, which is what makes `codex exec resume`
		// actually resume. Verified against the real CLI.
		return []RunUpdate{{Type: UpdateSessionID, SessionID: ev.ThreadID}}
	case ev.Type == "item.completed" && ev.Item.Text != "":
		return []RunUpdate{{Type: UpdateDelta, Delta: ev.Item.Text}}
	case ev.Type == "turn.completed" && ev.Usage != nil:
		return []RunUpdate{{Type: UpdateUsage, Usage: &domain.Usage{
			InputTokens:      ev.Usage.InputTokens,
			OutputTokens:     ev.Usage.OutputTokens,
			TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			CacheReadTokens:  ev.Usage.CachedInputTokens,
			CacheWriteTokens: ev.Usage.CacheWriteTokens,
		}}}
	}
	return nil
}
