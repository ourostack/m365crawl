//go:build windows

package snapshotowner

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSnapshotOwnerWindowsFreshMarkerOwnership(t *testing.T) {
	dir := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	var pinner runtime.Pinner
	pinner.Pin(user.User.Sid)
	pinner.Pin(system)
	defer pinner.Unpin()
	entries := []windows.EXPLICIT_ACCESS{}
	for _, sid := range []*windows.SID{user.User.Sid, system} {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.GRANT_ACCESS,
			Inheritance: windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE,
			Trustee:     windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(sid)},
		})
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	set := ownerSetSecurity
	t.Cleanup(func() { ownerSetSecurity = set })
	calls := 0
	ownerSetSecurity = func(path string, kind windows.SE_OBJECT_TYPE, info windows.SECURITY_INFORMATION, owner, group *windows.SID, dacl, sacl *windows.ACL) error {
		calls++
		if path != filepath.Join(dir, markerName) || kind != windows.SE_FILE_OBJECT ||
			info != windows.OWNER_SECURITY_INFORMATION || owner == nil || !owner.Equals(user.User.Sid) ||
			group != nil || dacl != nil || sacl != nil {
			t.Fatal("fresh marker must set only its current-user owner, not alter inherited DACL")
		}
		return set(path, kind, info, owner, group, dacl, sacl)
	}
	if err := Register(dir); err != nil || calls != 1 {
		t.Fatalf("native fresh marker: %v calls=%d", err, calls)
	}
	info, err := os.Lstat(filepath.Join(dir, markerName))
	if err != nil || !platformProtected(filepath.Join(dir, markerName), info) {
		t.Fatalf("native marker does not satisfy unchanged owner/DACL validator: %v", err)
	}
	if err := Register(dir); err == nil || calls != 1 {
		t.Fatalf("native exclusive refusal changed owner: %v calls=%d", err, calls)
	}
}

func TestSnapshotOwnerWindowsFreshMarkerErrors(t *testing.T) {
	token, set := ownerTokenUser, ownerSetSecurity
	t.Cleanup(func() { ownerTokenUser, ownerSetSecurity = token, set })
	f, err := os.CreateTemp(t.TempDir(), "fresh-marker-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	fault := errors.New("synthetic current-token refusal")
	ownerTokenUser = func() (*windows.Tokenuser, error) { return nil, fault }
	ownerSetSecurity = func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION, *windows.SID, *windows.SID, *windows.ACL, *windows.ACL) error {
		t.Fatal("token uncertainty must not attempt ownership assignment")
		return nil
	}
	if err := platformOwnMarker(f); !errors.Is(err, fault) {
		t.Fatalf("lost token failure: %v", err)
	}
	ownerTokenUser = token
	ownerSetSecurity = func(string, windows.SE_OBJECT_TYPE, windows.SECURITY_INFORMATION, *windows.SID, *windows.SID, *windows.ACL, *windows.ACL) error {
		return fault
	}
	if err := platformOwnMarker(f); !errors.Is(err, fault) {
		t.Fatalf("lost native setter failure: %v", err)
	}
}
