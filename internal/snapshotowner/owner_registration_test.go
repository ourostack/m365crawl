package snapshotowner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotOwnerRegistrationRefusesUnqualifiedRoots(t *testing.T) {
	for _, name := range []string{"relative", "unprotected-root", "unprotected-marker", "invalid-pid", "root-stat-error", "root-open-error", "marker-already-directory", "root-replaced", "opened-root-mismatch", "encode-error", "close-error"} {
		t.Run(name, func(t *testing.T) {
			pid, stat, protected, dead := ownerPID, ownerLstat, ownerProtected, ownerProcessDead
			encode := ownerEncodeMarker
			t.Cleanup(func() {
				ownerPID, ownerLstat, ownerProtected, ownerProcessDead = pid, stat, protected, dead
				ownerEncodeMarker = encode
			})
			ownerPID = func() int {
				if name == "invalid-pid" {
					return 0
				}
				return 4242
			}
			ownerProcessDead = func(int) (bool, error) {
				t.Fatal("registration queried process liveness")
				return false, nil
			}
			dir, other := t.TempDir(), t.TempDir()
			if name == "relative" {
				dir = "relative-owner-root"
			} else if name == "root-open-error" {
				dir = filepath.Join(dir, "absent")
			}
			path := filepath.Join(dir, markerName)
			if name == "marker-already-directory" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ownerProtected = func(p string, _ fs.FileInfo) bool {
				return name != "unprotected-root" && (name != "unprotected-marker" || p != path)
			}
			calls := 0
			ownerLstat = func(p string) (fs.FileInfo, error) {
				if name == "root-stat-error" {
					return nil, fs.ErrPermission
				}
				if p == dir {
					calls++
					if name == "root-open-error" || (name == "root-replaced" && calls > 1) ||
						(name == "opened-root-mismatch" && calls == 1) {
						return os.Lstat(other)
					}
				}
				return os.Lstat(p)
			}
			sentinel := errors.New("synthetic marker encoder refusal")
			var markerFile *os.File
			t.Cleanup(func() {
				if markerFile != nil {
					_ = markerFile.Close()
				}
			})
			if name == "encode-error" || name == "close-error" {
				ownerEncodeMarker = func(f *os.File, m marker) error {
					markerFile = f
					if name == "encode-error" {
						return sentinel
					}
					if err := json.NewEncoder(f).Encode(m); err != nil {
						t.Fatalf("close-fault fixture encode: %v", err)
					}
					if err := f.Close(); err != nil {
						t.Fatalf("close-fault fixture first close: %v", err)
					}
					return nil
				}
			}
			err := Register(dir)
			if err == nil {
				t.Fatalf("unqualified registration %s was accepted", name)
			}
			if name == "opened-root-mismatch" {
				if err.Error() != "snapshot root identity changed before registration" {
					t.Fatalf("opened-root fixture failed before identity comparison: %v", err)
				}
				for _, p := range []string{path, filepath.Join(other, markerName)} {
					if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("identity refusal created marker %s: %v", p, err)
					}
				}
			}
			if name == "encode-error" || name == "close-error" {
				want := error(sentinel)
				if name == "close-error" {
					want = os.ErrClosed
				}
				if !errors.Is(err, want) || markerFile == nil {
					t.Fatalf("registration lost real descriptor failure: %v", err)
				}
				if _, err := markerFile.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("registration leaked its marker descriptor: %v", err)
				}
				if name == "encode-error" && Reclaimable(dir) {
					t.Fatal("failed empty marker granted ownership evidence")
				}
			}
		})
	}
}

func TestSnapshotOwnerBoundedMarkerRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, markerName)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxMarkerBytes*2)), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := readMarker(path); err != nil || len(b) != maxMarkerBytes+1 {
		t.Fatalf("marker read is not bounded: bytes=%d err=%v", len(b), err)
	}
	if _, err := readMarker(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing marker read succeeded")
	}
	if _, err := readMarker(dir); err == nil {
		t.Fatal("directory marker read succeeded")
	}
	t.Run("closed-marker-stat", func(t *testing.T) {
		open := ownerOpenMarker
		t.Cleanup(func() { ownerOpenMarker = open })
		ownerOpenMarker = func(p string) (*os.File, error) {
			if p != path {
				t.Fatalf("unexpected marker path %q", p)
			}
			f, err := open(p)
			if err != nil {
				t.Fatalf("closed-descriptor fixture open: %v", err)
			}
			t.Cleanup(func() { _ = f.Close() })
			if err := f.Close(); err != nil {
				t.Fatalf("closed-descriptor fixture close: %v", err)
			}
			return f, nil
		}
		if b, err := readMarker(path); b != nil || !errors.Is(err, os.ErrClosed) {
			t.Fatalf("descriptor Stat failure became readable evidence: bytes=%d err=%v", len(b), err)
		}
	})
}

func TestSnapshotOwnerMarkerIdentityReplacement(t *testing.T) {
	stat, read, protected, dead := ownerLstat, ownerReadFile, ownerProtected, ownerProcessDead
	t.Cleanup(func() {
		ownerLstat, ownerReadFile, ownerProtected, ownerProcessDead = stat, read, protected, dead
	})
	dir, other := t.TempDir(), t.TempDir()
	path, replacement := filepath.Join(dir, markerName), filepath.Join(other, markerName)
	for _, p := range []string{path, replacement} {
		if err := os.WriteFile(p, []byte(`{"version":1,"pid":4242}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ownerProtected = func(string, fs.FileInfo) bool { return true }
	ownerReadFile = os.ReadFile
	ownerProcessDead = func(int) (bool, error) { return true, nil }
	markerStats := 0
	ownerLstat = func(p string) (fs.FileInfo, error) {
		if p == path {
			markerStats++
			if markerStats > 1 {
				return os.Lstat(replacement)
			}
		}
		return os.Lstat(p)
	}
	if Reclaimable(dir) {
		t.Fatal("replaced marker authorized reclamation")
	}
}
