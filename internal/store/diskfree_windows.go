//go:build windows

package store

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// FreeBytes is the space the current user can still use on the archive's volume.
func FreeBytes(dbPath string) (int64, error) {
	dir, err := windows.UTF16PtrFromString(filepath.Dir(dbPath))
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(dir, &available, &total, &free); err != nil {
		return 0, err
	}
	return int64(available), nil //nolint:gosec // G115: volume sizes fit int64.
}
