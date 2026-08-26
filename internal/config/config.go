// Package config loads runtime configuration from environment variables,
// keeping cmd/uhpd/main.go a thin composition root.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// Addr is the listen address, and it defaults to loopback rather than to
	// every interface. Authentication is off unless UHP_API_KEYS is set, so
	// the default bind is what decides whether the default server is a local
	// tool or an open one; a deployment that wants to be reachable says so,
	// and CheckAuthPosture makes it say so with a credential.
	Addr         string
	APIKeys      []string
	Workspace    string
	MaxBodyBytes int64

	// MaxConcurrentRuns bounds how many harness processes run at once. Every
	// accepted task forks a CLI, so without a bound a single unauthenticated
	// caller can fork the host to a standstill.
	MaxConcurrentRuns int

	// TaskTimeout is the longest a task may run here: the budget applied when
	// neither the request nor the harness names one, and the ceiling both are
	// clamped to. Zero means unset and the service substitutes its own default,
	// which is not the same as unbounded — Security §5 requires a server to
	// bound task duration, so there is deliberately no way to switch it off.
	TaskTimeout time.Duration

	// HarnessStore is where harnesses created over the API are kept. Empty
	// means harness management is off, and discovery reports it as off:
	// a harness that does not survive a restart is not configuration, so the
	// capability is only offered when there is somewhere to keep one.
	HarnessStore string

	// Database is the SQLite file that holds tasks and sessions. Empty means
	// they are kept in memory and are gone on the next restart, which uhpd
	// warns about on startup. See store.SQLiteStore for why that is a warning
	// and not a silent default.
	Database string

	// DefaultHarness is the harness a task that names none runs on. Empty
	// means the server infers it, which it can only do when exactly one
	// harness is ready; see service.DefaultHarness.
	DefaultHarness string

	// PublicBaseURL is the origin clients reach this server on. It is used to
	// make artifact download URLs absolute; unset means they are emitted
	// relative, which is correct whenever the client shares the API's origin.
	//
	// It is worth setting once SessionSharing is on: a share is a link someone
	// sends to someone else, and a relative one is not a link.
	PublicBaseURL string

	// SessionSharing is whether this server serves the read-only session views
	// of Sessions §5. It defaults to off, and that default is a security
	// posture rather than an oversight.
	//
	// Every other capability here is gated on having somewhere to put
	// something — a workspace for files, a store for harnesses. This one is
	// gated on consent: turning it on makes a server that answered nothing
	// without a credential start answering some things without one, which is a
	// change to what the deployment *is* and belongs to whoever runs it. With
	// it off, discovery reports `session_sharing: false` and the endpoints
	// answer 501, which is the honest pair.
	SessionSharing bool

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
		Addr:         getEnv("UHP_ADDR", "127.0.0.1:8080"),
		APIKeys:      splitCSV(os.Getenv("UHP_API_KEYS")),
		Workspace:    workspace,
		HarnessStore: harnessStorePath(os.Getenv("UHP_HARNESS_STORE"), workspace),
		Database:     databasePath(os.Getenv("UHP_DB"), workspace),
		MaxBodyBytes: getEnvInt("UHP_MAX_BODY_BYTES", 8<<20),
		// Zero is passed straight through to the service, which substitutes its
		// own default. Config does not carry a second copy of that number.
		MaxConcurrentRuns: int(getEnvInt("UHP_MAX_CONCURRENT_RUNS", 0)),
		TaskTimeout:       envDuration(os.Getenv("UHP_TASK_TIMEOUT")),
		PublicBaseURL:     strings.TrimSuffix(os.Getenv("UHP_PUBLIC_URL"), "/"),
		SessionSharing:    envBool(os.Getenv("UHP_SESSION_SHARING")),
		DefaultHarness:    strings.TrimSpace(os.Getenv("UHP_DEFAULT_HARNESS")),
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

// maxDuration is the largest time.Duration, which is a little under 292 years.
const maxDuration = time.Duration(1<<63 - 1)

// envDuration reads a duration an operator may have written either way.
//
// A Go duration — "30m", "90s" — is what someone naming a timeout writes, and a
// bare number is read as seconds because the protocol field this backstops is
// spelled `timeout_seconds`, so an operator who has been reading the Tasks
// chapter reaches for "1800" and means the same thing.
//
// Anything else, including a value that is not positive, answers zero: the
// caller substitutes its own default, and the one thing that must not happen is
// a typo switching the bound off. See the note on Config.TaskTimeout.
func envDuration(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(v, 10, 64); err == nil {
		// The upper guard is not defensiveness for its own sake: multiplying a
		// number of seconds larger than a Duration can hold wraps, and a wrap
		// that lands positive is a bound nobody chose.
		if seconds <= 0 || seconds > int64(maxDuration/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// envBool reads a boolean switch, accepting the spellings an operator actually
// types and treating everything else as off.
//
// The asymmetry is deliberate: the only variable that uses this turns on an
// unauthenticated read path, so a value nobody can agree the meaning of —
// "yes", "on", "enabled", "maybe" — must not be the one that opens it. An
// operator who meant to enable sharing and typed "on" gets a server that did
// not, which they discover from the discovery document; the other way round,
// they get a server serving links they never asked it to serve.
func envBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true":
		return true
	default:
		return false
	}
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

// databasePath resolves where tasks and sessions are kept, on the same rule
// harnessStorePath uses and for the same reason: an explicit UHP_DB wins, and
// otherwise a configured workspace implies one, because a deployment that has
// given this server a durable directory has already answered the only question
// that matters.
//
// With neither, the path is empty and the store is in memory. That differs
// from harness management, which is switched off rather than made volatile:
// there is no such thing as running without somewhere to put a task, so the
// choice here is between a volatile store and no server at all.
func databasePath(explicit, workspace string) string {
	if explicit != "" {
		return explicit
	}
	if workspace == "" {
		return ""
	}
	return filepath.Join(workspace, "uhp.db")
}
