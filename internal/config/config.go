// Package config loads runtime configuration from environment variables,
// keeping cmd/uhpd/main.go a thin composition root.
package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Addr         string
	APIKeys      []string
	Workspace    string
	MaxBodyBytes int64

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
	return Config{
		Addr:           getEnv("UHP_ADDR", ":8080"),
		APIKeys:        splitCSV(os.Getenv("UHP_API_KEYS")),
		Workspace:      os.Getenv("UHP_WORKSPACE"),
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
