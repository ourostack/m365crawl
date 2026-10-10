package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errTimeout = errors.New("acceptance_timeout")

type workspace struct {
	dir string
	id  [3]uint32
}

func newWorkspace(parent string) (*workspace, error) {
	if !filepath.IsAbs(parent) || safeAncestors(parent) != nil || outsideGit(parent) != nil {
		return nil, errors.New("workspace_parent_refused")
	}
	dir, err := os.MkdirTemp(parent, "owa-acceptance-")
	if err != nil {
		return nil, errors.New("workspace_create_failed")
	}
	w := &workspace{dir: dir}
	if err := makePrivate(dir); err != nil {
		if removeErr := os.Remove(dir); removeErr != nil {
			return nil, errors.New("workspace_acl_and_cleanup_failed")
		}
		return nil, err
	}
	w.id, err = directoryIdentity(dir)
	if err != nil {
		if removeErr := os.Remove(dir); removeErr != nil {
			return nil, errors.New("workspace_identity_and_cleanup_failed")
		}
		return nil, errors.New("workspace_identity_failed")
	}
	return w, nil
}

func (w *workspace) run(binary string, args, env []string, timeout time.Duration) (exit int, result error) {
	exit = -1
	consumersReleased := true
	defer func() {
		if !consumersReleased {
			result = errors.Join(result, errors.New("cleanup_deferred_consumers_unverified"))
			return
		}
		if err := w.cleanup(); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if timeout <= 0 || !filepath.IsAbs(binary) {
		return exit, errors.New("launch_arguments_refused")
	}
	if err := w.check(); err != nil {
		return exit, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return exit, errors.New("job_create_failed")
	}
	defer func() {
		if job != 0 {
			if err := windows.CloseHandle(job); err != nil {
				result = errors.Join(result, errors.New("job_close_failed"))
			}
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// #nosec G103 -- Native Job Object API requires this SDK-defined structure pointer.
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return exit, errors.New("job_limits_failed")
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return exit, errors.New("null_io_failed")
	}
	defer func() {
		if err := null.Close(); err != nil {
			result = errors.Join(result, errors.New("null_io_close_failed"))
		}
	}()
	// #nosec G204 -- The local operator explicitly supplies the absolute precompiled test binary; no shell.
	cmd := exec.Command(binary, args...)
	cmd.Env = workspaceEnv(env, w.dir)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		return exit, errors.New("test_start_failed")
	}
	consumersReleased = false
	if cmd.Process.Pid <= 0 || int64(cmd.Process.Pid) > 0xffffffff {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		consumersReleased = cmd.ProcessState != nil && cmd.ProcessState.Exited()
		return exit, errors.New("leader_pid_refused")
	}
	pid := uint32(cmd.Process.Pid)
	// The leader is suspended: no cache read or descendant creation can precede job ownership.
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		consumersReleased = cmd.ProcessState != nil && cmd.ProcessState.Exited()
		return exit, errors.New("leader_handle_failed")
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			result = errors.Join(result, errors.New("leader_handle_close_failed"))
		}
	}()
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		consumersReleased = cmd.ProcessState != nil && cmd.ProcessState.Exited()
		return exit, errors.New("job_assignment_failed")
	}
	if err := resumeLeader(pid); err != nil {
		stopErr := stopJob(job)
		_ = cmd.Wait()
		if stopErr != nil {
			return exit, stopErr
		}
		consumersReleased = true
		return exit, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			result = errors.New("test_wait_failed")
		}
		exit = cmd.ProcessState.ExitCode()
	case <-timer.C:
		result = errTimeout
	}
	if err := stopJob(job); err != nil {
		return exit, errors.Join(result, err)
	}
	if errors.Is(result, errTimeout) {
		<-done
		exit = cmd.ProcessState.ExitCode()
	}
	consumersReleased = true
	return exit, result
}

func (w *workspace) cleanup() error {
	inventory, err := w.inventory()
	if err != nil {
		return err
	}
	return inventory.remove()
}

type disposal struct {
	root      string
	objects   []windows.Handle
	ancestors []windows.Handle
}

