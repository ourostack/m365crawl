package onenote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCaptureSQLiteChild(t *testing.T) {
	mode := os.Getenv("M365CRAWL_SQLITE_LOCK_CHILD")
	if mode != "1" && mode != "hold" {
		return
	}
	path, err := filepath.Abs("index.db")
	if err != nil {
		t.Fatal(err)
	}
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/"), RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if mode == "hold" {
		if _, err := db.Exec("BEGIN EXCLUSIVE"); err != nil {
			t.Fatal(err)
		}
		fmt.Println("EXCLUSIVE_READY")
		var release [1]byte
		if _, err := os.Stdin.Read(release[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("ROLLBACK"); err != nil {
			t.Fatal(err)
		}
		return
	}
	if _, err := db.Exec("PRAGMA busy_timeout=25"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM Entities").Scan(&count); err != nil || count != 4 {
		t.Fatalf("reader denied: %d, %v", count, err)
	}
	fmt.Println("READ_OK")
	if _, err := db.Exec("UPDATE Entities SET Title='changed' WHERE rowid=20"); err != nil {
		if !strings.Contains(err.Error(), "SQLITE_BUSY") {
			t.Fatalf("not SQLite write contention: %v", err)
		}
		fmt.Println("WRITE_BLOCKED")
		return
	}
	fmt.Println("WRITE_OK")
}

func sqliteWriterProbe(t *testing.T, path string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCaptureSQLiteChild$", "-test.count=1") //nolint:gosec // reexecutes only the checked current test executable, with literal arguments
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(), "M365CRAWL_SQLITE_LOCK_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQLite child: %v\n%s", err, output)
	}
	return string(output)
}

