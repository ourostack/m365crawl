//go:build windows

package browsertest

import (
	"os/exec"

	"golang.org/x/sys/windows"
)

// Windows has no SIGTERM and no session to escape to; the job object contains every child.
func ignoreTerm() {}

func detach(*exec.Cmd) {}

// waitGone waits until process pid has exited.
func waitGone(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)) //nolint:gosec // G115: a pid
	if err != nil {
		return
	}
	defer windows.CloseHandle(h) //nolint:errcheck // nothing to do about a failed close
	_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
}
