//go:build windows

package outlookowa

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDefaultRootWindows(t *testing.T) {
	local := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	root, err := DefaultRoot()
	if err != nil || root != filepath.Join(local, "Microsoft", "Olk", "EBWebView") {
		t.Fatalf("default root: %q %v", root, err)
	}
	t.Setenv("LOCALAPPDATA", "")
	if _, err := DefaultRoot(); err == nil {
		t.Fatal("missing LOCALAPPDATA silently defaulted")
	}
	t.Setenv("LOCALAPPDATA", "relative")
	if _, err := DefaultRoot(); err == nil {
		t.Fatal("relative LOCALAPPDATA accepted")
	}
}

func TestDiscoverRejectsWindowsJunctions(t *testing.T) {
	for _, location := range []string{"profile", "IndexedDB", "leveldb", "blob", "root"} {
		t.Run(location, func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			p := fixtureProfile(t, root, "Default", "empty", false)
			var link string
			switch location {
			case "profile":
				link = filepath.Join(root, "Linked")
			case "IndexedDB":
				link = filepath.Join(root, "Linked", "IndexedDB")
				if err := os.Mkdir(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
			case "leveldb":
				link = filepath.Join(p.Dir, "IndexedDB", "https_outlook.office.com_443.indexeddb.leveldb")
			case "blob":
				link = filepath.Join(p.Dir, "IndexedDB", fixtureOrigin+".indexeddb.blob")
			case "root":
				link = filepath.Join(root, "LinkedRoot")
			}
			b, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
			if err != nil {
				t.Fatalf("synthetic junction creation: %v %s", err, b)
			}
			t.Cleanup(func() {
				if err := os.Remove(link); err != nil {
					t.Error(err)
				}
			})
			if location == "root" {
				if _, _, err := Discover(link); err == nil {
					t.Fatal("junction root accepted")
				}
			} else {
				_, skipped, err := Discover(root)
				if err != nil || len(skipped) == 0 {
					t.Fatalf("junction not rejected: %+v %v", skipped, err)
				}
			}
			if _, err := sourceIdentity(link, nil); err == nil || err.Error() != "outlookowa: unsafe_source" {
				t.Fatalf("native identity followed reparse point: %v", err)
			}
		})
	}
}

func TestWindowsIdentityErrorsAreFixed(t *testing.T) {
	for _, path := range []string{"synthetic\x00invalid", filepath.Join(t.TempDir(), "missing")} {
		if _, err := sourceIdentity(path, nil); err == nil || err.Error() != "outlookowa: source_unreadable" {
			t.Fatalf("identity error was not fixed: %v", err)
		}
	}
}
