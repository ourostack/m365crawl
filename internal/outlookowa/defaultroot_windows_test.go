//go:build windows

package outlookowa

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
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

func TestWindowsHandleInformationFailureClosesHandleAndRetries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(file, []byte("synthetic metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := sourceHandleInfo
	var handle windows.Handle
	sourceHandleInfo = func(h windows.Handle, info *windows.ByHandleFileInformation) error {
		handle = h
		if err := old(h, info); err != nil {
			return err
		}
		return fs.ErrPermission
	}
	t.Cleanup(func() { sourceHandleInfo = old })
	if id, err := sourceIdentity(file, nil); id != "" || !errors.Is(err, fs.ErrPermission) || err.Error() != "outlookowa: source_unreadable" {
		t.Fatalf("native handle fault returned identity or unsafe diagnostic: %q %v", id, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("failed identity left its native handle open: %v", err)
	}
	sourceHandleInfo = old
	if id, err := sourceIdentity(file, nil); err != nil || id == "" {
		t.Fatalf("identity failure blocked later eligible read: %q %v", id, err)
	}
}

func TestFingerprintRejectsReparseReplacementAfterValidation(t *testing.T) {
	restoreSourceIO(t)
	p := fixtureProfile(t, sourceTempDir(t), "Default", "mail", false)
	current := filepath.Join(p.Source.LevelDBDir, "CURRENT")
	content, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	linked := false
	restore := func() {
		if linked {
			if err := os.Remove(current); err != nil {
				t.Error(err)
			}
			if err := os.WriteFile(current, content, 0o600); err != nil {
				t.Error(err)
			}
			linked = false
		}
	}
	t.Cleanup(restore)
	walk := sourceWalkDir
	validated := false
	sourceWalkDir = func(path string, fn fs.WalkDirFunc) error {
		if !validated {
			err := walk(path, fn)
			validated = true
			return err
		}
		if err := os.Remove(current); err != nil {
			return err
		}
		out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", current, target).CombinedOutput()
		if err != nil {
			t.Fatalf("controlled concurrent junction: %v %s", err, out)
		}
		linked = true
		return walk(path, fn)
	}
	fp, fresh, err := Fingerprint(p)
	if err == nil || err.Error() != "outlookowa: unsafe_source" || fp != "" || !fresh.IsZero() {
		t.Fatalf("concurrent reparse entry vouched for a fingerprint: %q %v %v", fp, fresh, err)
	}
	restore()
	sourceWalkDir = walk
	if fp, _, err := Fingerprint(p); err != nil || fp == "" {
		t.Fatalf("restored controlled source cannot retry: %q %v", fp, err)
	}
}
