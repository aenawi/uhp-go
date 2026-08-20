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

	// HarnessStore is where harnesses created over the API are kept. Empty
	// means harness management is off, and discovery reports it as off:
	// a harness that does not survive a restart is not configuration, so the
	// capability is only offered when there is somewhere to keep one.
	HarnessStore string

	// PublicBaseURL is the origin clients reach this server on. It is used to
	// make artifact download URLs absolute; unset means they are emitted
	// relative, which is correct whenever the client shares the API's origin.
	PublicBaseURL string

	ClaudeModels   []string
	CodexModels    []string
	GrokModels     []string
	OpenCodeModels []string
	PiModels       []string
}

func Load() Config {
	workspace := os.Getenv("UHP_WORKSPACE")
	return Config{
		Addr:           getEnv("UHP_ADDR", ":8080"),
		APIKeys:        splitCSV(os.Getenv("UHP_API_KEYS")),
		Workspace:      workspace,
		HarnessStore:   harnessStorePath(os.Getenv("UHP_HARNESS_STORE"), workspace),
		MaxBodyBytes:   getEnvInt("UHP_MAX_BODY_BYTES", 8<<20),
		PublicBaseURL:  strings.TrimSuffix(os.Getenv("UHP_PUBLIC_URL"), "/"),
		ClaudeModels:   splitCSVDefault(os.Getenv("UHP_CLAUDE_MODELS"), "claude-sonnet-4.6", "claude-opus-4.6"),
		CodexModels:    splitCSVDefault(os.Getenv("UHP_CODEX_MODELS"), "gpt-5.2-codex"),
		GrokModels:     splitCSVDefault(os.Getenv("UHP_GROK_MODELS"), "grok-4.6", "grok-4.5"),
		OpenCodeModels: splitCSVDefault(os.Getenv("UHP_OPENCODE_MODELS"), "auto"),
		PiModels:       splitCSVDefault(os.Getenv("UHP_PI_MODELS"), "auto"),
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
