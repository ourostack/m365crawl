//go:build windows

package browser

import (
	"errors"
	"runtime"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	completionMaxTargets = 4096
	completionMaxScan    = 65536
	completionMaxArgs    = 65536
)

var (
	completionOpenProcess      = windows.OpenProcess
	completionCloseHandle      = windows.CloseHandle
	completionWaitProcess      = windows.WaitForSingleObject
	completionTerminateProcess = windows.TerminateProcess
	completionQueryJob         = func(job windows.Handle, class int32, ptr unsafe.Pointer, size uint32, returned *uint32) error {
		return windows.QueryInformationJobObject(job, class, uintptr(ptr), size, returned)
	}
	completionProcessStart = completionCreationTime
	completionMembership   = completionInJob
	completionReadArgs     = readProcArgsHandle
	completionIsInJobProc  = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")
)

type completionJobPIDList struct {
	assigned, returned uint32
	pids               [completionMaxTargets]uintptr
}

func jobPIDs(job windows.Handle, limit int) ([]uint32, error) {
	if limit <= 0 || limit > completionMaxTargets {
		return nil, &completionFailure{code: "browser_completion_too_large"}
	}
	var info completionJobPIDList
	err := completionQueryJob(job, 3, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil) //nolint:gosec // G103: fixed native PID-list layout and exact buffer size
	if err != nil {
		return nil, &completionFailure{code: "browser_completion_job_query_failed", cause: err}
	}
	if info.assigned != info.returned || info.returned > uint32(limit) { //nolint:gosec // limit checked against fixed 4096 cap above
		return nil, &completionFailure{code: "browser_completion_too_large"}
	}
	pids := make([]uint32, info.returned)
	for i := range pids {
		if info.pids[i] == 0 || info.pids[i] > 0xffffffff {
			return nil, &completionFailure{code: "browser_completion_identity_failed"}
		}
		pids[i] = uint32(info.pids[i]) //nolint:gosec // native PID bounded to uint32 immediately above
	}
	return pids, nil
}

func completionCreationTime(h windows.Handle) (int64, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, err
	}
	if created.LowDateTime == 0 && created.HighDateTime == 0 {
		return 0, errors.New("browser_completion_identity_failed")
	}
	return created.Nanoseconds(), nil
}

func completionInJob(h, job windows.Handle) (bool, error) {
	var result int32
	r, _, err := completionIsInJobProc.Call(uintptr(h), uintptr(job), uintptr(unsafe.Pointer(&result))) //nolint:gosec // G103: native BOOL output lives through the synchronous call
	if r == 0 {
		return false, err
	}
	return result != 0, nil
}

func pollCompletionHandle(h windows.Handle) (bool, error) {
	status, err := completionWaitProcess(h, 0)
	if err != nil {
		return false, &completionFailure{code: "browser_completion_wait_failed", cause: err}
	}
	switch status {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, &completionFailure{code: "browser_completion_wait_failed"}
	}
}