func (w *workspace) inventory() (inventory *disposal, result error) {
	if err := w.check(); err != nil {
		return nil, err
	}
	inventory = &disposal{root: w.dir}
	defer func() {
		if result != nil {
			result = errors.Join(result, inventory.close())
			inventory = nil
		}
	}()
	for path := filepath.Dir(w.dir); ; path = filepath.Dir(path) {
		handle, _, err := lockObject(path, false)
		if err != nil {
			return inventory, errors.New("cleanup_ancestor_lock_failed")
		}
		inventory.ancestors = append(inventory.ancestors, handle)
		if filepath.Dir(path) == path {
			break
		}
	}
	handle, info, err := lockObject(w.dir, true)
	if err != nil {
		return inventory, errors.New("cleanup_root_lock_failed")
	}
	inventory.objects = append(inventory.objects, handle)
	if [3]uint32{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow} != w.id || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return inventory, errors.New("cleanup_root_identity_refused")
	}
	if err := w.check(); err != nil {
		return inventory, err
	}
	err = inventory.scan(w.dir, 0)
	return inventory, err
}

func (inventory *disposal) scan(path string, depth int) (result error) {
	if depth > 128 || len(inventory.objects) >= 131072 {
		return errors.New("cleanup_inventory_budget_refused")
	}
	pattern, err := windows.UTF16PtrFromString(filepath.Join(path, "*"))
	if err != nil {
		return errors.New("cleanup_inventory_path_refused")
	}
	var data windows.Win32finddata
	search, err := windows.FindFirstFile(pattern, &data)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return errors.New("cleanup_inventory_open_failed")
	}
	defer func() {
		if err := windows.FindClose(search); err != nil {
			result = errors.Join(result, errors.New("cleanup_inventory_close_failed"))
		}
	}()
	for {
		name := windows.UTF16ToString(data.FileName[:])
		if name != "." && name != ".." {
			if name == "" || strings.ContainsAny(name, `\/:`) || len(inventory.objects) >= 131072 {
				return errors.New("cleanup_inventory_entry_refused")
			}
			child := filepath.Join(path, name)
			handle, info, err := lockObject(child, true)
			if err != nil {
				return errors.New("cleanup_object_lock_failed")
			}
			inventory.objects = append(inventory.objects, handle)
			if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
				if err := inventory.scan(child, depth+1); err != nil {
					return err
				}
			}
		}
		err := windows.FindNextFile(search, &data)
		if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
			return nil
		}
		if err != nil {
			return errors.New("cleanup_inventory_next_failed")
		}
	}
}

func lockObject(path string, remove bool) (windows.Handle, windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, info, errors.New("cleanup_object_path_refused")
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	if remove {
		access |= windows.DELETE
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, info, errors.New("cleanup_object_open_failed")
	}
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			return 0, info, errors.New("cleanup_refused_object_close_failed")
		}
		return 0, info, errors.New("cleanup_object_identity_refused")
	}
	return handle, info, nil
}

func (inventory *disposal) close() error {
	var result error
	for i := len(inventory.objects) - 1; i >= 0; i-- {
		if inventory.objects[i] == 0 {
			continue
		}
		if err := windows.CloseHandle(inventory.objects[i]); err != nil {
			result = errors.Join(result, errors.New("cleanup_object_close_failed"))
		} else {
			inventory.objects[i] = 0
		}
	}
	for i := len(inventory.ancestors) - 1; i >= 0; i-- {
		if inventory.ancestors[i] == 0 {
			continue
		}
		if err := windows.CloseHandle(inventory.ancestors[i]); err != nil {
			result = errors.Join(result, errors.New("cleanup_ancestor_close_failed"))
		} else {
			inventory.ancestors[i] = 0
		}
	}
	return result
}

func (inventory *disposal) remove() (result error) {
	defer func() { result = errors.Join(result, inventory.close()) }()
	for i := len(inventory.objects) - 1; i >= 0; i-- {
		deleted := byte(1) // FILE_DISPOSITION_INFO.DeleteFile is a one-byte Windows BOOLEAN.
		if err := windows.SetFileInformationByHandle(inventory.objects[i], windows.FileDispositionInfo, &deleted, 1); err != nil {
			return errors.New("cleanup_handle_delete_failed")
		}
		if err := windows.CloseHandle(inventory.objects[i]); err != nil {
			return errors.New("cleanup_deleted_handle_close_failed")
		}
		inventory.objects[i] = 0
	}
	if _, err := os.Lstat(inventory.root); !errors.Is(err, os.ErrNotExist) {
		return errors.New("cleanup_verify_failed")
	}
	return nil
}

