//go:build !windows

package workiq

import (
	"errors"
	"os/exec"
	"syscall"
)

type processGroup struct{ pid int }

var signalOwnedGroup = syscall.Kill

func commandFor(binary string, args []string) (*exec.Cmd, error) {
	return exec.Command(binary, args...), nil //nolint:gosec // executable is explicit local configuration; argv is fixed/validated
}

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func adoptProcess(cmd *exec.Cmd) (*processGroup, error) {
	return &processGroup{pid: cmd.Process.Pid}, nil
}

func (g *processGroup) end() error {
	err := signalOwnedGroup(-g.pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return failure("workiq_cleanup_failed")
	}
	return nil
}
