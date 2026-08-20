//go:build !unix

package harness

import "os/exec"

// isolateProcessGroup is a no-op where process groups are unavailable;
// cancellation falls back to exec.CommandContext killing the direct child.
func isolateProcessGroup(cmd *exec.Cmd) {}
