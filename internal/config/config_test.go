package config

import (
	"path/filepath"
	"testing"
	"time"
)

// A deployment that has given this server a durable directory has already
// answered the only question that matters, so it does not have to answer it
// twice. An explicit path still wins, because an operator who names one means
// it.
func TestDatabasePath(t *testing.T) {
	tests := []struct {
		name      string
		explicit  string
		workspace string
		want      string
	}{
		{"explicit wins", "/var/lib/uhp/tasks.db", "/workspace", "/var/lib/uhp/tasks.db"},
		{"workspace implies one", "", "/workspace", filepath.Join("/workspace", "uhp.db")},
		{"neither means in memory", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := databasePath(tc.explicit, tc.workspace); got != tc.want {
				t.Fatalf("databasePath(%q, %q) = %q, want %q", tc.explicit, tc.workspace, got, tc.want)
			}
		})
	}
}

// The two stores answer to the same rule, and share a workspace without
// sharing a file.
func TestStorePathsDoNotCollide(t *testing.T) {
	db := databasePath("", "/workspace")
	harnesses := harnessStorePath("", "/workspace")
	if db == harnesses {
		t.Fatalf("both stores resolved to %q", db)
	}
}

// The switch that opens an unauthenticated read path is the one place where a
// value nobody agrees the meaning of must not be read as "yes".
//
// An operator who meant to enable sharing and typed "on" gets a server that did
// not, and finds out from the discovery document. The other way round, they get
// a server serving links they never asked it to serve, and find out from
// whoever opened one.
func TestSessionSharingIsOffUnlessItIsUnambiguouslyOn(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " true ", "True"} {
		if !envBool(v) {
			t.Errorf("envBool(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "0", "false", "on", "yes", "enabled", "y", "maybe"} {
		if envBool(v) {
			t.Errorf("envBool(%q) = true, want false", v)
		}
	}
}

func TestSessionSharingDefaultsOff(t *testing.T) {
	t.Setenv("UHP_SESSION_SHARING", "")
	if Load().SessionSharing {
		t.Error("session sharing is on with the variable unset")
	}
	t.Setenv("UHP_SESSION_SHARING", "1")
	if !Load().SessionSharing {
		t.Error("session sharing is off with UHP_SESSION_SHARING=1")
	}
}

// UHP_TASK_TIMEOUT is the deployment's own bound on how long a task may run
// (#54). It accepts a Go duration because that is what an operator writing
// "30m" means, and a bare number because the protocol field it backstops is
// spelled `timeout_seconds` — so "1800" and "30m" are the same configuration.
//
// Zero is passed straight through to the service, which substitutes its own
// default, on the same rule MaxConcurrentRuns follows: config does not carry a
// second copy of that number.
func TestTaskTimeoutIsParsed(t *testing.T) {
	cases := []struct {
		set  string
		want time.Duration
	}{
		{"", 0},
		{"90m", 90 * time.Minute},
		{"45s", 45 * time.Second},
		{"1800", 30 * time.Minute},
		// Nothing here means "unbounded": Security §5 requires a server to
		// bound task duration, so a value that cannot be read as a positive
		// duration falls back to the service's default rather than switching
		// the bound off.
		{"0", 0},
		{"-5m", 0},
		{"soon", 0},
	}
	for _, tc := range cases {
		t.Run(tc.set, func(t *testing.T) {
			t.Setenv("UHP_TASK_TIMEOUT", tc.set)
			if got := Load().TaskTimeout; got != tc.want {
				t.Errorf("TaskTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}

// UHP_TASK_MAX_STEP falls back to *no* ceiling rather than to a default one, so
// a typo silently unbounds the deployment — the one direction a bound must never
// fail in. The value is kept verbatim so CheckStepBudget can say so (#72).
func TestAnUnreadableStepCeilingIsRecorded(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		want    int
		flagged bool
	}{
		{"", 0, false},        // unset: the ordinary deployment, and nothing to warn about
		{"20", 20, false},     // a real ceiling
		{"twenty", 0, true},   // a typo that would otherwise mean "unbounded"
		{"0", 0, true},        // not a ceiling; a deployment permitting no tool calls at all
		{"-1", 0, true},       // meaningless
		{"  20  ", 20, false}, // whitespace is not a typo
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("UHP_TASK_MAX_STEP", tc.raw)
			cfg := Load()
			if cfg.TaskMaxStep != tc.want {
				t.Errorf("TaskMaxStep = %d, want %d", cfg.TaskMaxStep, tc.want)
			}
			// Set-but-unusable is the case that must be distinguishable from
			// unset, because only one of the two is worth telling an operator
			// about.
			flagged := cfg.TaskMaxStepRaw != "" && cfg.TaskMaxStep <= 0
			if flagged != tc.flagged {
				t.Errorf("flagged = %v, want %v: TaskMaxStepRaw = %q",
					flagged, tc.flagged, cfg.TaskMaxStepRaw)
			}
		})
	}
}
