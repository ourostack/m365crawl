package teamsdesktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/leveldb"
)

func TestSnapshotOwnershipMarkerLifetime(t *testing.T) {
	privateTempDir(t)
	var checked string
	check := func(dir string) {
		t.Helper()
		path := filepath.Join(dir, ".m365crawl-owner.json")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("snapshot lacks registered ownership before source copy: %v", err)
			return
		}
		var marker struct{ Version, PID int }
		if err := json.Unmarshal(b, &marker); err != nil || marker.Version != 1 || marker.PID <= 0 {
			t.Errorf("invalid snapshot ownership metadata: %s err=%v", b, err)
		}
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Errorf("ownership marker is not a regular private file: %v", err)
		}
	}
	old := onSnapshotAttempt
	onSnapshotAttempt = func(int) {
		paths, err := filepath.Glob(filepath.Join(os.TempDir(), snapshotPrefix+"*"))
		if err != nil || len(paths) != 1 {
			t.Errorf("cannot identify the owned registration root: %v err=%v", paths, err)
			return
		}
		checked = paths[0]
		check(checked)
	}
	t.Cleanup(func() { onSnapshotAttempt = old })
	snap, cleanup, err := Snapshot(context.Background(), fixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if checked != snap {
		t.Errorf("registration must precede the copy of exactly this snapshot: checked=%q snapshot=%q", checked, snap)
	}
	check(snap)
	cleanup()
	cleanup()
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Errorf("owned snapshot/marker survived its idempotent cleanup: %v", err)
	}
}

func TestSnapshotOwnershipRegistrationFailureCleanup(t *testing.T) {
	privateTempDir(t)
	oldRegister, oldAttempt := registerSnapshotOwner, onSnapshotAttempt
	t.Cleanup(func() { registerSnapshotOwner, onSnapshotAttempt = oldRegister, oldAttempt })
	var allocated string
	fault := errors.New("synthetic registration failure")
	registerSnapshotOwner = func(dir string) error {
		allocated = dir
		if err := os.WriteFile(filepath.Join(dir, ".m365crawl-owner.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		return fault
	}
	onSnapshotAttempt = func(int) { t.Fatal("registration failure reached source copy") }
	// This deliberately nonexistent source must never be opened.
	snap, cleanup, err := Snapshot(context.Background(), Source{LevelDBDir: filepath.Join(t.TempDir(), "absent")})
	if !errors.Is(err, fault) || snap != "" || allocated == "" || cleanup == nil {
		t.Fatalf("registration failure: snapshot=%q allocated=%q cleanup-nil=%t err=%v", snap, allocated, cleanup == nil, err)
	}
	cleanup()
	cleanup()
	if _, err := os.Lstat(allocated); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed registration leaked its private root/partial marker: %v", err)
	}
}

type sweepSymlinkInfo struct{ fs.FileInfo }

func (s sweepSymlinkInfo) Mode() fs.FileMode { return s.FileInfo.Mode() | fs.ModeSymlink }

func TestSnapshotOwnershipSweepGuards(t *testing.T) {
	for _, name := range []string{"unknown", "stat-error", "replaced", "symlink", "not-directory", "freshened", "remove-error", "positive-qualified"} {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			dir := filepath.Join(tmp, snapshotPrefix+"guard")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "owned")
			if err := os.WriteFile(payload, []byte("retained"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldTime := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(dir, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
			other := t.TempDir()
			file := filepath.Join(other, "regular")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			oldReclaim, oldStat, oldRemove := reclaimSnapshot, sweepSnapshotLstat, sweepSnapshotRemoveAll
			t.Cleanup(func() {
				reclaimSnapshot, sweepSnapshotLstat, sweepSnapshotRemoveAll = oldReclaim, oldStat, oldRemove
			})
			reclaimSnapshot = func(p string) bool {
				if p != dir {
					t.Fatalf("unexpected ownership path %s", p)
				}
				if name == "freshened" {
					now := time.Now()
					if err := os.Chtimes(dir, now, now); err != nil {
						t.Fatal(err)
					}
				}
				return name != "unknown"
			}
			sweepSnapshotLstat = func(p string) (fs.FileInfo, error) {
				switch name {
				case "stat-error":
					return nil, fs.ErrPermission
				case "replaced":
					return os.Lstat(other)
				case "not-directory":
					return os.Lstat(file)
				case "symlink":
					info, err := os.Lstat(p)
					return sweepSymlinkInfo{info}, err
				default:
					return os.Lstat(p)
				}
			}
			removes := 0
			sweepSnapshotRemoveAll = func(p string) error {
				removes++
				if p != dir {
					t.Fatalf("unexpected deletion path %s", p)
				}
				if name == "remove-error" {
					return fs.ErrPermission
				}
				return os.RemoveAll(p)
			}
			wantCount, wantCalls := 0, 0
			if name == "positive-qualified" {
				wantCount, wantCalls = 1, 1
			} else if name == "remove-error" {
				wantCalls = 1
			}
			if got := SweepStaleSnapshots(tmp, time.Hour); got != wantCount || removes != wantCalls {
				t.Fatalf("guard %s: removed=%d calls=%d, want %d/%d", name, got, removes, wantCount, wantCalls)
			}
			if name != "positive-qualified" {
				if b, err := os.ReadFile(payload); err != nil || string(b) != "retained" {
					t.Fatalf("guard %s lost owned payload: %q %v", name, b, err)
				}
			} else if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("positive removal did not remove its exact owned root: %v", err)
			}
		})
	}
}

