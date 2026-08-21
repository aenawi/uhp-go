package harness

import (
	"strings"

	"github.com/aenawi/uhp-go/internal/domain"
)

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
		// `grok models` prints the models this login can actually use, so the
		// advertised list is computed rather than guessed. Verified by
		// execution; it is what caught `grok-4.1`, which never existed.
		ModelsArgs:  []string{"models"},
		ParseModels: parseGrokModels,

		Prompt: PromptArgs,
		BuildArgs: func(req RunRequest) ([]string, error) {
			args := []string{"-p=" + req.Input}
			if req.Model != "" {
				args = append(args, "--model", req.Model)
			}
			return args, nil
		},
		ParseLine: passthroughParseLine,

		// Verified against `grok --help`: "--disallowed-tools <TOOLS>  Built-in
		// tools to remove (comma-separated)". A real block, so the router does
		// not fall back to asking the model nicely.
		DisallowArgs: func(tools []string) []string {
			return []string{"--disallowed-tools", strings.Join(tools, ",")}
		},

		// No MCPArgs: grok configures MCP through its own `grok mcp`
		// subcommand, which writes global state rather than taking a per-run
		// file. Declaring one here would advertise support this server cannot
		// deliver for a single turn, which §4.1 forbids.
	}).Build()
}

// parseGrokModels reads the list `grok models` prints:
//
//	You are logged in with grok.com.
//
//	Default model: grok-4.6
//
//	Available models:
//	  * grok-4.6 (default)
//	  - grok-4.5
//
// Only lines inside the list are read, so the prose above it — which is where
// "you are not logged in" appears — can never become a model id. The starred
// entry is moved to the front, leaving the rest in the order grok printed
// them, because the first model advertised is the one a task that names none
// gets.
func parseGrokModels(stdout string) []string {
	var models []string
	def := -1
	inList := false

	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Available models:") {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		isDefault := strings.HasPrefix(line, "* ")
		if !isDefault && !strings.HasPrefix(line, "- ") {
			continue
		}
		id := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line[2:]), "(default)"))
		if id == "" {
			continue
		}
		if isDefault {
			def = len(models)
		}
		models = append(models, id)
	}

	if def > 0 {
		// Lifted out and put back at the front, not swapped with whatever was
		// first: a swap would also move that one to the middle of the list.
		promoted := models[def]
		models = append(models[:def], models[def+1:]...)
		models = append([]string{promoted}, models...)
	}
	return models
}
