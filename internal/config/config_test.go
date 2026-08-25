package config

import (
	"path/filepath"
	"testing"
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
