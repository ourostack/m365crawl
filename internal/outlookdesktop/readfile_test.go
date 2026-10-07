package outlookdesktop

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyOpensWithTheReadOnlyFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.dat")
	mustWrite(t, path, []byte("fixture"))
	var gotPath string
	var gotFlag int
	var gotPerm os.FileMode
	swap(t, &openFile, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		gotPath, gotFlag, gotPerm = name, flag, perm
		return os.OpenFile(name, flag, perm) //nolint:gosec // test wrapper over the seam; the name comes from the code under test
	})
	f, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if gotPath != path || gotFlag != sourceOpenFlags || gotPerm != 0 {
		t.Fatalf("open(%q, %#x, %v), want the path with the read-only flags", gotPath, gotFlag, gotPerm)
	}
	if gotFlag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		t.Fatalf("flag %#x contains a write bit", gotFlag)
	}
	if b, err := io.ReadAll(f); err != nil || string(b) != "fixture" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := OpenReadOnly(path + ".missing"); !os.IsNotExist(err) {
		t.Fatalf("a missing file: %v", err)
	}
}
