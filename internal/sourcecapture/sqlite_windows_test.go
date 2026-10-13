//go:build windows

package sourcecapture

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLockSQLiteWindowsFaultsReleaseOnlyOwnedLocks(t *testing.T) {
	for _, fault := range []string{"pending-lock", "shared-lock", "pending-unlock", "shared-unlock"} {
		t.Run(fault, func(t *testing.T) {
			path := sqliteFixture(t)
			file, err := os.Open(path) //nolint:gosec // synthetic fixture
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = file.Close() }()
			lock, unlock := lockFile, unlockFile
			t.Cleanup(func() { lockFile, unlockFile = lock, unlock })
			failed := false
			lockFile = func(handle windows.Handle, flags, reserved, low, high uint32, overlap *windows.Overlapped) error {
				if !failed && ((fault == "pending-lock" && overlap.Offset == pendingByte) ||
					(fault == "shared-lock" && overlap.Offset == sharedFirst)) {
					failed = true
					return errors.New("lock fault")
				}
				return lock(handle, flags, reserved, low, high, overlap)
			}
			unlockFile = func(handle windows.Handle, reserved, low, high uint32, overlap *windows.Overlapped) error {
				if !failed && ((fault == "pending-unlock" && overlap.Offset == pendingByte) ||
					(fault == "shared-unlock" && overlap.Offset == sharedFirst)) {
					failed = true
					return errors.New("unlock fault")
				}
				return unlock(handle, reserved, low, high, overlap)
			}
			release, err := LockSQLite(file)
			if fault == "shared-unlock" {
				if err != nil {
					t.Fatal(err)
				}
				if err := release(); err == nil {
					t.Fatal("unlock failure hidden")
				}
			} else if err == nil || release != nil {
				t.Fatal("lock failure admitted")
			}
			lockFile, unlockFile = lock, unlock
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if output := writerProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("native protection leaked: %s", output)
			}
		})
	}
}
