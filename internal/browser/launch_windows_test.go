//go:build windows

package browser

import (
	"os"
	"testing"
)

func checkProfileMode(t *testing.T, p string) {
	t.Helper()
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		t.Fatalf("profile = %v %v", fi, err)
	}
}

func skipStdioCheckOnWindows(t *testing.T) {
	t.Helper()
	t.Skip("handle identity of the null device is not comparable on Windows")
}
