//go:build !windows

package workiq

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestWorkIQOwnedGroupTerminationFailure(t *testing.T) {
	useStub(t, "")
	client, err := Open(context.Background(), Invocation{}, "")
	if err != nil {
		t.Fatal(err)
	}
	old := signalOwnedGroup
	signalOwnedGroup = func(int, syscall.Signal) error { return syscall.EPERM }
	defer func() { signalOwnedGroup = old }()
	// Closing owned stdin lets this helper exit before the group error is injected.
	err = client.Close()
	requireCode(t, err, "workiq_cleanup_failed")
}

func TestWorkIQSignedOutChildIsKilledAndReaped(t *testing.T) {
	shortHandshake(t)
	pid := filepath.Join(t.TempDir(), "pid")
	t.Setenv("WORKIQ_TEST_PID", pid)
	useStub(t, "signed-out")
	_, err := Open(context.Background(), Invocation{}, "")
	requireCode(t, err, "workiq_signin_required")
	raw, err := os.ReadFile(filepath.Clean(pid))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(n, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("the signed-out child outlived its handshake", err)
	}
}
