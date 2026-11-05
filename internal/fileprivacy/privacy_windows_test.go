//go:build windows

package fileprivacy

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func privateWindowsFixture(t *testing.T, inheritance, extra string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;" + inheritance +
		";FA;;;" + user.User.Sid.String() + ")(A;" + inheritance + ";FA;;;SY)" + extra)
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil)
	runtime.KeepAlive(sd)
	runtime.KeepAlive(user)
	if err != nil {
		t.Fatal("native controlled fixture setup failed", err)
	}
	return dir
}

func TestPrivateImageWindowsHandlePolicy(t *testing.T) {
	for _, fixture := range []struct {
		name, inheritance, extra string
		want                     bool
	}{
		{"private-object-inheritance", "OICI", "", true},
		{"effective-only", "", "", false},
		{"container-only", "CI", "", false},
		{"public-reader", "OICI", "(A;OICI;FR;;;WD)", false},
		{"no-propagate", "OINP", "", false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			dir := privateWindowsFixture(t, fixture.inheritance, fixture.extra)
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			parent, err := root.Open(".")
			if err != nil {
				t.Fatal("actual bound Windows directory handle unavailable", err)
			}
			defer func() {
				if err := errors.Join(parent.Close(), root.Close()); err != nil {
					t.Error(err)
				}
			}()
			if got := Parent(parent) == nil; got != fixture.want {
				t.Fatalf("effective/inheritable handle policy=%v want=%v", got, fixture.want)
			}
			if fixture.want {
				temp, err := root.OpenFile("private-temp", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := Temporary(temp); err != nil {
					t.Error("exact inherited private child handle refused", err)
				}
				if err := temp.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPrivateImageWindowsOwnerAndQueryRefusal(t *testing.T) {
	dir := privateWindowsFixture(t, "OICI", "")
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := Parent(parent); err != nil {
		t.Fatal("valid native handle failed before injected boundary", err)
	}
	oldUser, oldSecurity := privateTokenUser, privateGetSecurity
	t.Cleanup(func() { privateTokenUser, privateGetSecurity = oldUser, oldSecurity })
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	privateTokenUser = func() (*windows.Tokenuser, error) {
		return &windows.Tokenuser{User: windows.SIDAndAttributes{Sid: system}}, nil
	}
	if err := Parent(parent); err == nil {
		t.Fatal("handle owner mismatch admitted")
	}
	privateTokenUser = oldUser
	privateGetSecurity = func(windows.Handle, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION) (*windows.SECURITY_DESCRIPTOR, error) {
		return nil, errors.New("synthetic exact-handle security query failure")
	}
	if err := Parent(parent); err == nil {
		t.Fatal("unknown handle protection admitted")
	}
	runtime.KeepAlive(system)
}

func TestPrivateImageWindowsACLAllocationAndLegacyPolicy(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		runtime.KeepAlive(user)
		runtime.KeepAlive(system)
	}()
	for _, inheritance := range []string{"", "OICI", "OINP"} {
		sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inheritance + ";FA;;;" +
			user.User.Sid.String() + ")(A;" + inheritance + ";FA;;;SY)")
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil {
			t.Fatal(err)
		}
		base := unsafe.Pointer(sd)
		size := uint64(sd.Length())
		address := uintptr(unsafe.Pointer(acl))
		got := ValidateACLInAllocation(base, size, address, user.User.Sid, system, true, true)
		if got != (inheritance == "OICI") {
			t.Fatal("allocation-bounded parser did not enforce object inheritance")
		}
		if inheritance == "" && !ValidateACLInAllocation(base, size, address, user.User.Sid, system, true, false) {
			t.Fatal("legacy effective-only snapshot policy changed")
		}
		if ValidateACLInAllocation(base, size, uintptr(base)+uintptr(size), user.User.Sid, system, true, true) {
			t.Fatal("out-of-allocation ACL pointer admitted")
		}
		runtime.KeepAlive(sd)
	}
}
