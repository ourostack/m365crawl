//go:build windows

package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/ourostack/teamscrawl/internal/errs"
	"golang.org/x/sys/windows"
)

func TestOpenRejectsNonInheritingPrivateCustomParent(t *testing.T) {
	for _, flags := range []uint32{0, windows.OBJECT_INHERIT_ACE, windows.CONTAINER_INHERIT_ACE, windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.NO_PROPAGATE_INHERIT_ACE} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "custom")
			must0(os.Mkdir(parent, 0o700))
			setACL(t, parent,
				aceForSIDWithInheritance(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, flags),
				aceForSIDWithInheritance(t, mustSystemSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, flags),
			)
			dbPath := filepath.Join(parent, "archive.db")
			assertOpenDBError(t, dbPath)
			assertNoArchiveArtifacts(t, dbPath)
		})
	}
}

func TestOpenRejectsUnsafeExistingSQLiteSidecarsBeforeSQLiteOpen(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "private")
			must0(os.Mkdir(parent, 0o700))
			setInheritableCurrentUserAndSystemOnly(t, parent)
			dbPath := filepath.Join(parent, "archive.db")
			must0(os.WriteFile(dbPath, nil, 0o600))
			sidecar := dbPath + suffix
			content := []byte("synthetic existing sidecar")
			must0(os.WriteFile(sidecar, content, 0o600))
			setBroadACL(t, sidecar)
			assertOpenDBError(t, dbPath)
			got, err := os.ReadFile(sidecar) //nolint:gosec // sidecar is created by this test under t.TempDir
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("sidecar changed before rejection: %q, %v", got, err)
			}
			info, err := os.Stat(dbPath)
			if err != nil || info.Size() != 0 {
				t.Fatalf("archive changed before rejection: %v, %v", info, err)
			}
			assertWorldTrustee(t, sidecar)
		})
	}
}

func TestPrepareArchivePreservesPrivateExistingSQLiteSidecars(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	must0(os.Mkdir(parent, 0o700))
	setInheritableCurrentUserAndSystemOnly(t, parent)
	dbPath := filepath.Join(parent, "archive.db")
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		must0(os.WriteFile(dbPath+suffix, []byte("synthetic private companion"), 0o600))
	}
	must0(prepareArchiveForWrite(dbPath))
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		got, err := os.ReadFile(dbPath + suffix) //nolint:gosec // sidecar is created by this test under t.TempDir
		if err != nil || string(got) != "synthetic private companion" {
			t.Fatalf("private sidecar changed: %q, %v", got, err)
		}
		assertCurrentUserAndSystemOnly(t, dbPath+suffix)
	}
}

func TestOpenRejectsSQLiteSidecarDirectoriesBeforeSQLiteOpen(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "new-parent", "archive.db")
			must0(os.MkdirAll(dbPath+suffix, 0o700))
			setInheritableCurrentUserAndSystemOnly(t, filepath.Dir(dbPath))
			assertOpenDBError(t, dbPath)
			if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
				t.Fatalf("database created before companion validation: %v", err)
			}
		})
	}
}

func TestOpenRejectsUnsafeMacStyleCustomParentOnWindows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	parent := filepath.Join(home, ".teamscrawl")
	must0(os.Mkdir(parent, 0o700))
	setBroadACL(t, parent)
	dbPath := filepath.Join(parent, "archive.db")
	assertOpenDBError(t, dbPath)
	assertNoArchiveArtifacts(t, dbPath)
	assertWorldTrustee(t, parent)
}

func TestOpenSecuresExistingWindowsDefaultParent(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("USERPROFILE", home)
			base := filepath.Join(home, "AppData", "Local")
			if fallback {
				t.Setenv("LOCALAPPDATA", "")
			} else {
				t.Setenv("LOCALAPPDATA", base)
			}
			parent := filepath.Join(base, "teamscrawl")
			must0(os.MkdirAll(parent, 0o700))
			setBroadACL(t, parent)
			st, err := Open(context.Background(), filepath.Join(parent, "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			assertCurrentUserAndSystemOnly(t, parent)
		})
	}
}

func TestOpenRejectsUnsafeInheritedFilesInWindowsDefaultParent(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("LOCALAPPDATA", base)
			parent := filepath.Join(base, "teamscrawl")
			must0(os.Mkdir(parent, 0o700))
			setACL(t, parent,
				aceForSIDWithInheritance(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
				aceForSIDWithInheritance(t, mustWorldSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
			)
			dbPath := filepath.Join(parent, "archive.db")
			must0(os.WriteFile(dbPath+suffix, nil, 0o600))
			assertOpenDBError(t, dbPath)
			assertWorldTrustee(t, dbPath+suffix)
		})
	}
}

func assertOpenDBError(t *testing.T, dbPath string) {
	t.Helper()
	st, err := Open(context.Background(), dbPath)
	if st != nil {
		_ = st.Close()
	}
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError {
		t.Fatalf("err = %v, want coded db_error before SQLite open", err)
	}
}

