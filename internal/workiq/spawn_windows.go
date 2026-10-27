//go:build windows

package workiq

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processGroup struct{ job windows.Handle }

func commandFor(binary string, args []string) (*exec.Cmd, error) {
	if !strings.EqualFold(filepath.Ext(binary), ".cmd") && !strings.EqualFold(filepath.Ext(binary), ".bat") {
		return exec.Command(binary, args...), nil //nolint:gosec // executable is explicit local configuration; argv fixed/validated
	}
	path, err := filepath.Abs(binary)
	if err != nil || strings.ContainsAny(path, "\"%!^&|<>\r\n\x00") {
		return nil, failure("workiq_binary_unsafe")
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, failure("workiq_binary_unsafe")
	}
	shell := filepath.Join(system, "cmd.exe")
	cmd := exec.Command(shell) //nolint:gosec // system directory, never COMSPEC or server data
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine: `"` + shell + `" /d /s /v:off /c ""` + path + `" ` + strings.Join(args, " ") + `"`,
	}
	return cmd, nil
}

func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP
}

// The child stays suspended until assigned to our kill-on-close job.
func adoptProcess(cmd *exec.Cmd) (*processGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	g := &processGroup{job: job}
	fail := func(err error) (*processGroup, error) {
		_ = g.end()
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return fail(err)
	}
	pid := uint32(cmd.Process.Pid)
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(job, handle)
	closeErr := windows.CloseHandle(handle)
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	if err := resumeChild(pid); err != nil {
		return fail(err)
	}
	return g, nil
}

func resumeChild(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(snap) }()
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	if err = windows.Thread32First(snap, &entry); err != nil {
		return err
	}
	for {
		if entry.OwnerProcessID == pid {
			thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if err != nil {
				return err
			}
			_, err = windows.ResumeThread(thread)
			closeErr := windows.CloseHandle(thread)
			if err != nil {
				return err
			}
			return closeErr
		}
		if err = windows.Thread32Next(snap, &entry); err != nil {
			return err
		}
	}
}

func (g *processGroup) end() error {
	if g.job == 0 {
		return nil
	}
	err := windows.TerminateJobObject(g.job, 1)
	closeErr := windows.CloseHandle(g.job)
	g.job = 0
	if err != nil || closeErr != nil {
		return failure("workiq_cleanup_failed")
	}
	return nil
}