func TestSnapshotFixtureOpens(t *testing.T) {
	snap, cleanup, err := Snapshot(context.Background(), fixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.HasPrefix(filepath.Base(snap), "m365crawl-snapshot-") {
		t.Fatalf("snapDir = %q", snap)
	}
	o, err := indexeddb.Open(filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = o.Close() }()
	dbs, err := o.Databases()
	if err != nil || len(dbs) != 19 {
		t.Fatalf("dbs=%d err=%v", len(dbs), err)
	}
	// Teams' lock and text log are not copied.
	for _, n := range []string{"LOCK", "LOG", "LOG.old"} {
		if _, err := os.Stat(filepath.Join(snap, "leveldb", n)); err == nil {
			t.Errorf("%s copied", n)
		}
	}
}

func TestSnapshotCleanup(t *testing.T) {
	snap, cleanup, err := Snapshot(context.Background(), fixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(snap)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		assertCurrentUserAndSystemOnly(t, snap)
		for _, sub := range []string{"leveldb", "blob"} {
			assertCurrentUserAndSystemOnly(t, filepath.Join(snap, sub))
		}
	} else {
		if st.Mode().Perm() != 0o700 {
			t.Fatalf("snapDir mode = %v", st.Mode().Perm())
		}
		for _, sub := range []string{"leveldb", "blob"} {
			if st, err := os.Stat(filepath.Join(snap, sub)); err != nil || st.Mode().Perm() != 0o700 {
				t.Fatalf("%s: %v %v", sub, st, err)
			}
		}
	}
	cleanup()
	if _, err := os.Stat(snap); !os.IsNotExist(err) {
		t.Fatalf("snapshot still present: %v", err)
	}
	cleanup() // idempotent
}

// privateTempDir points os.TempDir at a directory of this test's own, so counting snapshot
// directories is not disturbed by other packages' tests running at the same time.
func privateTempDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
}

