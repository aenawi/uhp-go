package harness

import (
	"encoding/json"

	"github.com/aenawi/uhp-go/internal/domain"
)

// NewClaude declares the Claude Code harness (`claude -p --output-format
// stream-json`). The prompt goes over stdin: verified by execution that
// `claude -p "--help"` prints usage, while the same string on stdin is
// answered as a prompt.
func NewClaude(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("claude-code"),
		Base:   "claude-code",
		Name:   "Claude Code",
		Vendor: "Anthropic",
		Binary: "claude",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapSessions, domain.CapCancellation, domain.CapTools,
		},
		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			// `--verbose` is mandatory: Claude Code rejects
			// `--print --output-format=stream-json` without it.
			args := []string{"-p", "--output-format", "stream-json", "--verbose"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, "--resume", req.NativeSessionID)
			}
			// Minimal mode: skip hooks, LSP and plugins, which a router never wants.
			return append(args, "--bare"), nil
		},
		ParseLine: parseClaudeLine,
	}).Build()
}

// claudeStreamEvent is the subset of Claude Code's stream-json schema we need.
//
// Verified against the real CLI. The stream opens with
// {"type":"system","subtype":"init","session_id":"…"}, carries
// {"type":"assistant","message":{"content":[…]}} for text, and closes with a
// result event carrying usage totals.
type claudeStreamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage *struct {
		InputTokens         int `json:"input_tokens"`
		OutputTokens        int `json:"output_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func parseClaudeLine(line string) []RunUpdate {
	var ev claudeStreamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return nil
	}
	var updates []RunUpdate

	// Emitted once, from the init event, rather than from every line that
	// happens to repeat the id.
	if ev.Type == "system" && ev.Subtype == "init" && ev.SessionID != "" {
		updates = append(updates, RunUpdate{Type: UpdateSessionID, SessionID: ev.SessionID})
	}

	if ev.Type == "assistant" {
		for _, c := range ev.Message.Content {
			if c.Type == "text" && c.Text != "" {
				updates = append(updates, RunUpdate{Type: UpdateDelta, Delta: c.Text})
			}
		}
	}

	// The result event carries the run totals.
	if ev.Type == "result" && ev.Usage != nil {
		updates = append(updates, RunUpdate{Type: UpdateUsage, Usage: &domain.Usage{
			InputTokens:      ev.Usage.InputTokens,
			OutputTokens:     ev.Usage.OutputTokens,
			TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			CacheReadTokens:  ev.Usage.CacheReadTokens,
			CacheWriteTokens: ev.Usage.CacheCreationTokens,
		}})
	}

	return updates
}
