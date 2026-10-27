//go:build !windows

package workiq

import (
	"context"
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
