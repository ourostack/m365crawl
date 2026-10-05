//go:build acceptance && windows

package acceptance

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func setCurrentUserAndSystemOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		setACL(t, path,
			aceForSIDWithInheritance(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
			aceForSIDWithInheritance(t, mustSystemSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
		)
		return
	}
	setACL(t, path,
		aceForSIDWithInheritance(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, 0),
		aceForSIDWithInheritance(t, mustSystemSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, 0),
	)
}

func setACL(t *testing.T, path string, entries ...windows.EXPLICIT_ACCESS) {
	t.Helper()
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatalf("acl for %s: %v", path, err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		t.Fatalf("set acl for %s: %v", path, err)
	}
}

func aceForSIDWithInheritance(t *testing.T, sid *windows.SID, perms windows.ACCESS_MASK, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	t.Helper()
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: perms,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func mustCurrentUserSID(t *testing.T) *windows.SID {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("token user: %v", err)
	}
	return user.User.Sid
}

func mustSystemSID(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("system sid: %v", err)
	}
	return sid
}
