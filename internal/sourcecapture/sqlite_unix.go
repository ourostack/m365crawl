//go:build darwin || linux

package sourcecapture

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var fcntlLock = unix.FcntlFlock

// LockSQLite takes SQLite-compatible SHARED protection on file's own descriptor. The caller must
// hold the source gate and must not open or close another descriptor for the same inode until
// the returned unlock runs. A failed acquisition releases only the locks it took.
func LockSQLite(file *os.File) (unlock func() error, err error) {
	lock := func(kind int16, start, size int64) error {
		return fcntlLock(file.Fd(), unix.F_SETLK,
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
