package outlookdesktop

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// swap replaces a package variable for one test.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// quiet clears the hooks every test starts from.
func quiet(t *testing.T) *[]time.Duration {
	swap(t, &onAttempt, func(int) {})
	swap(t, &afterCopy, func(int) {})
	var waits []time.Duration
	swap(t, &wait, func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	})
	return &waits
}

func TestSnapshotCopiesOnlyTheStore(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	src := t.TempDir()
	store := fakeProfile(t, src, "Main Profile", 5000)
	dir := filepath.Dir(store)
	lockBefore, _ := os.Stat(filepath.Join(dir, "HxStore.lock"))

	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info.Attempts != 1 || info.Size != 5000 || filepath.Base(info.Path) != StoreFileName {
		t.Fatalf("info = %+v", info)
	}
	got, err := os.ReadFile(info.Path)
	if err != nil || !bytes.Equal(got, syntheticStore(5000)) {
		t.Fatalf("copy differs from source: %v", err)
	}
	if names := entries(t, filepath.Dir(info.Path)); len(names) != 1 {
		t.Fatalf("snapshot directory holds %v, want only the store", names)
	}
	if len(entries(t, tmp)) != 1 {
		t.Fatalf("temp holds %v", entries(t, tmp))
	}
	srcInfo, _ := os.Stat(store)
	if !info.ModTime.Equal(srcInfo.ModTime()) {
		t.Fatal("ModTime is not the source's")
	}
	// The sibling files are untouched.
	lockAfter, _ := os.Stat(filepath.Join(dir, "HxStore.lock"))
	if !lockAfter.ModTime().Equal(lockBefore.ModTime()) || lockAfter.Size() != lockBefore.Size() {
		t.Fatal("lock file changed")
	}
}

func TestSnapshotModeAndCleanup(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)

	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		di, _ := os.Stat(filepath.Dir(info.Path))
		fi, _ := os.Stat(info.Path)
		if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
			t.Errorf("modes: dir %v file %v", di.Mode().Perm(), fi.Mode().Perm())
		}
	}
	cleanup()
	cleanup() // idempotent
	assertClean(t, tmp)
}

