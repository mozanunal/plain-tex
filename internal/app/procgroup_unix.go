//go:build unix

package app

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup puts the child in its own process group and, when the
// context is cancelled (a compile timeout), kills the whole group. A compiler
// spawns helpers of its own, so killing only the direct child would leave them
// running.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 3 * time.Second
}
