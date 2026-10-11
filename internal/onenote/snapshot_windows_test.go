//go:build windows

package onenote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCaptureIndexWindowsLockFaultsReleaseNativeProtection(t *testing.T) {
	for _, fault := range []string{"pending-lock", "shared-lock", "pending-unlock", "shared-unlock"} {
		t.Run(fault, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
			if err != nil {
				t.Fatal(err)
			}
			lock, unlock := snapshotLockFile, snapshotUnlockFile
			t.Cleanup(func() { snapshotLockFile, snapshotUnlockFile = lock, unlock })
			failed := false
			snapshotLockFile = func(handle windows.Handle, flags, reserved, low, high uint32, overlap *windows.Overlapped) error {
				if !failed && ((fault == "pending-lock" && overlap.Offset == pendingByte) ||
					(fault == "shared-lock" && overlap.Offset == sharedFirst)) {
					failed = true
					return errors.New("lock fault")
				}
				return lock(handle, flags, reserved, low, high, overlap)
			}
			snapshotUnlockFile = func(handle windows.Handle, reserved, low, high uint32, overlap *windows.Overlapped) error {
				if !failed && ((fault == "pending-unlock" && overlap.Offset == pendingByte) ||
					(fault == "shared-unlock" && overlap.Offset == sharedFirst)) {
					failed = true
					return errors.New("unlock fault")
				}
				return unlock(handle, reserved, low, high, overlap)
			}
			capture, err := CaptureIndex(context.Background(), path)
			if fault == "shared-unlock" {
				if err != nil {
					t.Fatal(err)
				}
				if err := capture.Close(); err == nil {
					t.Fatal("unlock failure hidden")
				}
			} else if err == nil || capture != nil {
				t.Fatal("lock failure admitted")
			}
			snapshotLockFile, snapshotUnlockFile = lock, unlock
			if output := sqliteWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
				t.Fatalf("native protection leaked: %s", output)
			}
		})
	}
}

type unsupportedSnapshotInfo struct{ os.FileInfo }

func (unsupportedSnapshotInfo) Sys() any { return nil }

func TestSnapshotWindowsIdentityFailures(t *testing.T) {
	path := indexFixture(t, "index.db")
	file, err := os.Open(path) //nolint:gosec // opens only the synthetic fixture created immediately above
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := snapshotSingleLink(file); err == nil {
		t.Fatal("closed descriptor accepted")
	}
	if identity := snapshotIdentity(unsupportedSnapshotInfo{info}); identity != "" {
		t.Fatal("unsupported identity fabricated")
	}
}

func TestCaptureIndexWindowsHardLinkRefused(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path+".alias"); err != nil {
		t.Fatal(err)
	}
	if capture, err := CaptureIndex(context.Background(), path); capture != nil || err == nil {
		t.Fatal("multiply linked Windows source admitted")
	}
}
