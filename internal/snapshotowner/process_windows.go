//go:build windows

package snapshotowner

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ownerOpenProcess = windows.OpenProcess
	ownerExitCode    = windows.GetExitCodeProcess
	ownerCloseHandle = windows.CloseHandle
)

func platformProcessDead(pid int) (bool, error) {
	if pid <= 0 || pid > maxOwnerPID {
		return false, fs.ErrInvalid
	}
	if pid == ownerSelfPID() {
		return false, nil
	}
	h, err := ownerOpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Failure to obtain a handle is uncertainty, never positive absence.
		return false, err
	}
	if h == 0 || h == windows.InvalidHandle {
		return false, fs.ErrInvalid
	}
	var code uint32
	queryErr := ownerExitCode(h, &code)
	if err := errors.Join(queryErr, ownerCloseHandle(h)); err != nil {
		return false, err
	}
	const stillActive = 259
	return code != stillActive, nil
}

func platformProtected(path string, info fs.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !owner.Equals(user.User.Sid) {
		return false
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || (info.IsDir() && control&windows.SE_DACL_PROTECTED == 0) {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount != 2 {
		return false
	}
	userAllowed, systemAllowed := false, false
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil ||
			ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 ||
			uintptr(ace.Header.AceSize) < unsafe.Sizeof(*ace) {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		isUser, isSystem := sid.Equals(user.User.Sid), sid.Equals(system)
		if !isUser && !isSystem {
			return false
		}
		userAllowed = userAllowed || isUser
		systemAllowed = systemAllowed || isSystem
	}
	runtime.KeepAlive(sd)
	runtime.KeepAlive(user)
	return userAllowed && systemAllowed
}

func platformOpenMarker(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	if f == nil {
		return nil, errors.Join(fs.ErrInvalid, windows.CloseHandle(h))
	}
	return f, nil
}
