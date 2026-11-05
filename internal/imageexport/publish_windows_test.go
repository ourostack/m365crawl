//go:build windows

package imageexport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/windows"
)

func imagePrivateParent(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	imageFixtureACL(t, dir, "OICI")
	return dir
}

func imageFixtureACL(t *testing.T, path, inheritance string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() +
		"D:P(A;" + inheritance + ";FA;;;" + user.User.Sid.String() + ")(A;" + inheritance + ";FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil)
	runtime.KeepAlive(sd)
	runtime.KeepAlive(user)
	if err != nil {
		t.Fatal("controlled fixture ACL setup failed", err)
	}
}

func TestImageParentRequiresPrivateObjectInheritance(t *testing.T) {
	dir := imagePrivateParent(t)
	imageFixtureACL(t, dir, "")
	output := filepath.Join(dir, "selected.png")
	reservation, effect, err := Reserve(t.Context(), output)
	if err == nil || reservation != nil || effect.FilePublished == nil || *effect.FilePublished {
		t.Fatalf("effective-only directory admitted before acquisition: %+v %v", effect, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("unsafe parent gained a temporary file", err)
	}
}
