//go:build windows

package teamsdesktop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/snapshotowner"
	"golang.org/x/sys/windows"
)

func TestSnapshotFreshWindowsOwnership(t *testing.T) {
	privateTempDir(t)
	user, _, err := privateSIDs()
	if err != nil {
		t.Fatal(err)
	}
	set := snapshotSetSecurity
	t.Cleanup(func() { snapshotSetSecurity = set })
	calls := 0
	wantOwner := true
	snapshotSetSecurity = func(path string, kind windows.SE_OBJECT_TYPE, info windows.SECURITY_INFORMATION, owner, group *windows.SID, dacl, sacl *windows.ACL) error {
		calls++
		want := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
		if wantOwner {
			want |= windows.OWNER_SECURITY_INFORMATION
			if owner == nil || !owner.Equals(user) {
				t.Fatal("fresh snapshot creation did not explicitly select the current user")
			}
		} else if owner != nil {
			t.Fatal("securing a possibly existing directory attempted owner repair")
		}
		if kind != windows.SE_FILE_OBJECT || info != want || group != nil || dacl == nil || sacl != nil {
			t.Fatal("snapshot security request changed its protected DACL contract")
		}
		return set(path, kind, info, owner, group, dacl, sacl)
	}
	root, err := makeSnapshotRoot()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "owned-payload")
	f, err := openSnapshotFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fresh root/file security calls = %d", calls)
	}
	if _, err := openSnapshotFile(path); !errors.Is(err, os.ErrExist) || calls != 2 {
		t.Fatalf("exclusive refusal attempted existing-file ownership repair: %v calls=%d", err, calls)
	}
	wantOwner = false
	if err := makeSnapshotDir(root); err != nil || calls != 3 {
		t.Fatalf("existing private directory securing: %v calls=%d", err, calls)
	}
	if err := snapshotowner.Register(root); err != nil {
		t.Fatalf("our fresh creator is incompatible with ownership registration: %v", err)
	}
	for _, path := range []string{root, filepath.Join(root, ".m365crawl-owner.json"), filepath.Join(root, "owned-payload")} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil || !owner.Equals(user) {
			t.Fatalf("fresh object does not belong to current user: %s %v", path, err)
		}
	}
}
