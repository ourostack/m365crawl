//go:build darwin || linux || windows

package diskspace

import (
	"path/filepath"
	"testing"
)

func TestFree(t *testing.T) {
	avail, measured, err := Free(t.TempDir())
	if err != nil || !measured || avail == 0 {
		t.Fatalf("avail=%d measured=%v err=%v", avail, measured, err)
	}
	if _, _, err := Free(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
