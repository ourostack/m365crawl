//go:build !windows

package fileprivacy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateImageParentAndTemporaryPolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(dir) //nolint:gosec // G304: test-owned private directory
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := parent.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := Parent(parent); err != nil {
		t.Fatal("controlled owned parent refused", err)
	}
	path := filepath.Join(dir, "temporary")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: exclusively created test-owned temporary path
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := Temporary(file); err != nil {
		t.Fatal("private regular temporary handle refused", err)
	}
	if Parent(file) == nil || Temporary(parent) == nil {
		t.Fatal("wrong handle type admitted")
	}
	if err := file.Chmod(0o640); err != nil {
		t.Fatal(err)
	}
	if err := Temporary(file); err == nil {
		t.Fatal("group-readable temporary bytes admitted")
	}
	if err := parent.Chmod(0o777); err != nil {
		t.Fatal(err)
	}
	if err := Parent(parent); err == nil {
		t.Fatal("mutable parent admitted")
	}
}
