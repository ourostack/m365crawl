//go:build windows

package browser

import (
	"testing"

	"golang.org/x/sys/windows"
)

func processExists(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h) //nolint:errcheck // test helper
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

// The job object is the guarantee on Windows; there is no command-line sweep to check.
func requireNoProfileProcs(t *testing.T, _ string) { t.Helper() }