func (w *workspace) check() error {
	if w.id == [3]uint32{} || !filepath.IsAbs(w.dir) || !strings.HasPrefix(filepath.Base(w.dir), "owa-acceptance-") {
		return errors.New("workspace_ownership_refused")
	}
	info, err := os.Lstat(w.dir)
	id, idErr := directoryIdentity(w.dir)
	if err != nil || idErr != nil || w.id != id || !info.IsDir() || safeAncestors(w.dir) != nil {
		return errors.New("workspace_identity_refused")
	}
	sd, err := windows.GetNamedSecurityInfo(w.dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || !privateACL(sd) {
		return errors.New("workspace_acl_refused")
	}
	return nil
}

func directoryIdentity(path string) (id [3]uint32, result error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return id, err
	}
	h, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return id, err
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			result = errors.Join(result, errors.New("identity_handle_close_failed"))
		}
	}()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return id, err
	}
	return [3]uint32{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, nil
}

func makePrivate(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return errors.New("workspace_user_failed")
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return errors.New("workspace_acl_create_failed")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return errors.New("workspace_acl_create_failed")
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return errors.New("workspace_acl_set_failed")
	}
	return nil
}

func privateACL(sd *windows.SECURITY_DESCRIPTOR) bool {
	if sd == nil {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return false
	}
	seen := map[string]bool{}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		const fileAllAccess windows.ACCESS_MASK = 0x001f01ff
		if err := windows.GetAce(acl, i, &ace); err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != fileAllAccess || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
			return false
		}
		// #nosec G103 -- GetAce returns the SDK's variable-length ACE; SidStart is its inline SID.
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if sid != user.User.Sid.String() && sid != "S-1-5-18" {
			return false
		}
		seen[sid] = true
	}
	return seen[user.User.Sid.String()] && seen["S-1-5-18"]
}

func safePath(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attr, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attr&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("reparse_path_refused")
	}
	return nil
}

func safeAncestors(path string) error {
	for {
		if err := safePath(path); err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func outsideGit(path string) error {
	for {
		_, err := os.Lstat(filepath.Join(path, ".git"))
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("git_workspace_refused")
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func workspaceEnv(env []string, root string) []string {
	keys := []string{"TEMP", "TMP", "TMPDIR", "HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "GOWORK"}
	var out []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		replaced := false
		for _, key := range keys {
			replaced = replaced || strings.EqualFold(name, key)
		}
		if !replaced {
			out = append(out, entry)
		}
	}
	for _, key := range keys[:5] {
		out = append(out, key+"="+root)
	}
	return append(out, "LOCALAPPDATA="+filepath.Join(root, "AppData", "Local"), "APPDATA="+filepath.Join(root, "AppData", "Roaming"), "GOWORK=off")
}

func resumeLeader(pid uint32) (result error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return errors.New("leader_thread_snapshot_failed")
	}
	defer func() {
		if err := windows.CloseHandle(snapshot); err != nil {
			result = errors.Join(result, errors.New("thread_snapshot_close_failed"))
		}
	}()
	entry := windows.ThreadEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != pid {
			continue
		}
		thread, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if err != nil {
			return errors.New("leader_thread_open_failed")
		}
		previous, resumeErr := windows.ResumeThread(thread)
		closeErr := windows.CloseHandle(thread)
		if resumeErr != nil || closeErr != nil || previous != 1 {
			return errors.New("leader_resume_failed")
		}
		return nil
	}
	return errors.New("leader_thread_missing")
}

// Layout specified by JOBOBJECT_BASIC_ACCOUNTING_INFORMATION in winnt.h.
type jobAccounting struct {
	totalUser, totalKernel, periodUser, periodKernel int64
	pageFaults, total, active, terminated            uint32
}

func waitEmpty(job windows.Handle) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		var accounting jobAccounting
		// #nosec G103 -- Fixed winnt.h accounting layout; buffer length is checked by the native API.
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return errors.New("job_consumers_unverified")
		}
		if accounting.active == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("job_consumers_remaining")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stopJob(job windows.Handle) (result error) {
	if err := sealJob(job); err != nil {
		return err
	}
	type processList struct {
		assigned, listed uint32
		pids             [128]uintptr
	}
	var list processList
	// #nosec G103 -- Fixed SDK process-list header plus bounded ULONG_PTR array.
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&list)), uint32(unsafe.Sizeof(list)), nil); err != nil || list.assigned != list.listed || list.listed > uint32(len(list.pids)) {
		return errors.New("job_process_inventory_failed")
	}
	var handles []windows.Handle
	defer func() {
		for _, handle := range handles {
			if err := windows.CloseHandle(handle); err != nil {
				result = errors.Join(result, errors.New("job_process_handle_close_failed"))
			}
		}
	}()
	for _, pid := range list.pids[:list.listed] {
		if pid == 0 || pid > 0xffffffff {
			return errors.New("job_process_identity_refused")
		}
		handle, err := memberHandle(job, uint32(pid))
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			continue
		}
		if err != nil {
			return errors.New("job_process_handle_failed")
		}
		handles = append(handles, handle)
	}
	if err := windows.TerminateJobObject(job, 1); err != nil {
		return errors.New("job_termination_failed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, handle := range handles {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.New("job_process_wait_timeout")
		}
		millis := remaining.Milliseconds() + 1
		if millis < 1 || millis > 5001 {
			return errors.New("job_wait_budget_refused")
		}
		state, err := windows.WaitForSingleObject(handle, uint32(millis))
		if err != nil || state != windows.WAIT_OBJECT_0 {
			return errors.New("job_process_wait_failed")
		}
	}
	return waitEmpty(job)
}

