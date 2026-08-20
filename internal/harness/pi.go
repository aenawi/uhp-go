package harness

import (
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

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

		// Both verified against `pi --help`:
		//   --exclude-tools, -xt <tools>  Comma-separated denylist of tool names
		//   --skill <path>                Load a skill file or directory
		// pi is the one runtime here that loads a skill folder itself, so its
		// skills do not need the standing-instruction fallback.
		DisallowArgs: func(tools []string) []string {
			return []string{"--exclude-tools", strings.Join(tools, ",")}
		},
		SkillArgs: func(dirs []string) []string {
			args := make([]string, 0, len(dirs)*2)
			for _, dir := range dirs {
				args = append(args, "--skill", dir)
			}
			return args
		},
	}).Build()
}
