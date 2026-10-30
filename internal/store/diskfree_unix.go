//go:build !windows

package store

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

// FreeBytes is the space an unprivileged writer can still use on the archive's volume.
func FreeBytes(dbPath string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Dir(dbPath), &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil //nolint:gosec,unconvert // G115: volume sizes fit int64; field types differ by platform.
}
