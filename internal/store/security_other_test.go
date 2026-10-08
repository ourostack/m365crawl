//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func assertCurrentUserAndSystemOnly(t *testing.T, _ string) {
	t.Helper()
	t.Fatal("assertCurrentUserAndSystemOnly is Windows-only")
}

func setCurrentUserAndSystemOnly(t *testing.T, _ string) {
	t.Helper()
}

func TestPrivateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	if _, err := PrivateDir(dir); err == nil {
		t.Fatal("a missing directory is an error")
	}
	must0(os.Mkdir(dir, 0o700))
	for mode, want := range map[os.FileMode]bool{0o700: true, 0o750: false, 0o705: false} {
		must0(os.Chmod(dir, mode))
		if got, err := PrivateDir(dir); err != nil || got != want {
			t.Errorf("%o: %v, %v; want %v", mode, got, err, want)
		}
	}
}
