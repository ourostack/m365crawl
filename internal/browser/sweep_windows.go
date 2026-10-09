//go:build windows

package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procInfo is one process and its argument vector.
type procInfo struct {
	pid  int
	argv []string
}

// Test seams.
var (
	listProcs     = toolhelpList
	readArgs      = readProcArgs
	terminateProc = terminatePid
)

// reclaim ends the leader a pid file names while it is still the same process (same creation
// time). The job object already ended its children when the earlier run died; sweepArgv ends any
// process that runs with the profile outside the job.
func reclaim(_ string, pid int, started int64) {
	if started == 0 || creationTime(uint32(pid)) != started {
		return
	}
	terminatePid(uint32(pid))
}

func terminatePid(pid uint32) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	defer windows.CloseHandle(h) //nolint:errcheck // nothing to do about a failed close
	_ = windows.TerminateProcess(h, 1)
}

// toolhelpList lists every running process with its command line split into arguments.
// Processes whose command line cannot be read (exited meanwhile, protected, or another user's)
// are left out; they are not ours. A process with no threads left has exited and is skipped too.
func toolhelpList() ([]procInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snap) //nolint:errcheck // nothing to do about a failed close
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var procs []procInfo
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.Threads == 0 {
			continue
		}
		argv, aerr := readArgs(entry.ProcessID)
		if aerr != nil || len(argv) == 0 {
			continue
		}
		procs = append(procs, procInfo{pid: int(entry.ProcessID), argv: argv})
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, err
	}
	return procs, nil
}

// readProcArgs reads a process's command line (ProcessCommandLineInformation, which needs only
// limited query rights) and splits it the way the process itself would.
func readProcArgs(pid uint32) ([]string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h) //nolint:errcheck // nothing to do about a failed close
	var n uint32
	_ = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, nil, 0, &n)
	if n < uint32(unsafe.Sizeof(windows.NTUnicodeString{})) {
		return nil, errors.New("no command line")
	}
	buf := make([]uint64, (n+7)/8) // 8-byte aligned for the UNICODE_STRING header
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buf[0]), uint32(len(buf)*8), &n); err != nil {
		return nil, err
	}
	line := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String()
	runtime.KeepAlive(buf)
	return windows.DecomposeCommandLine(line)
}

// ownedBy reports whether one argument is the --user-data-dir flag of the profile whose longPath
// is want. Windows paths are compared without regard to case or short (8.3) names; a longer path
// that merely starts with the profile path does not count.
func ownedBy(argv []string, want string) bool {
	for _, a := range argv {
		if v, ok := strings.CutPrefix(a, "--user-data-dir="); ok && strings.EqualFold(longPath(v), want) {
			return true
		}
	}
	return false
}

// longPath is the cleaned path with short (8.3) names expanded, or the cleaned path when it
// cannot be expanded (for example, because it no longer exists).
func longPath(p string) string {
	p = filepath.Clean(p)
	u, err := windows.UTF16PtrFromString(p)
	if err != nil {
		return p
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	if _, err := windows.GetLongPathName(u, &buf[0], uint32(len(buf))); err != nil {
		return p
	}
	return windows.UTF16ToString(buf)
}

// profileProcs returns the processes started with this profile, other than this one.
func profileProcs(profile string) ([]procInfo, error) {
	all, err := listProcs()
	if err != nil {
		return nil, err
	}
	want := longPath(profile)
	var out []procInfo
	for _, p := range all {
		if p.pid != os.Getpid() && ownedBy(p.argv, want) {
			out = append(out, p)
		}
	}
	return out, nil
}

// sweepArgv ends every process started with this profile that is still running, wherever it
// runs: the job object ends the processes in it, and this ends any that a launcher handed off to
// outside it. Windows has no polite signal for another process, so polite changes nothing. It
// returns an error when a process survives.
func sweepArgv(profile string, _ bool) error {
	procs, err := profileProcs(profile)
	if err != nil {
		return err
	}
	if len(procs) == 0 {
		return nil
	}
	for _, p := range procs {
		terminateProc(uint32(p.pid))
	}
	waitUntil(killGrace, func() bool {
		left, err := profileProcs(profile)
		return err == nil && len(left) == 0
	})
	left, err := profileProcs(profile)
	if err != nil {
		return err
	}
	if len(left) > 0 {
		return fmt.Errorf("%d browser processes survived the kill", len(left))
	}
	return nil
}

// groupHasProfileProcs: the job object is private to this launch, so it is always safe to end.
func groupHasProfileProcs(string) bool { return true }
