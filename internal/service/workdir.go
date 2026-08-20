package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aenawi/uhp-go/internal/domain"
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
	if !domain.ValidSessionID(sessionID) {
		return "", fmt.Errorf("service: refusing to build a workspace path from session id %q", sessionID)
	}
	dir := filepath.Join(string(root), sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("service: create session workspace: %w", err)
	}
	return dir, nil
}
