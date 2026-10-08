//go:build !windows

package browser

import (
	"errors"
	"syscall"
	"testing"
)

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func requireNoProfileProcs(t *testing.T, profile string) {
	t.Helper()
	if procs, err := profileProcs(profile); err != nil || len(procs) != 0 {
		t.Fatalf("processes still run with the profile: %v %v", procs, err)
	}
}
