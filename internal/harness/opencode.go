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
		// `sessions` is deliberately absent, and the `--session <id>` branch
		// that used to be in BuildArgs went with it. Resuming needs two halves
		// and only one was ever here: the id has to come back out of the CLI's
		// own output before it can be passed back in, and ParseLine is the
		// passthrough, which can only ever produce text deltas. So the id was
		// never discovered, the flag was never reached, and every continuation
		// quietly started a new conversation while the harness advertised that
		// it had not. Restore both halves together — a ParseLine that emits
		// UpdateSessionID, then the flag, then the capability — once the event
		// format has been checked against the real CLI.
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapFilesOut,
			domain.CapTools,
		},
		Prompt: PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"run"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			return args, nil
		},
		ParseLine: passthroughParseLine,
	}).Build()
}
