package snapshotowner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type ownerSymlinkInfo struct{ fs.FileInfo }

func (i ownerSymlinkInfo) Mode() fs.FileMode { return i.FileInfo.Mode() | fs.ModeSymlink }

func TestSnapshotOwnerConservativeLiveness(t *testing.T) {
	oldPID, oldDead := ownerPID, ownerProcessDead
	oldStat, oldRead, oldProtected := ownerLstat, ownerReadFile, ownerProtected
	t.Cleanup(func() {
		ownerPID, ownerProcessDead = oldPID, oldDead
		ownerLstat, ownerReadFile, ownerProtected = oldStat, oldRead, oldProtected
	})
	ownerPID = func() int { return 4242 }
	ownerProcessDead = func(int) (bool, error) {
		t.Error("registration must not query a process")
		return false, errors.New("unexpected registration process query")
	}
	ownerProtected = func(string, fs.FileInfo) bool { return true }
	t.Run("exclusive-registration", func(t *testing.T) {
		dir := t.TempDir()
		if err := Register(dir); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, ".m365crawl-owner.json")
		b, err := os.ReadFile(path) //nolint:gosec // G304: exact marker in this test's newly allocated private t.TempDir
		if err != nil {
			t.Fatal(err)
		}
		var marker struct{ Version, PID int }
		if err := json.Unmarshal(b, &marker); err != nil || marker.Version != 1 || marker.PID != 4242 {
			t.Fatalf("registration did not bind the declared owner: %s err=%v", b, err)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			t.Fatalf("owner marker must be regular: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Errorf("owner marker is not protected: %v", info.Mode())
		}
		if err := Register(dir); err == nil {
			t.Error("registration overwrote an existing owner marker")
		}
		after, err := os.ReadFile(path) //nolint:gosec // G304: same owned marker, checking exclusive registration preserved its bytes
		if err != nil || string(after) != string(b) {
			t.Errorf("failed exclusive registration changed ownership evidence: %v", err)
		}
	})
	for _, tc := range []struct {
		name, marker                   string
		dead, want                     bool
		processErr                     error
		missing, symlink               bool
		readDenied, statDenied         bool
		unprotected, replaced          bool
		markerUnprotected, rootSymlink bool
	}{
		{name: "positive-dead", marker: `{"version":1,"pid":4242}`, dead: true, want: true},
		{name: "alive", marker: `{"version":1,"pid":4242}`},
		{name: "pid-reused-is-live", marker: `{"version":1,"pid":4242}`},
		{name: "access-denied", marker: `{"version":1,"pid":4242}`, processErr: fs.ErrPermission},
		{name: "unknown-process-result", marker: `{"version":1,"pid":4242}`, processErr: errors.New("unknown")},
		{name: "invalid-version", marker: `{"version":2,"pid":4242}`, dead: true},
		{name: "zero-pid", marker: `{"version":1,"pid":0}`, dead: true},
		{name: "negative-pid", marker: `{"version":1,"pid":-1}`, dead: true},
		{name: "overflow-pid", marker: `{"version":1,"pid":2147483648}`, dead: true},
		{name: "oversize-marker", marker: strings.Repeat(" ", maxMarkerBytes+1), dead: true},
		{name: "malformed", marker: `{`, dead: true},
		{name: "missing-marker", missing: true, dead: true},
		{name: "symlink-marker", marker: `{"version":1,"pid":4242}`, symlink: true, dead: true},
		{name: "read-denied", marker: `{"version":1,"pid":4242}`, readDenied: true, dead: true},
		{name: "stat-denied", marker: `{"version":1,"pid":4242}`, statDenied: true, dead: true},
		{name: "foreign-or-unprotected", marker: `{"version":1,"pid":4242}`, unprotected: true, dead: true},
		{name: "foreign-or-unprotected-marker", marker: `{"version":1,"pid":4242}`, markerUnprotected: true, dead: true},
		{name: "symlink-root", marker: `{"version":1,"pid":4242}`, rootSymlink: true, dead: true},
		{name: "root-identity-replaced", marker: `{"version":1,"pid":4242}`, replaced: true, dead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, other := t.TempDir(), t.TempDir()
			path := filepath.Join(dir, ".m365crawl-owner.json")
			if !tc.missing {
				if err := os.WriteFile(path, []byte(tc.marker), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ownerProtected = func(p string, _ fs.FileInfo) bool {
				return !tc.unprotected && (!tc.markerUnprotected || p != path)
			}
			ownerReadFile = func(p string) ([]byte, error) {
				if p != path {
					t.Fatalf("owner reader escaped the exact owned marker: %q", p)
				}
				if tc.readDenied {
					return nil, fs.ErrPermission
				}
				return os.ReadFile(p) //nolint:gosec // G304: p is checked against the exact marker written above in this subtest's private t.TempDir
			}
			stats := 0
			ownerLstat = func(p string) (fs.FileInfo, error) {
				if tc.statDenied {
					return nil, fs.ErrPermission
				}
				if p == dir {
					stats++
					if tc.rootSymlink {
						info, err := os.Lstat(p)
						if err != nil {
							return nil, err
						}
						return ownerSymlinkInfo{info}, nil
					}
					if tc.replaced && stats > 1 {
						return os.Lstat(other)
					}
				}
				if p == path && tc.symlink {
					info, err := os.Lstat(p)
					if err != nil {
						return nil, err
					}
					return ownerSymlinkInfo{info}, nil
				}
				return os.Lstat(p)
			}
			ownerProcessDead = func(pid int) (bool, error) {
				if pid != 4242 {
					t.Errorf("queried unqualified pid %d", pid)
					return false, errors.New("unqualified pid must never be checked")
				}
				return tc.dead, tc.processErr
			}
			if got := Reclaimable(dir); got != tc.want {
				t.Errorf("owner evidence %s authorized reclamation=%t, want %t", tc.name, got, tc.want)
			}
		})
	}
}

func TestSnapshotOwnerReadMarkerNonRegularFailure(t *testing.T) {
	open := ownerOpenMarker
	t.Cleanup(func() { ownerOpenMarker = open })
	dir := t.TempDir()
	path := filepath.Join(dir, markerName)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(path, "unchanged")
	const payload = "owned non-regular marker fixture"
	if err := os.WriteFile(child, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: exact directory just created as the marker in this test's private t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if info, err := f.Stat(); err != nil || !info.IsDir() {
		t.Fatalf("non-regular owned fixture not qualified: %v", err)
	}
	ownerOpenMarker = func(p string) (*os.File, error) {
		if p != path {
			t.Fatalf("reader escaped the exact owned marker: %q", p)
		}
		return f, nil
	}
	b, err := readMarker(path)
	var ioErr *fs.PathError
	if b != nil || err == nil || errors.As(err, &ioErr) {
		t.Fatalf("non-regular marker needs policy refusal, not an attempted I/O failure: bytes=%q err=%v", b, err)
	}
	if n, err := f.Write([]byte("x")); n != 0 || !errors.Is(err, os.ErrClosed) {
		t.Fatalf("non-regular marker refusal did not close its file: wrote=%d err=%v", n, err)
	}
	after, err := os.ReadFile(child) //nolint:gosec // G304: exact child written in the owned non-regular marker directory above
	if err != nil || string(after) != payload {
		t.Fatalf("non-regular refusal changed owned fixture contents: %q %v", after, err)
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 1 || entries[0].Name() != "unchanged" {
		t.Fatalf("non-regular refusal changed its owned directory: %v %v", entries, err)
	}
}
