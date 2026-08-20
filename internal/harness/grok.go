package harness

import "github.com/aenawi/uhp-go/internal/domain"

// NewGrok declares the Grok CLI harness.
//
// Grok is the one harness that must put the prompt in argv: it does not read a
// prompt from stdin (`grok -p` with a piped prompt still reports "a value is
// required for '--single <PROMPT>'"). Nor does `--` help, because the prompt is
// the value of `-p` rather than a positional, and the parser rejects a value
// beginning with a hyphen. The attached form `-p=<prompt>` is the only shape
// that carries an arbitrary prompt safely, verified by execution.
func NewGrok(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:     NewID("grok-cli"),
		Base:   "grok-cli",
		Name:   "Grok CLI",
		Vendor: "xAI",
		Binary: "grok",
		Models: models,
		Capabilities: []domain.Capability{
			domain.CapStreaming, domain.CapFilesIn, domain.CapTools,
		},
		Prompt: PromptArgs,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"-p=" + req.Input}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			return args, nil
		},
		ParseLine: passthroughParseLine,
	}).Build()
}
