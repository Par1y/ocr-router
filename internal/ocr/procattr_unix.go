//go:build !windows

package ocr

import (
	"os/exec"
	"syscall"
)

// setProcGroup places the child renderer in its own process group and, on
// context cancellation, kills the whole group (SIGKILL to -pgid) rather than
// just the direct child. PDF renderers (pdftoppm/mutool) rarely fork, but this
// guarantees no orphaned rasterizer survives a cancelled extract run.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID targets the process group led by the child.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return cmd.Process.Kill()
	}
}
