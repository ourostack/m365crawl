//go:build darwin || linux

package outlookdesktop

import (
	"path/filepath"
	"testing"
)

func TestRealFreeBytes(t *testing.T) {
	avail, ok, err := realFreeBytes(t.TempDir())
	if err != nil || !ok || avail == 0 {
		t.Fatalf("avail=%d ok=%v err=%v", avail, ok, err)
	}
	if _, _, err := realFreeBytes(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}
