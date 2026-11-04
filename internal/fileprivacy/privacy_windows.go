//go:build windows

package fileprivacy

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	privateGetSecurity = windows.GetSecurityInfo
	privateTokenUser   = func() (*windows.Tokenuser, error) {
		return windows.GetCurrentProcessToken().GetTokenUser()
	}
)

func platformPrivate(f *os.File, directory bool) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() != directory || !directory && !info.Mode().IsRegular() {
		return privateError()
	}
	h := windows.Handle(f.Fd())
	var attributes windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &attributes); err != nil {
		return err
	}
	if attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return privateError()
	}
	sd, err := privateGetSecurity(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	if sd == nil || !sd.IsValid() {
		return privateError()
	}
	user, err := privateTokenUser()
	if err != nil {
		return err
	}
	if user == nil || user.User.Sid == nil {
		return privateError()
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return privateError()
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	defer func() {
		runtime.KeepAlive(sd)
		runtime.KeepAlive(user)
		runtime.KeepAlive(system)
	}()
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_SELF_RELATIVE == 0 ||
		directory && control&windows.SE_DACL_PROTECTED == 0 {
		return privateError()
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil ||
		!ValidateACLInAllocation(unsafe.Pointer(sd), uint64(sd.Length()), uintptr(unsafe.Pointer(acl)),
			user.User.Sid, system, directory, directory) {
		return privateError()
	}
	return nil
}