func sealJob(job windows.Handle) error {
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
	limits.BasicLimitInformation.ActiveProcessLimit = 1
	// #nosec G103 -- SDK structure pointer; one active member leaves no slot for a new descendant.
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return errors.New("job_birth_barrier_failed")
	}
	var actual windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	// #nosec G103 -- SDK-defined buffer and exact native structure size.
	if err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&actual)), uint32(unsafe.Sizeof(actual)), nil); err != nil || actual.BasicLimitInformation.LimitFlags&limits.BasicLimitInformation.LimitFlags != limits.BasicLimitInformation.LimitFlags || actual.BasicLimitInformation.ActiveProcessLimit != 1 {
		return errors.New("job_birth_barrier_unverified")
	}
	return nil
}

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func memberHandle(job windows.Handle, pid uint32) (windows.Handle, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	var member uint32
	// #nosec G103 -- IsProcessInJob takes an SDK BOOL output pointer; verify the opened identity, not its PID label.
	ok, _, _ := isProcessInJob.Call(uintptr(handle), uintptr(job), uintptr(unsafe.Pointer(&member)))
	if ok == 0 || member == 0 {
		if err := windows.CloseHandle(handle); err != nil {
			return 0, errors.New("job_nonmember_handle_close_failed")
		}
		return 0, errors.New("job_member_identity_refused")
	}
	return handle, nil
}

func main() {
	binary := flag.String("test-binary", "", "Absolute precompiled acceptance test binary")
	source := flag.String("outlook-root", "", "Explicit measured EBWebView source")
	parent := flag.String("scratch-parent", "", "Existing private validation directory outside Git")
	flag.Parse()
	if !filepath.IsAbs(*binary) || !filepath.IsAbs(*source) || !filepath.IsAbs(*parent) || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "owa_acceptance status=refused reason=arguments")
		os.Exit(2)
	}
	w, err := newWorkspace(*parent)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owa_acceptance status=refused reason=workspace")
		os.Exit(2)
	}
	env := append(os.Environ(), "M365CRAWL_REAL_CACHE=1", "M365CRAWL_OUTLOOK_ROOT="+*source, "M365CRAWL_TEAMS_ROOT="+filepath.Join(w.dir, "no-teams"))
	exit, err := w.run(*binary, []string{"-test.run=^TestRealOWAMailCountsAndRelationships$", "-test.timeout=4m"}, env, 5*time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "owa_acceptance status=failed reason=lifecycle_or_cleanup")
		os.Exit(1)
	}
	if exit != 0 {
		fmt.Fprintln(os.Stderr, "owa_acceptance status=failed reason=test cleanup=verified")
		os.Exit(1)
	}
	fmt.Println("owa_acceptance status=completed qualification=unproved cleanup=verified")
}
