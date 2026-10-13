//go:build windows

package sourcecapture

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var (
	lockFile   = windows.LockFileEx
	unlockFile = windows.UnlockFileEx
)

// LockSQLite takes SQLite-compatible SHARED protection on file's own handle. A failed acquisition
// releases only the ranges it took.
func LockSQLite(file *os.File) (unlock func() error, err error) {
	handle := windows.Handle(file.Fd())
	pending := &windows.Overlapped{Offset: pendingByte}
	shared := &windows.Overlapped{Offset: sharedFirst}
	unlockPending := func() error { return unlockFile(handle, 0, 1, 0, pending) }
	unlockShared := func() error { return unlockFile(handle, 0, sharedBytes, 0, shared) }
	if err := lockFile(handle, windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, pending); err != nil {
		return nil, err
	}
	if err := lockFile(handle, windows.LOCKFILE_FAIL_IMMEDIATELY, 0, sharedBytes, 0, shared); err != nil {
		return nil, errors.Join(err, unlockPending())
	}
	if err := unlockPending(); err != nil {
		return nil, errors.Join(err, unlockPending(), unlockShared())
	}
	return unlockShared, nil
}
