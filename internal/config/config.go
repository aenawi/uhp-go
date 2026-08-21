// Package config loads runtime configuration from environment variables,
// keeping cmd/uhpd/main.go a thin composition root.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Addr         string
	APIKeys      []string
	Workspace    string
	MaxBodyBytes int64

	// MaxConcurrentRuns bounds how many harness processes run at once. Every
	// accepted task forks a CLI, so without a bound a single unauthenticated
	// caller can fork the host to a standstill.
	MaxConcurrentRuns int

	// HarnessStore is where harnesses created over the API are kept. Empty
	// means harness management is off, and discovery reports it as off:
	// a harness that does not survive a restart is not configuration, so the
	// capability is only offered when there is somewhere to keep one.
	HarnessStore string

	// PublicBaseURL is the origin clients reach this server on. It is used to
	// make artifact download URLs absolute; unset means they are emitted
	// relative, which is correct whenever the client shares the API's origin.
	PublicBaseURL string

	// The five model lists are fallbacks, not the advertised catalogue. Four
	// of the five CLIs can enumerate their own models and are asked; these
	// values stand only where a CLI cannot be asked or cannot be reached. See
	// harness.CLIHarness.models.
	ClaudeModels   []string
	CodexModels    []string
	GrokModels     []string
	OpenCodeModels []string
	PiModels       []string
}

func Load() Config {
	workspace := os.Getenv("UHP_WORKSPACE")
	return Config{
		Addr:         getEnv("UHP_ADDR", ":8080"),
		APIKeys:      splitCSV(os.Getenv("UHP_API_KEYS")),
		Workspace:    workspace,
		HarnessStore: harnessStorePath(os.Getenv("UHP_HARNESS_STORE"), workspace),
		MaxBodyBytes: getEnvInt("UHP_MAX_BODY_BYTES", 8<<20),
		// Zero is passed straight through to the service, which substitutes its
		// own default. Config does not carry a second copy of that number.
		MaxConcurrentRuns: int(getEnvInt("UHP_MAX_CONCURRENT_RUNS", 0)),
		PublicBaseURL:     strings.TrimSuffix(os.Getenv("UHP_PUBLIC_URL"), "/"),
		// Claude Code cannot be asked what it serves, so this list is the only
		// answer there will be. Both ids were checked against the real CLI on
		// 2026-08-21: the previous `claude-sonnet-4.6` / `claude-opus-4.6`
		// still resolve but are retired, and the CLI says so.
		ClaudeModels: splitCSVDefault(os.Getenv("UHP_CLAUDE_MODELS"), "claude-sonnet-5", "claude-opus-5"),
		// Read from `codex debug models` on 2026-08-21, where it is the
		// highest-priority listed slug. Reached when codex is not installed,
		// or is installed but cannot render its catalogue.
		CodexModels: splitCSVDefault(os.Getenv("UHP_CODEX_MODELS"), "gpt-5.6-sol"),
		// Read from `grok models` on 2026-08-21, default first.
		GrokModels: splitCSVDefault(os.Getenv("UHP_GROK_MODELS"), "grok-4.6", "grok-4.5"),
		// No fallback for either, deliberately. Both route through whichever
		// providers the operator has logged in to, so there is no id that is
		// true on someone else's machine — `auto`, which both used to
		// advertise, is not a model to either of them. Empty means this server
		// advertises no model rather than a wrong one, `--model` is then left
		// off the command line and the CLI picks its own, and an operator who
		// wants a fixed list sets UHP_OPENCODE_MODELS / UHP_PI_MODELS.
		OpenCodeModels: splitCSV(os.Getenv("UHP_OPENCODE_MODELS")),
		PiModels:       splitCSV(os.Getenv("UHP_PI_MODELS")),
	}
}

func getEnvInt(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func splitCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitCSVDefault(v string, fallback ...string) []string {
	if parsed := splitCSV(v); len(parsed) > 0 {
		return parsed
	}
	return fallback
}

// harnessStorePath resolves where created harnesses are kept.
//
// An explicit UHP_HARNESS_STORE wins. Otherwise a configured workspace implies
// one, because a deployment that has given this server a durable directory has
// already answered the only question that matters. With neither, the path is
// empty and harness management is off rather than in-memory: offering it and
// losing every harness on the next restart is worse than not offering it.
func harnessStorePath(explicit, workspace string) string {
	if explicit != "" {
		return explicit
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, "harnesses.json")
}
