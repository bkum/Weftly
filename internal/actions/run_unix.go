//go:build !windows

package actions

import (
	"os/exec"
	"syscall"
)

// setupProcessGroup puts the child in its own process group so cancel
// can SIGKILL the whole tree (parent + any grandchildren). On POSIX,
// Setpgid=true guarantees pgid == pid so `syscall.Kill(-pid, ...)`
// hits every descendant.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	// On ctx cancel (per-step timeout or DELETE /runs), kill the whole
	// process group rather than just the parent shell. Without this an
	// orphaned grandchild like `sleep 5` inside a killed bash keeps
	// the inherited stdio FDs open and WaitDelay never fires.
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
