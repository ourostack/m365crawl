//go:build windows

package snapshotowner_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/snapshotowner"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"golang.org/x/sys/windows"
)

// No policy is changed: this diagnoses the real creator/Register boundary on an owned root.
func TestSnapshotOwnerWindowsPrivateRootDiagnostic(t *testing.T) {
	parent := t.TempDir()
	for _, key := range []string{"TMP", "TEMP", "TMPDIR"} {
		t.Setenv(key, parent)
	}
	dir, err := teamsdesktop.NewPrivateSnapshotRoot()
	if err != nil {
		t.Fatalf("private-root creator: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if rel, err := filepath.Rel(parent, dir); err != nil || !filepath.IsLocal(rel) {
		t.Fatalf("diagnostic creator escaped its owned temp parent: %v", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	inspect := func(label, path string) {
		t.Helper()
		info, err := os.Lstat(path)
		if err != nil {
			t.Logf("%s lstat_error=%v", label, err)
			return
		}
		t.Logf("%s absolute=%t directory=%t regular=%t symlink=%t mode=%v", label,
			filepath.IsAbs(path), info.IsDir(), info.Mode().IsRegular(), info.Mode()&os.ModeSymlink != 0, info.Mode())
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil || sd == nil {
			t.Logf("%s descriptor_error=%v nil=%t", label, err, sd == nil)
			return
		}
		owner, defaulted, err := sd.Owner()
		t.Logf("%s owner_error=%v defaulted=%t matches_current_user=%t matches_SYSTEM=%t", label,
			err, defaulted, owner != nil && owner.Equals(user.User.Sid), owner != nil && owner.Equals(system))
		control, revision, err := sd.Control()
		t.Logf("%s control=%x revision=%d protected=%t control_error=%v", label,
			control, revision, control&windows.SE_DACL_PROTECTED != 0, err)
		acl, aclDefaulted, err := sd.DACL()
		if err != nil || acl == nil {
			t.Logf("%s dacl_error=%v nil=%t defaulted=%t", label, err, acl == nil, aclDefaulted)
			return
		}
		t.Logf("%s ace_count=%d current_user_is_SYSTEM=%t", label, acl.AceCount, user.User.Sid.Equals(system))
		for i := uint32(0); i < uint32(acl.AceCount) && i < 16; i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, i, &ace); err != nil || ace == nil {
				t.Logf("%s ace=%d error=%v nil=%t", label, i, err, ace == nil)
				continue
			}
			t.Logf("%s ace=%d type=%d flags=%x size=%d", label, i,
				ace.Header.AceType, ace.Header.AceFlags, ace.Header.AceSize)
			if ace.Header.AceSize >= 8 {
				t.Logf("%s ace=%d mask=%x", label, i, ace.Mask)
			}
		}
		runtime.KeepAlive(sd)
	}
	inspect("created-root", dir)
	err = snapshotowner.Register(dir)
	t.Logf("register_error=%v", err)
	inspect("post-register-root", dir)
	inspect("post-register-marker", filepath.Join(dir, ".m365crawl-owner.json"))
	if err != nil {
		t.Errorf("W-OWN-01 real private-root/Register incompatibility: %v", err)
	}
}
