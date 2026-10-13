//go:build darwin || linux || windows

package sourcecapture

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func sqliteURI(path, query string) string {
	return (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/"), RawQuery: query}).String()
}

func sqliteFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "source.db")
	db, err := sql.Open("sqlite", sqliteURI(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t(v TEXT); INSERT INTO t VALUES('synthetic')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSQLiteWriterChild(t *testing.T) {
	path := os.Getenv("M365CRAWL_SOURCECAPTURE_WRITER")
	if path == "" {
		return
	}
	db, err := sql.Open("sqlite", sqliteURI(path, "mode=rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec("PRAGMA busy_timeout=25"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE t SET v='changed'"); err != nil {
		if !strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "locked") &&
			!strings.Contains(err.Error(), "readonly") {
			t.Fatalf("not SQLite write contention: %v", err)
		}
		fmt.Println("WRITE_BLOCKED")
		return
	}
	fmt.Println("WRITE_OK")
}

// writerProbe runs a real separate SQLite writer process; same-process locks prove nothing.
func writerProbe(t *testing.T, path string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSQLiteWriterChild$", "-test.count=1") //nolint:gosec // reexecutes only the current test binary with literal arguments
	cmd.Env = append(os.Environ(), "M365CRAWL_SOURCECAPTURE_WRITER="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQLite writer child: %v\n%s", err, output)
	}
	return string(output)
}

func TestLockSQLiteExcludesSeparateWriterUntilUnlock(t *testing.T) {
	path := sqliteFixture(t)
	file, err := os.Open(path) //nolint:gosec // synthetic fixture
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	unlock, err := LockSQLite(file)
	if err != nil {
		t.Fatal(err)
	}
	if output := writerProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("held SHARED protection admitted a writer: %s", output)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if output := writerProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover: %s", output)
	}
}