func readProcArgsHandle(h windows.Handle, maxBytes uint32) ([]string, error) {
	var n uint32
	_ = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, nil, 0, &n)
	headerSize := uint32(unsafe.Sizeof(windows.NTUnicodeString{}))
	if n < headerSize || n > maxBytes || maxBytes > completionMaxArgs {
		return nil, &completionFailure{code: "browser_completion_args_unqualified"}
	}
	buf := make([]uint64, (n+7)/8)
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buf[0]), uint32(len(buf)*8), &n); err != nil { //nolint:gosec // allocation capped to 65536 bytes above
		return nil, &completionFailure{code: "browser_completion_args_query_failed", cause: err}
	}
	if n < headerSize || n > maxBytes {
		return nil, &completionFailure{code: "browser_completion_args_unqualified"}
	}
	header := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])) //nolint:gosec // G103: aligned buffer contains the validated full native header
	start := uintptr(unsafe.Pointer(&buf[0]))
	ptr := uintptr(unsafe.Pointer(header.Buffer))
	capacity := uint32(len(buf) * 8) //nolint:gosec // allocation capped to 65536 bytes above
	if ptr < start || ptr > start+uintptr(capacity) {
		return nil, &completionFailure{code: "browser_completion_args_unqualified"}
	}
	offset := uint32(ptr - start) //nolint:gosec // pointer offset bounded to the actual 65536-byte allocation above
	if header.Length%2 != 0 || header.Length > header.MaximumLength || !completionBufferFits(capacity, n, headerSize, offset, uint32(header.Length)) {
		return nil, &completionFailure{code: "browser_completion_args_unqualified"}
	}
	line := string(utf16.Decode(unsafe.Slice(header.Buffer, int(header.Length)/2)))
	runtime.KeepAlive(buf)
	argv, err := windows.DecomposeCommandLine(line)
	if err != nil {
		return nil, &completionFailure{code: "browser_completion_args_unqualified", cause: err}
	}
	return argv, nil
}

func terminateCompletionTarget(held windows.Handle, pid uint32, started int64) (err error) {
	done, err := pollCompletionHandle(held)
	if err != nil || done {
		return err
	}
	h, err := completionOpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		if done, waitErr := pollCompletionHandle(held); waitErr == nil && done {
			return nil
		}
		return &completionFailure{code: "browser_completion_terminate_open_failed", cause: err}
	}
	defer func() {
		if closeErr := completionCloseHandle(h); closeErr != nil && err == nil {
			err = &completionFailure{code: "browser_completion_release_failed", cause: closeErr}
		}
	}()
	actual, err := completionProcessStart(h)
	if err != nil {
		return &completionFailure{code: "browser_completion_identity_failed", cause: err}
	}
	if actual != started || actual == 0 {
		return &completionFailure{code: "browser_completion_identity_failed"}
	}
	if done, err := pollCompletionHandle(held); err != nil || done {
		return err
	}
	if err := completionTerminateProcess(h, 1); err != nil {
		if done, waitErr := pollCompletionHandle(held); waitErr == nil && done {
			return nil
		}
		return &completionFailure{code: "browser_completion_terminate_failed", cause: err}
	}
	return nil
}

func openCompletionTarget(pid uint32, job windows.Handle, profile string, knownOwned bool) (target *completionTarget, err error) {
	h, err := completionOpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if knownOwned {
			return nil, &completionFailure{code: "browser_completion_process_open_failed", cause: err}
		}
		return nil, nil // unclassifiable host entry, not certified unowned
	}
	defer func() {
		if target == nil {
			if closeErr := completionCloseHandle(h); closeErr != nil && err == nil {
				err = &completionFailure{code: "browser_completion_release_failed", cause: closeErr}
			}
		}
	}()
	started, startErr := completionProcessStart(h)
	var inJob bool
	var jobErr error
	if job != 0 {
		inJob, jobErr = completionMembership(h, job)
	}
	if !inJob || jobErr != nil {
		argv, argsErr := completionReadArgs(h, completionMaxArgs)
		if argsErr != nil || !ownedBy(argv, profile) {
			if knownOwned {
				return nil, &completionFailure{code: "browser_completion_identity_failed", cause: errors.Join(startErr, jobErr, argsErr)}
			}
			return nil, nil
		}
	}
	if startErr != nil || started == 0 || jobErr != nil {
		return nil, &completionFailure{code: "browser_completion_identity_failed", cause: errors.Join(startErr, jobErr)}
	}
	target = &completionTarget{
		identity: completionIdentity{pid: pid, started: started}, inJob: inJob,
		poll:      func() (bool, error) { return pollCompletionHandle(h) },
		terminate: func() error { return terminateCompletionTarget(h, pid, started) },
		release:   func() error { return completionCloseHandle(h) },
	}
	return target, nil
}
