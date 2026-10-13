//go:build windows

package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"golang.org/x/sys/windows"
)

func runSourceLockHolder(t *testing.T, path string) {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// A writable SQLite connection conflicts with the guard's FILE_SHARE_READ before any
	// byte-range lock is tested. A read-only handle isolates SQLite-compatible lock contention.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}()
	// SQLite's pending byte, reserved byte and 510 shared bytes form this 512-byte region.
	lock := &windows.Overlapped{Offset: 0x40000000}
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 512, 0, lock); err != nil {
		t.Fatal(err)
	}
	fmt.Println("LOCKED")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := windows.UnlockFileEx(handle, 0, 512, 0, lock); err != nil {
		t.Fatal(err)
	}
}

func TestFileSourceObservationRefusesWritableWindowsHandle(t *testing.T) {
	path := usageFileFixture(t, "usage-writable.db")
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error(err)
		}
	}()
	signature, err := officecapture.ObserveOneDriveUsage(context.Background(), path)
	var refusal *officecapture.ReadError
	if signature != "" || !errors.As(err, &refusal) || refusal.Code != "onedrive_usage_input_unqualified" {
		t.Fatalf("a writable handle was admitted by metadata observation: %q %v", signature, err)
	}
}
