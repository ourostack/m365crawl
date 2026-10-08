//go:build !windows

package browsertest

import (
	"os/exec"
	"os/signal"
	"syscall"
)

func ignoreTerm() { signal.Ignore(syscall.SIGTERM) }

// detach puts the child in its own session and process group, out of reach of a group kill.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
