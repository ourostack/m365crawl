//go:build !windows

package imageexport

import (
	"os"
	"path/filepath"
	"testing"
)

func imagePrivateParent(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}
