//go:build windows

package actions

import "os/exec"

// setupProcessGroup is a no-op on Windows: exec.Cmd's default cancel
// (Kill the process) is what we want here; there is no
// Setpgid-equivalent that reliably terminates a whole process tree,
// and JobObject wiring is beyond the scope of this action. Long-lived
// grandchildren on Windows will fall to the WaitDelay pipe-close path
// like anywhere else.
func setupProcessGroup(cmd *exec.Cmd) {}
