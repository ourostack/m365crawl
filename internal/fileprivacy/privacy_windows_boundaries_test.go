//go:build windows

package fileprivacy

import (
	"encoding/binary"
	"errors"
	"os"
	"runtime"
	"testing"
	"unsafe"

	"github.com/ourostack/m365crawl/internal/errs"
	"golang.org/x/sys/windows"
)

func TestPrivateImageWindowsUnknownHandleObservationsRefuse(t *testing.T) {
	for _, boundary := range []string{"parent-as-temporary", "attributes-error", "reparse", "security-error",
		"nil-descriptor", "invalid-descriptor", "token-error", "nil-user", "nil-user-sid", "system-error",
		"unprotected-descriptor", "null-dacl", "public-full-control"} {
		t.Run(boundary, func(t *testing.T) {
			dir := privateWindowsFixture(t, "OICI", "")
			parent, err := os.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := parent.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := Parent(parent); err != nil {
				t.Fatal("valid native private handle failed before boundary fault", err)
			}
			oldInfo, oldSID := privateGetFileInformation, privateWellKnownSID
			oldSecurity, oldUser := privateGetSecurity, privateTokenUser
			t.Cleanup(func() {
				privateGetFileInformation, privateWellKnownSID = oldInfo, oldSID
				privateGetSecurity, privateTokenUser = oldSecurity, oldUser
			})
			sentinel := errors.New("synthetic native policy query failure")
			var descriptor *windows.SECURITY_DESCRIPTOR
			switch boundary {
			case "attributes-error":
				privateGetFileInformation = func(windows.Handle, *windows.ByHandleFileInformation) error { return sentinel }
			case "reparse":
				privateGetFileInformation = func(handle windows.Handle, info *windows.ByHandleFileInformation) error {
					err := oldInfo(handle, info)
					info.FileAttributes |= windows.FILE_ATTRIBUTE_REPARSE_POINT
					return err
				}
			case "security-error":
				privateGetSecurity = func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
					return nil, sentinel
				}
			case "nil-descriptor", "invalid-descriptor":
				if boundary == "invalid-descriptor" {
					descriptor = &windows.SECURITY_DESCRIPTOR{}
				}
				privateGetSecurity = func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
					return descriptor, nil
				}
			case "token-error":
				privateTokenUser = func() (*windows.Tokenuser, error) { return nil, sentinel }
			case "nil-user":
				privateTokenUser = func() (*windows.Tokenuser, error) { return nil, nil }
			case "nil-user-sid":
				privateTokenUser = func() (*windows.Tokenuser, error) { return &windows.Tokenuser{}, nil }
			case "system-error":
				privateWellKnownSID = func(windows.WELL_KNOWN_SID_TYPE) (*windows.SID, error) { return nil, sentinel }
			case "unprotected-descriptor", "null-dacl", "public-full-control":
				user, err := oldUser()
				if err != nil {
					t.Fatal(err)
				}
				sddl := "O:" + user.User.Sid.String() + "D:(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)"
				switch boundary {
				case "null-dacl":
					sddl = "O:" + user.User.Sid.String() + "D:PNO_ACCESS_CONTROL"
				case "public-full-control":
					sddl = "O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;WD)"
				}
				descriptor, err = windows.SecurityDescriptorFromString(sddl)
				runtime.KeepAlive(user)
				if err != nil {
					t.Fatal("native security descriptor fixture failed", err)
				}
				privateGetSecurity = func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
					return descriptor, nil
				}
			}
			if boundary == "parent-as-temporary" {
				err = Temporary(parent)
			} else {
				err = Parent(parent)
			}
			runtime.KeepAlive(descriptor)
			switch boundary {
			case "attributes-error", "security-error", "token-error", "system-error":
				if !errors.Is(err, sentinel) {
					t.Fatal("native policy query failure lost its cause or admitted unknown protection", err)
				}
			default:
				var coded *errs.Coded
				if !errors.As(err, &coded) || coded.Code != "image_output_unprotected" {
					t.Fatal("unknown or unsafe exact-handle observation admitted", err)
				}
			}
		})
	}
}

func TestPrivateImageWindowsMalformedACLRefusals(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" +
		user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	header := unsafe.Slice((*byte)(unsafe.Pointer(acl)), 8)
	data := append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(acl)), int(binary.LittleEndian.Uint16(header[2:4])))...)
	defer func() {
		runtime.KeepAlive(descriptor)
		runtime.KeepAlive(user)
		runtime.KeepAlive(system)
	}()
	if !ValidateACL(data, user.User.Sid, system, true, true) {
		t.Fatal("native private ACL fixture failed before mutation")
	}
	for _, corruption := range []string{"header", "empty", "truncated-second-ace", "ace-size",
		"mask", "sid-count", "sid-revision", "nonzero-spare", "allocation-length"} {
		t.Run(corruption, func(t *testing.T) {
			changed := append([]byte(nil), data...)
			switch corruption {
			case "header":
				changed[0] = 0
			case "empty":
				binary.LittleEndian.PutUint16(changed[4:6], 0)
			case "truncated-second-ace":
				first := int(binary.LittleEndian.Uint16(changed[10:12]))
				changed = changed[:8+first+12]
				binary.LittleEndian.PutUint16(changed[2:4], uint16(len(changed)))
			case "ace-size":
				binary.LittleEndian.PutUint16(changed[10:12], 0)
			case "mask":
				binary.LittleEndian.PutUint32(changed[12:16], 1)
			case "sid-count":
				changed[17] = 16
			case "sid-revision":
				changed[16] = 0
			case "nonzero-spare":
				changed = append(changed, 1, 0, 0, 0)
				binary.LittleEndian.PutUint16(changed[2:4], uint16(len(changed)))
			case "allocation-length":
				binary.LittleEndian.PutUint16(changed[2:4], uint16(len(changed)+4))
			}
			if corruption == "allocation-length" {
				base := unsafe.Pointer(&changed[0])
				if ValidateACLInAllocation(base, uint64(len(changed)), uintptr(base), user.User.Sid, system, true, true) {
					t.Fatal("out-of-allocation ACL length admitted")
				}
				runtime.KeepAlive(changed)
			} else if ValidateACL(changed, user.User.Sid, system, true, true) {
				t.Fatal("malformed native ACL admitted", corruption)
			}
		})
	}
}
