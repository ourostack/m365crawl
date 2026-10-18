//go:build !windows

package snapshotowner

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

var ownerKill = syscall.Kill

func platformProcessDead(pid int) (bool, error) {
	if pid <= 0 || pid > maxOwnerPID {
		return false, fs.ErrInvalid
	}
	if pid == ownerSelfPID() {
		return false, nil
	}
	err := ownerKill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return true, nil
	}
	return false, err
}

func platformProtected(_ string, info fs.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(st.Uid) == int64(os.Geteuid()) && info.Mode().Perm()&0o077 == 0
}

func platformOpenMarker(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: protected snapshot marker; read-only no-follow, bounded and identity-checked by caller
}
