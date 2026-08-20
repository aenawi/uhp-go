package harness

import "github.com/aenawi/uhp-go/internal/domain"

// NewPi declares the Pi harness.
//
// The previous invocation was `pi run <prompt>`, which is wrong twice: pi has
// no `run` subcommand (its usage is `pi [options] [@files...] [messages...]`,
// so "run" was silently prepended to every prompt as a word), and `-p` is
// required for non-interactive mode. Pi also has no option terminator — `pi -p
// -- "--help"` still prints help — so stdin is the only safe delivery.
func NewPi(models []string) *CLIHarness {
	return (&CLIHarness{
		ID:           NewID("pi"),
		Base:         "pi",
		Name:         "Pi",
		Vendor:       "community",
		Binary:       "pi",
		Models:       models,
		Capabilities: []domain.Capability{domain.CapStreaming, domain.CapTools},
		Prompt:       PromptStdin,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"-p"}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			return args, nil
		},
		ParseLine: passthroughParseLine,
	}).Build()
}
