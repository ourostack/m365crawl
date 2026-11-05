//go:build windows

package snapshotowner

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ourostack/m365crawl/internal/fileprivacy"
)

var (
	ownerOpenProcess  = windows.OpenProcess
	ownerExitCode     = windows.GetExitCodeProcess
	ownerCloseHandle  = windows.CloseHandle
	ownerGetSecurity  = windows.GetNamedSecurityInfo
	ownerWellKnownSID = windows.CreateWellKnownSid
	ownerSetSecurity  = windows.SetNamedSecurityInfo
	ownerTokenUser    = func() (*windows.Tokenuser, error) {
		return windows.GetCurrentProcessToken().GetTokenUser()
	}
)

// Called only for the marker just created exclusively within the verified root.
func platformOwnMarker(f *os.File) error {
	user, err := ownerTokenUser()
	if err != nil {
		return err
	}
	err = ownerSetSecurity(f.Name(), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, user.User.Sid, nil, nil, nil)
	runtime.KeepAlive(user)
	return err
}

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
	sd, err := ownerGetSecurity(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil || !sd.IsValid() {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := ownerTokenUser()
	if err != nil || !owner.Equals(user.User.Sid) {
		return false
	}
	defer func() {
		runtime.KeepAlive(sd)
		runtime.KeepAlive(user)
	}()
	system, err := ownerWellKnownSID(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_SELF_RELATIVE == 0 ||
		(info.IsDir() && control&windows.SE_DACL_PROTECTED == 0) {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false
	}
	// GetNamedSecurityInfo returns a Go-owned self-relative descriptor. Bound the ACL to
	// that allocation before reading its declared size or any variable-length SID.
	return ownerACLInAllocation(unsafe.Pointer(sd), uint64(sd.Length()), uintptr(unsafe.Pointer(acl)), user.User.Sid, system, info.IsDir())
}

// base retains at least size readable bytes; address is checked as a value before slicing.
func ownerACLInAllocation(base unsafe.Pointer, size uint64, address uintptr, user, system *windows.SID, directory bool) bool {
	return fileprivacy.ValidateACLInAllocation(base, size, address, user, system, directory, false)
}

// Windows can split a generic inheritable grant into effective and inherit-only ACEs.
// Validate their meaning, not a particular entry count or principal ordering.
func ownerPrivateACL(data []byte, user, system *windows.SID, directory bool) bool {
	return fileprivacy.ValidateACL(data, user, system, directory, false)
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
	return os.NewFile(uintptr(h), path), nil
}