func TestSnapshotRetryThenFail(t *testing.T) {
	privateTempDir(t)
	base := t.TempDir()
	s := Source{Profile: "p", Origin: "o", LevelDBDir: filepath.Join(base, "leveldb"), BlobDir: filepath.Join(base, "blob")}
	tabs := fakeLevelDB(t, s.LevelDBDir)
	if err := os.Remove(tabs[0]); err != nil { // manifest now names a missing table
		t.Fatal(err)
	}
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "m365crawl-snapshot-*"))

	attempts := 0
	old := onSnapshotAttempt
	onSnapshotAttempt = func(int) { attempts++ }
	t.Cleanup(func() { onSnapshotAttempt = old })

	snap, cleanup, err := Snapshot(context.Background(), s)
	if snap != "" || cleanup == nil {
		t.Fatalf("snap=%q cleanup nil=%v", snap, cleanup == nil)
	}
	cleanup()
	c := codeOf(t, err)
	if c.Code != errs.CodeSnapshotInconsistent || c.Exit != 1 {
		t.Fatalf("err = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "m365crawl-snapshot-*"))
	if len(after) != len(before) {
		t.Fatalf("snapshot dirs leaked: before %d after %d", len(before), len(after))
	}
}

func TestSnapshotRetryThenSucceed(t *testing.T) {
	base := t.TempDir()
	s := Source{Profile: "p", Origin: "o", LevelDBDir: filepath.Join(base, "leveldb"), BlobDir: filepath.Join(base, "blob")}
	tabs := fakeLevelDB(t, s.LevelDBDir)
	hidden := tabs[0] + ".hidden"
	if err := os.Rename(tabs[0], hidden); err != nil {
		t.Fatal(err)
	}
	old := onSnapshotAttempt
	onSnapshotAttempt = func(n int) {
		if n == 2 { // Teams finishes its write between attempts
			_ = os.Rename(hidden, tabs[0])
		}
	}
	t.Cleanup(func() { onSnapshotAttempt = old })
	snap, cleanup, err := Snapshot(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	db, err := leveldb.Load(filepath.Join(snap, "leveldb"))
	if db != nil {
		defer func() { _ = db.Close() }()
	}
	if err != nil || db.Stats().Keys != 50 {
		t.Fatalf("keys=%v err=%v", db, err)
	}
}

func TestSnapshotCurrentChangedRetries(t *testing.T) {
	base := t.TempDir()
	s := Source{Profile: "p", Origin: "o", LevelDBDir: filepath.Join(base, "leveldb")}
	fakeLevelDB(t, s.LevelDBDir)
	cur := filepath.Join(s.LevelDBDir, "CURRENT")
	orig, _ := os.ReadFile(cur) //nolint:gosec // test temp dir
	attempts := 0
	oldCopy, oldAttempt := afterCopy, onSnapshotAttempt
	afterCopy = func(n int) {
		attempts = n
		if n == 1 { // CURRENT flips mid-copy on the first attempt only
			_ = os.WriteFile(cur, []byte("MANIFEST-999999\n"), 0o600)
		}
	}
	onSnapshotAttempt = func(n int) {
		if n == 2 { // Teams has settled again
			_ = os.WriteFile(cur, orig, 0o600) //nolint:gosec // test temp dir
		}
	}
	t.Cleanup(func() { afterCopy, onSnapshotAttempt = oldCopy, oldAttempt })
	_, cleanup, err := Snapshot(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestSnapshotCancel(t *testing.T) {
	privateTempDir(t)
	// Cancelled before the copy: error, nothing left behind.
	before, _ := filepath.Glob(filepath.Join(os.TempDir(), "m365crawl-snapshot-*"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap, cleanup, err := Snapshot(ctx, fixtureSource(t))
	if err == nil || snap != "" {
		t.Fatalf("snap=%q err=%v", snap, err)
	}
	cleanup()
	after, _ := filepath.Glob(filepath.Join(os.TempDir(), "m365crawl-snapshot-*"))
	if len(after) != len(before) {
		t.Fatalf("leaked on pre-cancel: %d -> %d", len(before), len(after))
	}

	// Cancelled after a successful snapshot: it removes itself.
	ctx2, cancel2 := context.WithCancel(context.Background())
	snap, cleanup, err = Snapshot(ctx2, fixtureSource(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cancel2()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(snap); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot not removed after cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSweepStaleSnapshots(t *testing.T) {
	tmp := t.TempDir()
	old := filepath.Join(tmp, "m365crawl-snapshot-old")
	fresh := filepath.Join(tmp, "m365crawl-snapshot-fresh")
	other := filepath.Join(tmp, "unrelated-old")
	for _, d := range []string{old, fresh, other} {
		if err := os.MkdirAll(filepath.Join(d, "leveldb"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ago := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{old, other} {
		_ = os.Chtimes(d, ago, ago)
	}
	if n := SweepStaleSnapshots(tmp, 24*time.Hour); n != 0 {
		t.Fatalf("swept %d unknown roots, want 0", n)
	}
	for _, d := range []string{old, fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s removed", d)
		}
	}
}

// manifestSource is a fake LevelDB source and the path of its live MANIFEST.
func manifestSource(t *testing.T) (Source, string) {
	t.Helper()
	base := t.TempDir()
	s := Source{Profile: "p", Origin: "o", LevelDBDir: filepath.Join(base, "leveldb")}
	fakeLevelDB(t, s.LevelDBDir)
	cur, err := os.ReadFile(filepath.Join(s.LevelDBDir, "CURRENT")) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return s, filepath.Join(s.LevelDBDir, strings.TrimSpace(string(cur)))
}

func TestSnapshotManifestSizeMismatchRetries(t *testing.T) {
	s, manifest := manifestSource(t)
	orig, _ := os.ReadFile(manifest) //nolint:gosec // test temp dir
	attempts := 0
	oldCopy, oldAttempt := afterCopy, onSnapshotAttempt
	afterCopy = func(n int) {
		attempts = n
		if n == 1 { // Teams appends to the MANIFEST after we copied it
			f, _ := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test temp dir
			_, _ = f.Write([]byte("more"))
			_ = f.Close()
		}
	}
	onSnapshotAttempt = func(n int) {
		if n == 2 {
			_ = os.WriteFile(manifest, orig, 0o600) //nolint:gosec // test temp dir
		}
	}
	t.Cleanup(func() { afterCopy, onSnapshotAttempt = oldCopy, oldAttempt })
	_, cleanup, err := Snapshot(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestSnapshotManifestSizeMismatchFails(t *testing.T) {
	s, manifest := manifestSource(t)
	attempts := 0
	oldCopy := afterCopy
	afterCopy = func(n int) {
		attempts = n
		f, _ := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // test temp dir
		_, _ = f.Write([]byte("x"))
		_ = f.Close()
	}
	t.Cleanup(func() { afterCopy = oldCopy })
	snap, cleanup, err := Snapshot(context.Background(), s)
	cleanup()
	if snap != "" || codeOf(t, err).Code != errs.CodeSnapshotInconsistent || attempts != 3 {
		t.Fatalf("snap=%q attempts=%d err=%v", snap, attempts, err)
	}
	if !strings.Contains(err.Error(), "MANIFEST") {
		t.Fatalf("error should name the MANIFEST: %v", err)
	}
}