func TestSnapshotRemovedWhenContextCancelled(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	ctx, cancel := context.WithCancel(context.Background())
	_, cleanup, err := Snapshot(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for len(entries(t, tmp)) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assertClean(t, tmp)
}

func TestSnapshotCleansUpWhenCancelledFirst(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := Snapshot(ctx, store)
	if !errors.Is(err, context.Canceled) || cleanup == nil {
		t.Fatalf("err=%v", err)
	}
	cleanup()
	assertClean(t, tmp)
}

func TestSnapshotCleansUpWhenCancelledDuringCopy(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	ctx, cancel := context.WithCancel(context.Background())
	swap(t, &onAttempt, func(int) { cancel() })
	_, _, err := Snapshot(ctx, store)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	assertClean(t, tmp)
}

func TestSnapshotRetriesOnSizeChange(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	swap(t, &afterCopy, func(attempt int) {
		if attempt == 1 {
			mustWrite(t, store, syntheticStore(1500))
		}
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if info.Attempts != 2 || info.Size != 1500 {
		t.Fatalf("info = %+v", info)
	}
	got, _ := os.ReadFile(info.Path)
	if !bytes.Equal(got, syntheticStore(1500)) {
		t.Fatal("the accepted copy is not the new content")
	}
	cleanup()
	assertClean(t, tmp)
}

func TestSnapshotRetriesOnModTimeChange(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	swap(t, &afterCopy, func(attempt int) {
		if attempt == 1 {
			setMtime(t, store, 3*time.Second)
		}
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if info.Attempts != 2 {
		t.Fatalf("attempts = %d", info.Attempts)
	}
}

func TestSnapshotRetriesOnInodeChange(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	before, _ := os.Stat(store)
	swap(t, &afterCopy, func(attempt int) {
		if attempt != 1 {
			return
		}
		// Outlook replaces the file with a new one of the same size and time.
		repl := store + ".new"
		mustWrite(t, repl, bytes.Repeat([]byte("Z"), 1000))
		if err := os.Chtimes(repl, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(repl, store); err != nil {
			t.Fatal(err)
		}
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ := os.ReadFile(info.Path)
	if info.Attempts != 2 || !bytes.Equal(got, bytes.Repeat([]byte("Z"), 1000)) {
		t.Fatalf("attempts=%d, copy is not the replacement", info.Attempts)
	}
}

// sizeInfo and timeInfo make the open handle report something other than the file's stat.
type sizeInfo struct {
	fs.FileInfo
	size int64
}

func (s sizeInfo) Size() int64 { return s.size }

type timeInfo struct {
	fs.FileInfo
	mod time.Time
}

func (s timeInfo) ModTime() time.Time { return s.mod }

func TestSnapshotRetriesWhenHandleDisagreesWithStat(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	for name, wrap := range map[string]func(fs.FileInfo) fs.FileInfo{
		"size": func(i fs.FileInfo) fs.FileInfo { return sizeInfo{i, i.Size() + 1} },
		"time": func(i fs.FileInfo) fs.FileInfo { return timeInfo{i, i.ModTime().Add(time.Second)} },
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			swap(t, &sameFile, func(a, b fs.FileInfo) bool { return true }) // isolate the size and time checks
			swap(t, &statOpen, func(f *os.File) (fs.FileInfo, error) {
				calls++
				i, err := f.Stat()
				if calls == 1 {
					return wrap(i), err
				}
				return i, err
			})
			info, cleanup, err := Snapshot(context.Background(), store)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if info.Attempts != 2 {
				t.Fatalf("attempts = %d", info.Attempts)
			}
		})
	}
}

func TestSnapshotRejectsShortCopy(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	st, _ := os.Stat(store)
	calls := 0
	// The file shrinks after it is opened and grows back, with its time restored, before the
	// final stat: only the byte count shows the copy is short.
	swap(t, &statOpen, func(f *os.File) (fs.FileInfo, error) {
		calls++
		i, err := f.Stat()
		if calls == 1 {
			if terr := os.Truncate(store, 400); terr != nil {
				t.Fatal(terr)
			}
		}
		return i, err
	})
	swap(t, &afterCopy, func(attempt int) {
		if attempt == 1 {
			mustWrite(t, store, syntheticStore(1000))
			if err := os.Chtimes(store, st.ModTime(), st.ModTime()); err != nil {
				t.Fatal(err)
			}
		}
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, _ := os.ReadFile(info.Path)
	if info.Attempts != 2 || len(got) != 1000 {
		t.Fatalf("attempts=%d len=%d", info.Attempts, len(got))
	}
}

func TestSnapshotStoreBusyAfterAllAttempts(t *testing.T) {
	waits := quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	size := 1000
	attempts := 0
	swap(t, &onAttempt, func(int) { attempts++ })
	swap(t, &afterCopy, func(int) {
		size++
		mustWrite(t, store, syntheticStore(size))
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	var busy *StoreBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("err = %v", err)
	}
	if busy.Attempts != SnapshotAttempts || attempts != SnapshotAttempts || busy.Path != store || busy.Last == "" {
		t.Fatalf("busy = %+v after %d attempts", busy, attempts)
	}
	if info != (Info{}) || cleanup == nil {
		t.Fatal("a busy store must return no copy and a usable cleanup")
	}
	cleanup()
	if codeOf(t, err) != errs.CodeSnapshotInconsistent {
		t.Fatal("StoreBusyError must unwrap to snapshot_inconsistent")
	}
	if busy.Error() == "" {
		t.Fatal("empty message")
	}
	// A settle pause after every copy, then 1 s and 2 s between attempts and none after the last.
	want := []time.Duration{settle, time.Second, settle, 2 * time.Second, settle}
	if !reflect.DeepEqual(*waits, want) {
		t.Fatalf("waits = %v, want %v", *waits, want)
	}
	assertClean(t, tmp)
}

func TestSnapshotOpensReadOnlyAndNeverTouchesTheLock(t *testing.T) {
	quiet(t)
	tempHome(t)
	srcRoot := t.TempDir()
	store := fakeProfile(t, srcRoot, "Main Profile", 100)

	type open struct {
		path string
		flag int
		perm os.FileMode
	}
	var mu sync.Mutex
	var opens []open
	var stats []string
	swap(t, &openFile, func(name string, flag int, perm os.FileMode) (*os.File, error) {
		mu.Lock()
		opens = append(opens, open{name, flag, perm})
		mu.Unlock()
		return os.OpenFile(name, flag, perm) //nolint:gosec // test wrapper over the seam; the name comes from the code under test
	})
	swap(t, &statSource, func(name string) (fs.FileInfo, error) {
		mu.Lock()
		stats = append(stats, name)
		mu.Unlock()
		return os.Stat(name)
	})
	_, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(opens) != 1 || opens[0].path != store || opens[0].flag != sourceOpenFlags || opens[0].perm != 0 {
		t.Fatalf("opens = %+v, want exactly one read-only open of the store", opens)
	}
	if opens[0].flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		t.Fatalf("flag %#x contains a write bit", opens[0].flag)
	}
	for _, p := range stats {
		if p != store {
			t.Errorf("stat of %s, want only the store", p)
		}
	}
	if len(stats) != 2 {
		t.Errorf("stats = %d, want one before and one after", len(stats))
	}
}

func TestSnapshotInsufficientSpace(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	swap(t, &freeBytes, func(string) (uint64, bool, error) { return 1000 + spaceHeadroom - 1, true, nil })
	_, cleanup, err := Snapshot(context.Background(), store)
	var sp *InsufficientSpaceError
	if !errors.As(err, &sp) || sp.Need != 1000+spaceHeadroom || sp.Avail != 1000+spaceHeadroom-1 || sp.Error() == "" {
		t.Fatalf("err = %v", err)
	}
	var c *errs.Coded
	if !errors.As(err, &c) || c.Fix == "" || c.Exit != errs.ExitRuntime {
		t.Fatalf("not coded with a remedy: %v", err)
	}
	cleanup()
	assertClean(t, tmp)

	// Exactly enough is enough.
	swap(t, &freeBytes, func(string) (uint64, bool, error) { return 1000 + spaceHeadroom, true, nil })
	_, cleanup, err = Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}

func TestSnapshotSpaceUnmeasuredProceeds(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	swap(t, &freeBytes, func(string) (uint64, bool, error) { return 0, false, nil })
	_, cleanup, err := Snapshot(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
}

func TestSnapshotErrorsCleanUp(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name string
		code string // expected errs code, or "" for a sentinel check
		sent error
		set  func(t *testing.T, store string)
	}{
		{name: "source missing", sent: ErrStoreNotFound, set: func(t *testing.T, store string) {
			if err := os.Remove(store); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "stat denied", code: errs.CodeNoFullDiskAccess, set: func(t *testing.T, _ string) {
			swap(t, &statSource, func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission })
		}},
		{name: "not a regular file", code: errs.CodeInternal, set: func(t *testing.T, store string) {
			if err := os.Remove(store); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(store, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "free space query fails", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &freeBytes, func(string) (uint64, bool, error) { return 0, false, boom })
		}},
		{name: "stale copy cannot be removed", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &removeFile, func(string) error { return boom })
		}},
		{name: "open denied", code: errs.CodeNoFullDiskAccess, set: func(t *testing.T, _ string) {
			swap(t, &openFile, func(string, int, os.FileMode) (*os.File, error) { return nil, fs.ErrPermission })
		}},
		{name: "open vanished", sent: ErrStoreNotFound, set: func(t *testing.T, _ string) {
			swap(t, &openFile, func(string, int, os.FileMode) (*os.File, error) { return nil, fs.ErrNotExist })
		}},
		{name: "handle stat fails", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &statOpen, func(*os.File) (fs.FileInfo, error) { return nil, boom })
		}},
		{name: "create copy fails", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &createFile, func(string) (io.WriteCloser, error) { return nil, boom })
		}},
		{name: "write fails", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &createFile, func(string) (io.WriteCloser, error) { return &fakeOut{writeErr: boom}, nil })
		}},
		{name: "write denied", code: errs.CodeNoFullDiskAccess, set: func(t *testing.T, _ string) {
			swap(t, &createFile, func(string) (io.WriteCloser, error) { return &fakeOut{writeErr: fs.ErrPermission}, nil })
		}},
		{name: "close fails", code: errs.CodeInternal, set: func(t *testing.T, _ string) {
			swap(t, &createFile, func(string) (io.WriteCloser, error) { return &fakeOut{closeErr: boom}, nil })
		}},
		{name: "final stat fails", code: errs.CodeInternal, set: func(t *testing.T, store string) {
			calls := 0
			swap(t, &statSource, func(p string) (fs.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, boom
				}
				return os.Stat(p)
			})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			quiet(t)
			tmp := tempHome(t)
			store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
			c.set(t, store)
			info, cleanup, err := Snapshot(context.Background(), store)
			if err == nil || info != (Info{}) || cleanup == nil {
				t.Fatalf("err=%v info=%+v", err, info)
			}
			switch {
			case c.sent != nil && !errors.Is(err, c.sent):
				t.Fatalf("want %v, got %v", c.sent, err)
			case c.code != "" && codeOf(t, err) != c.code:
				t.Fatalf("want code %s, got %v", c.code, err)
			}
			cleanup()
			assertClean(t, tmp)
		})
	}
}

