//go:build !windows

package snapshotowner

import (
	"errors"
	"syscall"
	"testing"
)

func TestSnapshotOwnerUnixProcessEvidence(t *testing.T) {
	old := ownerKill
	self := ownerSelfPID
	ownerSelfPID = func() int { return 4243 }
	t.Cleanup(func() { ownerKill, ownerSelfPID = old, self })
	for _, tc := range []struct {
		name string
		err  error
		dead bool
	}{
		{"alive-or-reused", nil, false},
		{"positive-absent", syscall.ESRCH, true},
		{"permission-unknown", syscall.EPERM, false},
		{"other-unknown", errors.New("synthetic syscall failure"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ownerKill = func(pid int, signal syscall.Signal) error {
				calls++
				if pid != 4242 || signal != 0 {
					t.Fatalf("unsafe process probe: pid=%d signal=%d", pid, signal)
				}
				return tc.err
			}
			dead, err := platformProcessDead(4242)
			if dead != tc.dead || calls != 1 || (!tc.dead && !errors.Is(err, tc.err)) {
				t.Fatalf("process evidence dead=%t err=%v calls=%d", dead, err, calls)
			}
			if tc.dead && err != nil {
				t.Fatal(err)
			}
		})
	}
	ownerKill = func(int, syscall.Signal) error {
		t.Fatal("invalid PID reached a signal syscall")
		return nil
	}
	large := int64(maxOwnerPID) + 1
	for _, pid := range []int{0, -1, int(large)} {
		if dead, err := platformProcessDead(pid); dead || err == nil {
			t.Fatalf("invalid pid %d: dead=%t err=%v", pid, dead, err)
		}
	}
	ownerSelfPID = func() int { return 4242 }
	if dead, err := platformProcessDead(4242); dead || err != nil {
		t.Fatalf("the executing owner must be retained without a probe: %t %v", dead, err)
	}
}
