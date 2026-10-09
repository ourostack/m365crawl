//go:build !windows

package browsertest

import (
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func ignoreTerm() { signal.Ignore(syscall.SIGTERM) }

// detach puts the child in its own session and process group, out of reach of a group kill.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// waitGone waits until process pid no longer exists: it has exited and its parent has reaped it.
func waitGone(pid int) {
	for syscall.Kill(pid, 0) == nil {
		time.Sleep(10 * time.Millisecond)
	}
}