type fakeOut struct {
	writeErr, closeErr error
}

func (f *fakeOut) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return len(p), nil
}
func (f *fakeOut) Close() error { return f.closeErr }

func TestSnapshotTempDirFailure(t *testing.T) {
	quiet(t)
	d := t.TempDir()
	missing := filepath.Join(d, "missing")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	_, cleanup, err := Snapshot(context.Background(), store)
	if codeOf(t, err) != errs.CodeInternal || cleanup == nil {
		t.Fatalf("err=%v", err)
	}
	cleanup()
}

func TestCopyStream(t *testing.T) {
	ctx := context.Background()
	// Data and EOF in one read.
	var buf bytes.Buffer
	n, err := copyStream(ctx, &buf, iotest.DataErrReader(bytes.NewReader([]byte("abc"))))
	if err != nil || n != 3 || buf.String() != "abc" {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// Several chunks.
	swap(t, &copyBufBytes, 2)
	buf.Reset()
	if n, err = copyStream(ctx, &buf, bytes.NewReader([]byte("abcde"))); err != nil || n != 5 || buf.String() != "abcde" {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// Read error.
	boom := errors.New("boom")
	if _, err = copyStream(ctx, &buf, iotest.ErrReader(boom)); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	// Write error.
	if _, err = copyStream(ctx, &fakeOut{writeErr: boom}, bytes.NewReader([]byte("abc"))); !errors.Is(err, boom) {
		t.Fatalf("err=%v", err)
	}
	// Cancelled.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = copyStream(cctx, &buf, bytes.NewReader([]byte("abc"))); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	// Deadline exceeded passes through mapCopyError unchanged.
	if err := mapCopyError("p", context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestSnapshotErrorsNeverCarryContent(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	size := 1000
	swap(t, &afterCopy, func(int) { size++; mustWrite(t, store, syntheticStore(size)) })
	_, _, err := Snapshot(context.Background(), store)
	if err == nil || bytes.Contains([]byte(err.Error()), []byte("FIXTURE")) {
		t.Fatalf("err = %v", err)
	}
}

func TestTeamsSweepRemovesOutlookSnapshots(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	info, _, err := Snapshot(context.Background(), store) // cleanup deliberately not called: a killed process
	if err != nil {
		t.Fatal(err)
	}
	if got := teamsdesktop.SweepStaleSnapshots(tmp, time.Hour); got != 0 {
		t.Fatalf("a fresh snapshot was swept (%d)", got)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Dir(info.Path), old, old); err != nil {
		t.Fatal(err)
	}
	if got := teamsdesktop.SweepStaleSnapshots(tmp, time.Hour); got != 1 {
		t.Fatalf("swept %d, want 1", got)
	}
	assertClean(t, tmp)
}

func TestSnapshotRetriesWhenChangedDuringSettle(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	first := true
	// The change lands in the settle pause, after the copy and before the final stat.
	swap(t, &wait, func(ctx context.Context, d time.Duration) error {
		if d == settle && first {
			first = false
			mustWrite(t, store, syntheticStore(1200))
		}
		return ctx.Err()
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil || info.Attempts != 2 || info.Size != 1200 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	cleanup()
	assertClean(t, tmp)
}

func TestSnapshotRetriesWhenSwappedBetweenStatAndOpen(t *testing.T) {
	quiet(t)
	tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	other := filepath.Join(t.TempDir(), "other")
	mustWrite(t, other, syntheticStore(1000))
	calls := 0
	swap(t, &statOpen, func(f *os.File) (fs.FileInfo, error) {
		calls++
		if calls == 1 {
			return os.Stat(other) // the handle is a different file from the one stat'ed
		}
		return f.Stat()
	})
	info, cleanup, err := Snapshot(context.Background(), store)
	if err != nil || info.Attempts != 2 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	cleanup()
}

func TestSnapshotRejectsIrregularHandle(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	swap(t, &statOpen, func(*os.File) (fs.FileInfo, error) { return os.Stat(t.TempDir()) })
	_, cleanup, err := Snapshot(context.Background(), store)
	if codeOf(t, err) != errs.CodeInternal {
		t.Fatalf("err=%v", err)
	}
	cleanup()
	assertClean(t, tmp)
}

func TestSnapshotCancelledBetweenAttempts(t *testing.T) {
	tmp := tempHome(t)
	quiet(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 1000)
	ctx, cancel := context.WithCancel(context.Background())
	size := 1000
	swap(t, &afterCopy, func(int) { size++; mustWrite(t, store, syntheticStore(size)) })
	swap(t, &wait, func(c context.Context, d time.Duration) error {
		if d == time.Second {
			cancel()
		}
		return c.Err()
	})
	_, cleanup, err := Snapshot(ctx, store)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	cleanup()
	assertClean(t, tmp)
}

func TestSnapshotCancelledDuringSettle(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	ctx, cancel := context.WithCancel(context.Background())
	swap(t, &wait, func(c context.Context, d time.Duration) error { cancel(); return c.Err() })
	_, _, err := Snapshot(ctx, store)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	assertClean(t, tmp)
}

func TestSnapshotPanicDoesNotLeakTheCopy(t *testing.T) {
	quiet(t)
	tmp := tempHome(t)
	store := fakeProfile(t, t.TempDir(), "Main Profile", 100)
	swap(t, &afterCopy, func(int) { panic("boom") })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_, _, _ = Snapshot(context.Background(), store)
	}()
	assertClean(t, tmp)
}

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestDefaultTimings(t *testing.T) {
	if settle != 250*time.Millisecond || retryWaits != [SnapshotAttempts - 1]time.Duration{time.Second, 2 * time.Second} {
		t.Fatalf("settle=%v retryWaits=%v", settle, retryWaits)
	}
}
