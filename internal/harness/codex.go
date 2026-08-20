package harness

import (
	"encoding/json"

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
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapSessions, domain.CapCancellation, domain.CapTools,
		},
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