func TestCaptureIndexRefusesActualExclusiveSQLiteWriter(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestCaptureSQLiteChild$", "-test.count=1") //nolint:gosec // reexecutes only the checked current test executable, with literal arguments
	cmd.Dir = filepath.Dir(path)
	cmd.Env = append(os.Environ(), "M365CRAWL_SQLITE_LOCK_CHILD=hold")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		_ = input.Close()
		_ = cmd.Wait()
	}()
	ready := bufio.NewScanner(output)
	if !ready.Scan() || ready.Text() != "EXCLUSIVE_READY" {
		t.Fatal("SQLite writer failed readiness protocol")
	}
	if capture, err := CaptureIndex(context.Background(), path); err == nil || capture != nil {
		t.Fatal("exclusive SQLite writer did not prevent capture")
	}
	if _, err := input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	for ready.Scan() {
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatalf("writer release did not restore capture: %v", err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureIndexSQLiteReadersAndWriterRecovery(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	if output := sqliteWriterProbe(t, path); !strings.Contains(output, "READ_OK\nWRITE_BLOCKED\n") {
		t.Fatalf("held capture failed to protect against SQLite writer: %s", output)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if output := sqliteWriterProbe(t, path); !strings.Contains(output, "READ_OK\nWRITE_OK\n") {
		t.Fatalf("writer did not recover: %s", output)
	}
}

func TestCaptureIndexPrivateEqualCopy(t *testing.T) {
	path := indexFixture(t, "index.db", hierarchyFixture)
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := ObserveIndex(path)
	if err != nil || signature == "" {
		t.Fatalf("observation: %q, %v", signature, err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	copyPath := capture.Path
	if copyPath == path || capture.SourceModified.IsZero() {
		t.Fatal("copy aliases source or loses observed time")
	}
	copied, err := os.ReadFile(copyPath) //nolint:gosec // reads the copy created by CaptureIndex in its owned private directory
	if err != nil || !bytes.Equal(before, copied) {
		t.Fatalf("private copy mismatch: %v", err)
	}
	if capture.ContentDigest != fmt.Sprintf("%x", sha256.Sum256(before)) ||
		capture.AcquisitionSignature != signature || capture.ContentDigest == signature {
		t.Fatal("content digest and metadata signature must be separate")
	}
	got, err := ReadIndex(context.Background(), copyPath)
	if err != nil || len(got.Entities) != 4 || len(got.Elements) != 2 {
		t.Fatalf("private reader: %#v, %v", got, err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatalf("repeat close: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(copyPath)); !os.IsNotExist(err) {
		t.Fatalf("owned copy directory survives close: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("source changed: %v", err)
	}
}

func TestCaptureIndexRefusesUnsafeInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(t *testing.T, path string) string
	}{
		{"missing", func(t *testing.T, path string) string { return path + ".missing" }},
		{"directory", func(t *testing.T, path string) string { return filepath.Dir(path) }},
		{"journal", func(t *testing.T, path string) string {
			if err := os.WriteFile(path+"-journal", []byte("active"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"wal", func(t *testing.T, path string) string {
			if err := os.WriteFile(path+"-wal", []byte("active"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"shm-empty", func(t *testing.T, path string) string {
			if err := os.WriteFile(path+"-shm", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"wal-header", func(t *testing.T, path string) string {
			b, err := os.ReadFile(path) //nolint:gosec // reads only the synthetic index supplied to this test callback
			if err != nil {
				t.Fatal(err)
			}
			b[18], b[19] = 2, 2
			if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // modifies only this callback's synthetic test fixture to simulate a WAL header
				t.Fatal(err)
			}
			return path
		}},
		{"linked-leaf", func(t *testing.T, path string) string {
			link := path + ".link"
			if err := os.Symlink(path, link); err != nil {
				t.Skipf("host cannot create link: %v", err)
			}
			return link
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			path = tc.edit(t, path)
			capture, err := CaptureIndex(context.Background(), path)
			if capture != nil {
				_ = capture.Close()
				t.Fatal("unsafe source admitted")
			}
			if err == nil || err.Error() != "onenote: onenote_snapshot_unavailable" {
				t.Fatalf("unsafe input must return fixed diagnostic: %v", err)
			}
		})
	}
}

func TestCaptureIndexRetainsCallerDeadlineThroughClose(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	capture, err := CaptureIndex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := capture.Close(); err == nil {
		t.Fatal("cancelled final validation cannot admit the copy")
	}
	if _, err := os.Stat(capture.Path); !os.IsNotExist(err) {
		t.Fatalf("cancelled capture not cleaned: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if c, err := CaptureIndex(ctx, path); c != nil || err == nil {
		t.Fatal("expired capture admitted")
	}
}

func TestCaptureIndexSerializedUntilClose(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
	if err != nil {
		t.Fatal(err)
	}
	first, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if second, err := CaptureIndex(ctx, path); second != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquisition should time out without opening source: %v", err)
	}
	if output := sqliteWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("second descriptor released first capture protection: %s", output)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureIndexPrivateAliasCannotReopenSource(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db", hierarchyFixture))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	if err := os.Remove(capture.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, capture.Path); err != nil {
		t.Skipf("host cannot create symlink: %v", err)
	}
	open := snapshotOpen
	t.Cleanup(func() { snapshotOpen = open })
	sourceOpens := 0
	snapshotOpen = func(name string) (*os.File, error) {
		resolved, err := filepath.EvalSymlinks(name)
		if err == nil && resolved == path {
			sourceOpens++
		}
		return open(name)
	}
	if err := capture.Close(); err == nil {
		t.Fatal("private-source alias admitted")
	}
	if sourceOpens != 0 {
		t.Fatal("private alias caused a second source descriptor")
	}
}

func TestCaptureIndexFinalSourceReplacementRefused(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	if err := os.Rename(path, path+".old"); err != nil {
		t.Skipf("host cannot substitute held source: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err == nil {
		t.Fatal("path substitution admitted")
	}
	if _, err := os.Stat(filepath.Dir(capture.Path)); !os.IsNotExist(err) {
		t.Fatalf("failed capture not cleaned: %v", err)
	}
}

func TestCaptureIndexEmptyJournalAdmitted(t *testing.T) {
	path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-journal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	capture, err := CaptureIndex(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureIndexAcquisitionFaultsCleanExactRoot(t *testing.T) {
	for _, fault := range []string{"open", "root", "create", "read", "write", "short-write", "close"} {
		t.Run(fault, func(t *testing.T) {
			path, err := filepath.EvalSymlinks(indexFixture(t, "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			open, root, create := snapshotOpen, snapshotRoot, snapshotCreate
			read, write, closeFile := snapshotRead, snapshotWrite, snapshotClose
			t.Cleanup(func() {
				snapshotOpen, snapshotRoot, snapshotCreate = open, root, create
				snapshotRead, snapshotWrite, snapshotClose = read, write, closeFile
			})
			var ownedRoot string
			snapshotRoot = func() (string, error) {
				if fault == "root" {
					return "", errors.New("root fault")
				}
				ownedRoot, err = root()
				return ownedRoot, err
			}
			switch fault {
			case "open":
				snapshotOpen = func(string) (*os.File, error) { return nil, errors.New("open fault") }
			case "create":
				snapshotCreate = func(string) (*os.File, error) { return nil, errors.New("create fault") }
			case "read":
				snapshotRead = func(*os.File, []byte) (int, error) { return 0, errors.New("read fault") }
			case "write":
				snapshotWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write fault") }
			case "short-write":
				snapshotWrite = func(f *os.File, b []byte) (int, error) { return write(f, b[:len(b)-1]) }
			case "close":
				snapshotClose = func(f *os.File) error {
					return errors.Join(closeFile(f), errors.New("close fault"))
				}
			}
			if capture, err := CaptureIndex(context.Background(), path); capture != nil || err == nil {
				if capture != nil {
					_ = capture.Close()
				}
				t.Fatal("failed acquisition admitted")
			}
			if ownedRoot != "" {
				if _, err := os.Stat(ownedRoot); !os.IsNotExist(err) {
					t.Fatalf("failed capture left root: %v", err)
				}
			}
			// Failures must release serialization as well as the descriptor.
			if !sourceGateFree(t) {
				t.Fatal("failed acquisition retained capture gate")
			}
		})
	}
}
