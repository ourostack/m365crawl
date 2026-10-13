package officecapture

import (
	"context"
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

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/sourcecapture"
	_ "modernc.org/sqlite"
)

func sqliteURI(path, query string) string {
	return (&url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/"), RawQuery: query}).String()
}

// sqliteSource creates an ordinary rollback-journal SQLite file in a fresh qualified directory.
func sqliteSource(t *testing.T, name string, statements ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, name)
	db, err := sql.Open("sqlite", sqliteURI(path, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range append([]string{"CREATE TABLE IF NOT EXISTS probe(v TEXT); INSERT INTO probe VALUES('synthetic')"}, statements...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOfficeSQLiteWriterChild(t *testing.T) {
	path := os.Getenv("M365CRAWL_OFFICECAPTURE_WRITER")
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
	if _, err := db.Exec("UPDATE probe SET v='changed'"); err != nil {
		if !strings.Contains(err.Error(), "SQLITE_BUSY") && !strings.Contains(err.Error(), "locked") &&
			!strings.Contains(err.Error(), "readonly") {
			t.Fatalf("not SQLite write contention: %v", err)
		}
		fmt.Println("WRITE_BLOCKED")
		return
	}
	fmt.Println("WRITE_OK")
}

// officeWriterProbe runs a real separate SQLite writer process against path.
func officeWriterProbe(t *testing.T, path string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestOfficeSQLiteWriterChild$", "-test.count=1") //nolint:gosec // reexecutes only the current test binary with literal arguments
	cmd.Env = append(os.Environ(), "M365CRAWL_OFFICECAPTURE_WRITER="+path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("SQLite writer child: %v\n%s", err, output)
	}
	return string(output)
}

func shortContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestNotesOfficeCaptureSerialization(t *testing.T) {
	jsonPath, _ := ordinaryFixture(t)
	notesPath := sqliteSource(t, "index.db")
	capture, err := onenote.CaptureIndex(context.Background(), notesPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = capture.Close() })
	if signature, err := ObserveJSON(shortContext(t), jsonPath); err == nil || signature != "" {
		t.Fatal("Office JSON observation opened a source while notes held the source lifetime")
	}
	if inputs, err := DiscoverJSON(shortContext(t), filepath.Dir(jsonPath)); err == nil || inputs != nil {
		t.Fatal("Office JSON discovery opened a source while notes held the source lifetime")
	}
	called := false
	if err := WithJSONCapture(shortContext(t), DocumentInput{Path: jsonPath, Surface: officedocuments.Recent, Application: "supplied"},
		func(*JSONCapture) error { called = true; return nil }); err == nil || called {
		t.Fatal("Office JSON capture opened a source while notes held the source lifetime")
	}
	waiting := make(chan error, 1)
	go func() {
		_, err := ObserveJSON(context.Background(), jsonPath)
		waiting <- err
	}()
	select {
	case <-waiting:
		t.Fatal("unrelated Office observation did not wait for notes cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-waiting; err != nil {
		t.Fatalf("Office observation did not proceed after notes cleanup: %v", err)
	}
	err = WithJSONCapture(context.Background(), DocumentInput{Path: jsonPath, Surface: officedocuments.Recent, Application: "supplied"},
		func(*JSONCapture) error {
			if notes, err := onenote.CaptureIndex(shortContext(t), notesPath); err == nil || notes != nil {
				t.Fatal("notes capture entered a held Office source lifetime")
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := onenote.CaptureIndex(context.Background(), notesPath)
	if err != nil {
		t.Fatalf("notes retry after Office cleanup: %v", err)
	}
	if err := notes.Close(); err != nil {
		t.Fatal(err)
	}
}

// holdSQLiteSource stands in for a registry capture: the same gate owner holding SHARED protection.
func holdSQLiteSource(t *testing.T, path string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	release, err := sourcecapture.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path) //nolint:gosec // synthetic fixture
	if err != nil {
		release()
		t.Fatal(err)
	}
	unlock, err := sourcecapture.LockSQLite(file)
	if err != nil {
		_ = file.Close()
		release()
		t.Fatal(err)
	}
	return func() {
		if err := errors.Join(unlock(), file.Close()); err != nil {
			t.Error(err)
		}
		release()
	}
}

func TestRegistryJSONRefusalCannotReleaseShared(t *testing.T) {
	path := sqliteSource(t, "recent.json")
	release := holdSQLiteSource(t, path)
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("held protection did not exclude writer: %s", output)
	}
	// Metadata observation, directory discovery and a JSON format failure all select the same
	// ordinary SQLite file. None may open and close it while the protection is held.
	_, _ = ObserveJSON(shortContext(t), path)
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("JSON observation released held SHARED protection: %s", output)
	}
	_, _ = DiscoverJSON(shortContext(t), filepath.Dir(path))
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("JSON discovery released held SHARED protection: %s", output)
	}
	_ = WithJSONCapture(shortContext(t), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"},
		func(c *JSONCapture) error { _, err := c.ReadJSON(context.Background()); return err })
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("JSON format refusal released held SHARED protection: %s", output)
	}
	_ = WithJSONCapture(shortContext(t), DocumentInput{Path: filepath.Dir(path), Surface: officedocuments.Recent, Application: "supplied"},
		func(*JSONCapture) error { return nil })
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_BLOCKED") {
		t.Fatalf("JSON type refusal released held SHARED protection: %s", output)
	}
	release()
	released = true
	// After owned cleanup the same calls run, fail closed on format, and leave no lock behind.
	if _, err := ObserveJSON(context.Background(), path); err != nil {
		t.Fatalf("observation retry after cleanup: %v", err)
	}
	err := WithJSONCapture(context.Background(), DocumentInput{Path: path, Surface: officedocuments.Recent, Application: "supplied"},
		func(c *JSONCapture) error { _, err := c.ReadJSON(context.Background()); return err })
	var fixed *ReadError
	if !errors.As(err, &fixed) || fixed.Code != "office_json_unrecognized" {
		t.Fatalf("SQLite bytes were not refused as unrecognized JSON: %v", err)
	}
	if output := officeWriterProbe(t, path); !strings.Contains(output, "WRITE_OK") {
		t.Fatalf("writer did not recover after cleanup: %s", output)
	}
}
