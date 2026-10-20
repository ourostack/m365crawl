//go:build windows

package snapshotowner

import (
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
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
	origin := uintptr(base)
	if size < 8 || size > uint64(^uint(0)>>1) || address < origin || uint64(address-origin) > size-8 {
		return false
	}
	data := unsafe.Slice((*byte)(base), int(size))
	start := int(address - origin)
	length := int(binary.LittleEndian.Uint16(data[start+2 : start+4]))
	if length < 8 || length > len(data)-start {
		return false
	}
	return ownerPrivateACL(data[start:start+length], user, system, directory)
}

// Windows can split a generic inheritable grant into effective and inherit-only ACEs.
// Validate their meaning, not a particular entry count or principal ordering.
func ownerPrivateACL(data []byte, user, system *windows.SID, directory bool) bool {
	if len(data) < 8 || len(data)%4 != 0 || len(data) != int(binary.LittleEndian.Uint16(data[2:4])) ||
		(data[0] != 2 && data[0] != 4) || data[1] != 0 || binary.LittleEndian.Uint16(data[6:8]) != 0 ||
		user == nil || system == nil || !user.IsValid() || !system.IsValid() {
		return false
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 || count > (len(data)-8)/16 {
		return false
	}
	userAllowed, systemAllowed := false, false
	offset := 8
	for i := 0; i < count; i++ {
		if len(data)-offset < 16 {
			return false
		}
		length := int(binary.LittleEndian.Uint16(data[offset+2 : offset+4]))
		if length < 16 || length%4 != 0 || length > len(data)-offset {
			return false
		}
		ace := data[offset : offset+length]
		offset += length
		flags := ace[1]
		const inherit = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		if ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE ||
			flags & ^uint8(inherit|windows.INHERIT_ONLY_ACE|windows.INHERITED_ACE) != 0 ||
			(!directory && flags & ^uint8(windows.INHERITED_ACE) != 0) ||
			(flags&windows.INHERIT_ONLY_ACE != 0 && flags&inherit == 0) {
			return false
		}
		// GENERIC_ALL and its file/directory mapping grant precisely the creator's rights.
		const fileAllAccess = 0x1f01ff
		mask := binary.LittleEndian.Uint32(ace[4:8])
		if mask != windows.GENERIC_ALL && mask != fileAllAccess {
			return false
		}
		if ace[9] > 15 || len(ace)-8 != 8+4*int(ace[9]) {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace[8]))
		if ace[8] != 1 || !sid.IsValid() {
			return false
		}
		isUser, isSystem := sid.Equals(user), sid.Equals(system)
		if !isUser && !isSystem {
			return false
		}
		if flags&windows.INHERIT_ONLY_ACE == 0 {
			userAllowed = userAllowed || isUser
			systemAllowed = systemAllowed || isSystem
		}
	}
	for _, spare := range data[offset:] {
		if spare != 0 {
			return false
		}
	}
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
	return os.NewFile(uintptr(h), path), nil
}
