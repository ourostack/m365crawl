//go:build darwin || linux

package onenote

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

var snapshotFcntl = unix.FcntlFlock

func lockIndex(file *os.File) (func() error, error) {
	lock := func(kind int16, start, size int64) error {
		return snapshotFcntl(file.Fd(), unix.F_SETLK,
			&unix.Flock_t{Type: kind, Whence: int16(0), Start: start, Len: size})
	}
	if err := lock(unix.F_RDLCK, pendingByte, 1); err != nil {
		return nil, err
	}
	if err := lock(unix.F_RDLCK, sharedFirst, sharedBytes); err != nil {
		return nil, errors.Join(err, lock(unix.F_UNLCK, pendingByte, 1))
	}
	if err := lock(unix.F_UNLCK, pendingByte, 1); err != nil {
		return nil, errors.Join(err, lock(unix.F_UNLCK, pendingByte, 1),
			lock(unix.F_UNLCK, sharedFirst, sharedBytes))
	}
	return func() error { return lock(unix.F_UNLCK, sharedFirst, sharedBytes) }, nil
}

func snapshotSingleLink(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || native.Nlink != 1 {
		return errors.New("unsafe index links")
	}
	return nil
}

func snapshotIdentity(info os.FileInfo) string {
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", native.Dev, native.Ino)
}
