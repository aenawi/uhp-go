package harness

import "github.com/aenawi/uhp-go/internal/domain"

// NewOpenCode declares the OpenCode harness (`opencode run`).
//
// UNVERIFIED: unlike the other four, this invocation has not been checked
// against the real CLI, because opencode is not installed on any machine this
// has been developed on. Stdin delivery is chosen as the conservative default —
// it is the only mode that is safe without knowing the CLI's option parser —
// but if opencode turns out not to read a prompt from stdin, this harness will
// hang rather than run, exactly as grok would have. Verify before relying on it.
//
// `--print-logs` was removed: it is not part of the documented invocation and
// its output was being streamed to clients verbatim as answer text.
func NewOpenCode(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("opencode"),
		Base:   "opencode",
		Name:   "OpenCode",
		Vendor: "sst.dev",
		Binary: "opencode",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapSessions, domain.CapTools,
		},
		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"run"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			if req.NativeSessionID != "" {
				args = append(args, "--session", req.NativeSessionID)
			}
			return args, nil
		},
		ParseLine: passthroughParseLine,
	}).Build()
}
