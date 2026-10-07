//go:build windows

package browser

import "golang.org/x/sys/windows"

// reclaim ends the leader a pid file names while it is still the same process (same creation
// time). The job object already ended its children when the earlier run died.
func reclaim(_ string, pid int, started int64) {
	if started == 0 || creationTime(uint32(pid)) != started {
		return
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h) //nolint:errcheck // nothing to do about a failed close
	_ = windows.TerminateProcess(h, 1)
}

// sweepArgv has nothing to do on Windows: the job object is the guarantee.
func sweepArgv(string, bool) error { return nil }
