package outlookdesktop

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var c *errs.Coded
	if !errors.As(err, &c) {
		t.Fatalf("error %v is not coded", err)
	}
	return c.Code
}

func TestDiscoverNoRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	_, _, _, err := Discover(root)
	var nf *RootNotFoundError
	if !errors.Is(err, ErrRootNotFound) || !errors.As(err, &nf) || nf.Root != root || nf.Error() == "" {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscoverNoProfile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "stray-file"), []byte("x"))
	if err := os.MkdirAll(filepath.Join(root, "Empty Profile"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, c, _, err := Discover(root)
	if err != nil || len(p) != 0 || len(c) != 0 {
		t.Fatalf("got %v %v %v", p, c, err)
	}
}

func TestDiscoverOneProfile(t *testing.T) {
	root := t.TempDir()
	store := fakeProfile(t, root, "Main Profile", 64)
	p, c, _, err := Discover(root)
	if err != nil || len(c) != 0 {
		t.Fatal(p, c, err)
	}
	want := []Profile{{Name: "Main Profile", Dir: filepath.Dir(store), StorePath: store}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %+v want %+v", p, want)
	}
}

func TestDiscoverSeveralProfilesSorted(t *testing.T) {
	root := t.TempDir()
	fakeProfile(t, root, "Zed", 8)
	fakeProfile(t, root, "Alpha", 8)
	fakeProfile(t, root, "Main Profile", 8)
	p, _, _, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, x := range p {
		names = append(names, x.Name)
	}
	if !reflect.DeepEqual(names, []string{"Alpha", "Main Profile", "Zed"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestDiscoverClassicSQLiteOnly(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Old Profile", "Data", "Outlook.sqlite"), []byte("sqlite"))
	mustWrite(t, filepath.Join(root, "Another Old", "Data", "Outlook.sqlite"), []byte("sqlite"))
	// A profile with both is a profile, not classic-only.
	store := fakeProfile(t, root, "Both", 8)
	mustWrite(t, filepath.Join(filepath.Dir(store), "Data", "Outlook.sqlite"), []byte("sqlite"))
	p, c, _, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || p[0].Name != "Both" {
		t.Fatalf("profiles = %+v", p)
	}
	if !reflect.DeepEqual(c, []string{"Another Old", "Old Profile"}) {
		t.Fatalf("classicOnly = %v", c)
	}
}

func TestDiscoverIgnoresStoreThatIsADirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Odd", StoreFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	p, c, _, err := Discover(root)
	if err != nil || len(p) != 0 || len(c) != 0 {
		t.Fatal(p, c, err)
	}
}

func TestDiscoverPermissionDeniedMapsToNoFullDiskAccess(t *testing.T) {
	root := t.TempDir()
	fakeProfile(t, root, "Main Profile", 8)

	orig := readDir
	t.Cleanup(func() { readDir = orig })
	readDir = func(string) ([]fs.DirEntry, error) {
		return nil, &fs.PathError{Op: "open", Path: root, Err: fs.ErrPermission}
	}
	_, _, _, err := Discover(root)
	if codeOf(t, err) != errs.CodeNoFullDiskAccess {
		t.Fatalf("readDir permission: %v", err)
	}

	readDir = orig
	origStat := statPath
	t.Cleanup(func() { statPath = origStat })
	statPath = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	_, _, _, err = Discover(root)
	if codeOf(t, err) != errs.CodeNoFullDiskAccess {
		t.Fatalf("stat permission: %v", err)
	}
}

func TestDiscoverOtherErrorsAreInternal(t *testing.T) {
	root := t.TempDir()
	fakeProfile(t, root, "Main Profile", 8)
	boom := errors.New("boom")

	orig := readDir
	t.Cleanup(func() { readDir = orig })
	readDir = func(string) ([]fs.DirEntry, error) { return nil, boom }
	if _, _, _, err := Discover(root); codeOf(t, err) != errs.CodeInternal {
		t.Fatalf("readDir: %v", err)
	}
	readDir = orig

	origStat := statPath
	t.Cleanup(func() { statPath = origStat })
	// Every directory unreadable and not for permission: the coded internal error.
	statPath = func(string) (fs.FileInfo, error) { return nil, boom }
	if _, _, sk, err := Discover(root); codeOf(t, err) != errs.CodeInternal || len(sk) != 1 {
		t.Fatalf("all unreadable: %v %v", sk, err)
	}
}

func TestDiscoverOneUnreadableProfileDoesNotHideTheOthers(t *testing.T) {
	root := t.TempDir()
	fakeProfile(t, root, "Good", 8)
	fakeProfile(t, root, "Denied", 8)
	fakeProfile(t, root, "Broken", 8)
	mustWrite(t, filepath.Join(root, "Classic", "Data", "Outlook.sqlite"), []byte("s"))
	boom := errors.New("boom")
	origStat := statPath
	t.Cleanup(func() { statPath = origStat })
	statPath = func(p string) (fs.FileInfo, error) {
		switch filepath.Base(filepath.Dir(p)) {
		case "Denied":
			return nil, fs.ErrPermission
		case "Broken":
			return nil, boom
		}
		return os.Stat(p)
	}
	p, c, sk, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 || p[0].Name != "Good" || !reflect.DeepEqual(c, []string{"Classic"}) {
		t.Fatalf("p=%v c=%v", p, c)
	}
	if len(sk) != 2 || sk[0].Name != "Broken" || sk[0].Reason != "unreadable" || !errors.Is(sk[0].Err, boom) ||
		sk[1].Name != "Denied" || sk[1].Reason != "no_full_disk_access" || sk[1].Dir != filepath.Join(root, "Denied") {
		t.Fatalf("skipped = %+v", sk)
	}
}

func TestDiscoverClassicStatFailureSkipsOnlyThatProfile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Odd"), 0o700); err != nil {
		t.Fatal(err)
	}
	origStat := statPath
	t.Cleanup(func() { statPath = origStat })
	statPath = func(p string) (fs.FileInfo, error) {
		if filepath.Base(p) == "Outlook.sqlite" {
			return nil, fs.ErrPermission
		}
		return os.Stat(p)
	}
	_, _, sk, err := Discover(root)
	if codeOf(t, err) != errs.CodeNoFullDiskAccess || len(sk) != 1 {
		t.Fatalf("sk=%v err=%v", sk, err)
	}
}

func TestDefaultRootHomeError(t *testing.T) {
	orig := userHome
	t.Cleanup(func() { userHome = orig })
	userHome = func() (string, error) { return "", errors.New("no home") }
	if _, err := DefaultRoot(); codeOf(t, err) != errs.CodeInternal {
		t.Fatalf("err = %v", err)
	}
}

func TestNotSupportedErrorMatches(t *testing.T) {
	e := &NotSupportedError{GOOS: "plan9"}
	if !errors.Is(e, ErrNotSupported) || errors.Is(e, ErrRootNotFound) || e.Error() == "" {
		t.Fatal(e)
	}
}
