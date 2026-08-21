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
		// `pi --list-models` prints the models pi will route to, filtered by
		// which providers have credentials — the same "computed, not asserted"
		// answer §3.1 asks for. Verified by execution; it is what caught
		// `auto`, which pi does not reject but fuzzy-matches to an unrelated
		// provider.
		ModelsArgs:  []string{"--list-models"},
		ParseModels: parsePiModels,

		Prompt: PromptStdin,
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

// parsePiModels reads the table `pi --list-models` prints:
//
//	provider  model                     context  max-out  thinking  images
//	groq      llama-3.3-70b-versatile   131.1K   32.8K    no        no
//
// The id `pi --model` takes is `provider/id`, so the first two columns are
// joined. The model column may itself contain a slash
// (`meta-llama/llama-4-scout-17b-16e-instruct`), which is why the columns are
// read positionally rather than by splitting an id apart afterwards.
//
// Nothing is read until the header has been seen, and then only lines as wide
// as the header was. With no catalogue to show pi prints prose to the same
// stream — `pi --list-models zzz` answers `No models matching "zzz"` — and the
// first two words of a sentence read as a provider and a model just as well as
// a row does. A message advertised as a model is published by /v1/models as
// available, which is the failure §3.1 calls the worst outcome for a client.
//
// Taking the width from the header rather than hard-coding six also means a
// column added to the table is followed rather than fatal.
func parsePiModels(stdout string) []string {
	var models []string
	width := 0

	for _, line := range strings.Split(stdout, "\n") {
		fields := strings.Fields(line)
		// The header names its own columns, so it identifies itself.
		if len(fields) >= 2 && fields[0] == "provider" && fields[1] == "model" {
			width = len(fields)
			continue
		}
		if width == 0 || len(fields) != width {
			continue
		}
		models = append(models, fields[0]+"/"+fields[1])
	}
	return models
}
