//go:build windows

package snapshotowner

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSnapshotOwnerWindowsProcessEvidence(t *testing.T) {
	open, query, close := ownerOpenProcess, ownerExitCode, ownerCloseHandle
	self := ownerSelfPID
	ownerSelfPID = func() int { return 4243 }
	t.Cleanup(func() {
		ownerOpenProcess, ownerExitCode, ownerCloseHandle = open, query, close
		ownerSelfPID = self
	})
	fault := errors.New("synthetic handle failure")
	for _, tc := range []struct {
		name                        string
		code                        uint32
		openErr, queryErr, closeErr error
		dead                        bool
	}{
		{name: "live-or-reused", code: 259},
		{name: "terminated", code: 0, dead: true},
		{name: "terminated-nonzero", code: 42, dead: true},
		{name: "access-denied-unknown", openErr: windows.ERROR_ACCESS_DENIED},
		{name: "invalid-parameter-unknown", openErr: windows.ERROR_INVALID_PARAMETER},
		{name: "other-open-unknown", openErr: fault},
		{name: "query-unknown", queryErr: fault},
		{name: "close-unknown", closeErr: fault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries, closes := 0, 0
			ownerOpenProcess = func(access uint32, inherit bool, pid uint32) (windows.Handle, error) {
				if access != windows.PROCESS_QUERY_LIMITED_INFORMATION || inherit || pid != 4242 {
					t.Fatalf("unsafe process handle request: %d %t %d", access, inherit, pid)
				}
				if tc.openErr != nil {
					return 0, tc.openErr
				}
				return 42, nil
			}
			ownerExitCode = func(h windows.Handle, code *uint32) error {
				queries++
				if h != 42 {
					t.Fatalf("queried unexpected handle %v", h)
				}
				*code = tc.code
				return tc.queryErr
			}
			ownerCloseHandle = func(h windows.Handle) error {
				closes++
				if h != 42 {
					t.Fatalf("closed unexpected handle %v", h)
				}
				return tc.closeErr
			}
			dead, err := platformProcessDead(4242)
			wantCalls := 1
			if tc.openErr != nil {
				wantCalls = 0
			}
			wantErr := errors.Join(tc.openErr, tc.queryErr, tc.closeErr)
			if dead != tc.dead || queries != wantCalls || closes != wantCalls ||
				(err == nil) != (wantErr == nil) || (wantErr != nil && !errors.Is(err, firstError(tc.openErr, tc.queryErr, tc.closeErr))) {
				t.Fatalf("dead=%t err=%v query=%d close=%d", dead, err, queries, closes)
			}
		})
	}
	for _, h := range []windows.Handle{0, windows.InvalidHandle} {
		ownerOpenProcess = func(uint32, bool, uint32) (windows.Handle, error) { return h, nil }
		ownerExitCode = func(windows.Handle, *uint32) error {
			t.Fatal("invalid handle was queried")
			return nil
		}
		ownerCloseHandle = func(windows.Handle) error {
			t.Fatal("invalid handle was closed")
			return nil
		}
		if dead, err := platformProcessDead(4242); dead || err == nil {
			t.Fatalf("invalid handle authorized death: %t %v", dead, err)
		}
	}
	ownerOpenProcess = func(uint32, bool, uint32) (windows.Handle, error) {
		t.Fatal("invalid PID reached OpenProcess")
		return 0, nil
	}
	large := int64(maxOwnerPID) + 1
	for _, pid := range []int{0, -1, int(large)} {
		if dead, err := platformProcessDead(pid); dead || err == nil {
			t.Fatalf("invalid pid %d: dead=%t err=%v", pid, dead, err)
		}
	}
	ownerSelfPID = func() int { return 4242 }
	if dead, err := platformProcessDead(4242); dead || err != nil {
		t.Fatalf("the executing owner must be retained without OpenProcess: %t %v", dead, err)
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
