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
	dir, err := root.sessionPath(sessionID)
	if err != nil || dir == "" {
		return dir, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("service: create session workspace: %w", err)
	}
	return dir, nil
}

// removeSession deletes a session's working directory and everything in it.
//
// It lives here, beside the function that creates the directory, rather than in
// the service method that wants it gone: this type owns the mapping from a
// session id to a path, and a caller that had to build the path in order to
// remove it would be the second place that knows the layout. A session with no
// directory — never ran a task, or no workspace configured at all — is removed
// successfully, because RemoveAll treats an absent path as done and so does
// UHP: DELETE is a statement about the end state, not about work performed.
func (root WorkspaceRoot) removeSession(sessionID string) error {
	dir, err := root.sessionPath(sessionID)
	if err != nil || dir == "" {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("service: remove session workspace: %w", err)
	}
	return nil
}

// sessionPath is where a session's working directory is, without creating it.
//
// It is separate from sessionDir because deletion is the one caller that must
// not create what it is about to remove: DELETE on a session that never ran a
// task would otherwise mkdir a directory in order to rmdir it, and a delete
// that leaves the filesystem dirtier for a moment is a delete that can fail
// having made a mess. An empty root — no workspace configured — yields an empty
// path and no error, which every caller reads as "there is nothing here".
func (root WorkspaceRoot) sessionPath(sessionID string) (string, error) {
	if root == "" {
		return "", nil
	}
	// The session id is server-generated, but it ends up in a filesystem path,
	// so it is validated rather than trusted.
	if !domain.ValidSessionID(sessionID) {
		return "", fmt.Errorf("service: refusing to build a workspace path from session id %q", sessionID)
	}
	return filepath.Join(string(root), sessionID), nil
}
