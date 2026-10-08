//go:build windows

package browsertest

import "os/exec"

// Windows has no SIGTERM and no session to escape to; the job object contains every child.
func ignoreTerm() {}

func detach(*exec.Cmd) {}
