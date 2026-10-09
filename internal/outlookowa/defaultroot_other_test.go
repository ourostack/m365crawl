//go:build !windows

package outlookowa

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultRootUnsupportedButExplicitRootsWork(t *testing.T) {
	t.Setenv("LOCALAPPDATA", sourceTempDir(t))
	if root, err := DefaultRoot(); err == nil || root != "" {
		t.Fatalf("non-Windows default enabled: %q %v", root, err)
	}
	root := sourceTempDir(t)
	p := fixtureProfile(t, root, "Default", "mail", false)
	if found, skipped, err := Discover(root); err != nil || len(found) != 1 || len(skipped) != 0 {
		t.Fatalf("explicit portable source: %+v %+v %v", found, skipped, err)
	}
	target := filepath.Join(sourceTempDir(t), "target")
	if err := os.WriteFile(target, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(p.Source.LevelDBDir, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Fingerprint(p); err == nil {
		t.Fatal("symlink fingerprint accepted")
	}
}