func assertWorldTrustee(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := aceSIDStrings(t, dacl)[mustWorldSID(t).String()]; !ok {
		t.Fatal("unsafe pre-existing ACL was silently rewritten")
	}
}

func TestArchiveSidecarsSecureAgainstLooseParent(t *testing.T) {
	ctx := context.Background()
	loose := filepath.Join(t.TempDir(), "loose")
	if err := os.MkdirAll(loose, 0o700); err != nil {
		t.Fatal(err)
	}
	setBroadACL(t, loose)

	dbPath := filepath.Join(loose, "private", "archive.db")
	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	release, err := AcquireLock(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	must0(st.ApplyAccount(ctx, acctA))

	for _, path := range []string{filepath.Dir(dbPath), dbPath, dbPath + ".lock", dbPath + "-wal", dbPath + "-shm"} {
		assertCurrentUserAndSystemOnly(t, path)
	}
}

func TestOpenRejectsUnsafeExistingCustomParentBeforeSQLiteOpen(t *testing.T) {
	ctx := context.Background()
	parent := filepath.Join(t.TempDir(), "custom-parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	setBroadACL(t, parent)

	dbPath := filepath.Join(parent, "archive.db")
	_, err := Open(ctx, dbPath)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError {
		t.Fatalf("err = %v", err)
	}
	assertNoArchiveArtifacts(t, dbPath)
}

func TestOpenRejectsUnsafeExistingArchiveFileBeforeSQLiteOpen(t *testing.T) {
	ctx := context.Background()
	parent := filepath.Join(t.TempDir(), "private-parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	setInheritableCurrentUserAndSystemOnly(t, parent)

	dbPath := filepath.Join(parent, "archive.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setBroadACL(t, dbPath)

	_, err := Open(ctx, dbPath)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError {
		t.Fatalf("err = %v", err)
	}
	assertNoArchiveArtifacts(t, dbPath)
}

func TestOpenAllowsInheritedPrivateCustomParentAndArchiveFile(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "private-root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	setInheritableCurrentUserAndSystemOnly(t, root)

	parent := filepath.Join(root, "custom-parent")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(parent, "archive.db")
	if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	assertInheritedCurrentUserAndSystemOnly(t, parent)
	assertInheritedCurrentUserAndSystemOnly(t, dbPath)

	st, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
}

func assertNoArchiveArtifacts(t *testing.T, dbPath string) {
	t.Helper()
	if info, err := os.Stat(dbPath); err == nil {
		if info.Size() != 0 {
			t.Fatalf("%s size = %d, want 0", dbPath, info.Size())
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat %s: %v", dbPath, err)
	}
	for _, suffix := range []string{".lock", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("%s exists err=%v", dbPath+suffix, err)
		}
	}
}

func assertCurrentUserAndSystemOnly(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("security info for %s: %v", path, err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("dacl for %s: %v", path, err)
	}
	if dacl == nil {
		t.Fatalf("nil dacl for %s", path)
	}

	got := aceSIDStrings(t, dacl)
	want := map[string]struct{}{mustCurrentUserSID(t).String(): {}, mustSystemSID(t).String(): {}}
	if len(got) != len(want) {
		t.Fatalf("%s trustees = %v, want %v", path, got, keys(want))
	}
	for sid := range want {
		if _, ok := got[sid]; !ok {
			t.Fatalf("%s trustees = %v, want %v", path, got, keys(want))
		}
	}
}

func setCurrentUserAndSystemOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		setInheritableCurrentUserAndSystemOnly(t, path)
		return
	}
	setACL(t, path,
		aceForSID(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER),
		aceForSID(t, mustSystemSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER),
	)
}

func setInheritableCurrentUserAndSystemOnly(t *testing.T, path string) {
	t.Helper()
	setACL(t, path,
		aceForSIDWithInheritance(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
		aceForSIDWithInheritance(t, mustSystemSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER, windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE),
	)
}

func setBroadACL(t *testing.T, path string) {
	t.Helper()
	setACL(t, path,
		aceForSID(t, mustCurrentUserSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_USER),
		aceForSID(t, mustWorldSID(t), windows.GENERIC_ALL, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
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

func aceForSID(t *testing.T, sid *windows.SID, perms windows.ACCESS_MASK, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	t.Helper()
	return aceForSIDWithInheritance(t, sid, perms, trusteeType, 0)
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

func assertInheritedCurrentUserAndSystemOnly(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("security info for %s: %v", path, err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("control for %s: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED != 0 {
		t.Fatalf("%s has protected dacl, want inherited/private test fixture", path)
	}
	assertCurrentUserAndSystemOnly(t, path)
}

func aceSIDStrings(t *testing.T, acl *windows.ACL) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, uint32(i), &ace); err != nil {
			t.Fatalf("get ace %d: %v", i, err)
		}
		out[(*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()] = struct{}{} //nolint:gosec // Windows ACCESS_ALLOWED_ACE stores the SID inline after SidStart
	}
	return out
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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

func mustWorldSID(t *testing.T) *windows.SID {
	t.Helper()
	sid, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatalf("world sid: %v", err)
	}
	return sid
}
