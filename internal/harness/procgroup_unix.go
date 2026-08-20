//go:build unix

package harness

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup puts the child in its own process group and makes
// cancellation kill that whole group.
//
// Harness CLIs are wrappers that spawn their own children (node, python, a
// shell). exec.CommandContext only signals the direct child, so a grandchild
// survives holding the inherited stdout pipe open — the pipe never reaches
// EOF, the reader blocks in Scan forever, and no terminal update is ever
// emitted. Killing the group closes every copy of the pipe.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid targets the group; Setpgid makes the group id equal
		// to the child's pid.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
