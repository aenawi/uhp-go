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
