//go:build windows

package browser

import (
	"errors"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// singletonLive reports whether a running browser holds the profile's "lockfile". Chrome and
// Edge keep it open without sharing for as long as they run.
func singletonLive(profile string) bool {
	name, err := windows.UTF16PtrFromString(filepath.Join(profile, "lockfile"))
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return errors.Is(err, windows.ERROR_SHARING_VIOLATION)
	}
	_ = windows.CloseHandle(h)
	return false
}
