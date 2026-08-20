package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceRoot is where per-session working directories are created.
type WorkspaceRoot string

// sessionDir returns the working directory for a session, creating it if
// needed.
//
// Every harness used to run in the router's own current working directory,
// which meant every task could see and edit the router's source tree, and a
// resumed session had no stable directory of its own. UHP Lifecycle §4
// requires a session to preserve "the working directory and its files" across
// the tasks in its chain, so the directory has to be per-session and durable
// for the session's life.
func (root WorkspaceRoot) sessionDir(sessionID string) (string, error) {
	if root == "" {
		return "", nil
	}
	// The session id is server-generated, but it ends up in a filesystem path,
	// so it is validated rather than trusted.
	if !safeSessionID(sessionID) {
		return "", fmt.Errorf("service: refusing to build a workspace path from session id %q", sessionID)
	}
	dir := filepath.Join(string(root), sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("service: create session workspace: %w", err)
	}
	return dir, nil
}

// safeSessionID allows only the shape the router itself mints: "sess_" plus
// hex and dashes. Anything else cannot become a path component.
func safeSessionID(id string) bool {
	if !strings.HasPrefix(id, "sess_") || len(id) > 128 {
		return false
	}
	for _, r := range id[len("sess_"):] {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r == '-':
		default:
			return false
		}
	}
	return true
}
