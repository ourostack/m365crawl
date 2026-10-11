//go:build windows

package onenote

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/windows"
)

var (
	snapshotLockFile   = windows.LockFileEx
	snapshotUnlockFile = windows.UnlockFileEx
)

func lockIndex(file *os.File) (func() error, error) {
	handle := windows.Handle(file.Fd())
	pending := &windows.Overlapped{Offset: pendingByte}
	shared := &windows.Overlapped{Offset: sharedFirst}
	unlockPending := func() error { return snapshotUnlockFile(handle, 0, 1, 0, pending) }
	unlockShared := func() error { return snapshotUnlockFile(handle, 0, sharedBytes, 0, shared) }
	if err := snapshotLockFile(handle, windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, pending); err != nil {
		return nil, err
	}
	if err := snapshotLockFile(handle, windows.LOCKFILE_FAIL_IMMEDIATELY, 0, sharedBytes, 0, shared); err != nil {
		return nil, errors.Join(err, unlockPending())
	}
	if err := unlockPending(); err != nil {
		return nil, errors.Join(err, unlockPending(), unlockShared())
	}
	return unlockShared, nil
}

func snapshotSingleLink(file *os.File) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("unsafe index links")
	}
	return nil
}

func snapshotIdentity(info os.FileInfo) string {
	native, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", native.CreationTime.HighDateTime, native.CreationTime.LowDateTime)
}
