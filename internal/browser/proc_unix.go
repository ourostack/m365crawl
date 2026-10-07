//go:build !windows

package browser

import (
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"time"
)

// group is the unit the browser's lifetime is built on: the process group the launch created.
type group struct {
	pgid     int
	released atomic.Bool
}

// prepareCmd makes the child the leader of a new process group.
func prepareCmd(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// adopt takes ownership of a started child's group.
func adopt(cmd *exec.Cmd) (*group, error) { return &group{pgid: cmd.Process.Pid}, nil }

// processStart is recorded in the pid file next to the pid.
func processStart(*exec.Cmd) int64 { return time.Now().UnixNano() }

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func (g *group) alive() bool { return !g.released.Load() && groupAlive(g.pgid) }
func (g *group) term()       { g.signal(syscall.SIGTERM) }
func (g *group) kill()       { g.signal(syscall.SIGKILL) }

// release ends the group's claim on the process group id, which the system may hand out again.
func (g *group) release() { g.released.Store(true) }

func (g *group) signal(sig syscall.Signal) {
	if !g.released.Load() {
		_ = syscall.Kill(-g.pgid, sig)
	}
}
