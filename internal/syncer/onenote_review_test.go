package syncer

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestNotesReviewedRetainedSelectionRefreshesCurrentLocatorAdmission(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := notesIndex(t, root, "index.db", "original-A")
	original, err := os.ReadFile(path) //nolint:gosec // test-owned synthetic source
	if err != nil {
		t.Fatal(err)
	}
	dbPath := newDB(t)
	first, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	oldID := first.Sources[0].ID
	uri := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update Entities set GOID='notebook-B' where Type=4; update PageElements set Text='different-B'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil || len(second.Sources) != 2 {
		t.Fatal("reset source did not qualify separately", err)
	}
	report, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: oldID, Explicit: true})
	if err != nil || report.Unchanged != 1 {
		t.Fatalf("selected retained source compared against wrong incarnation: captured=%d unchanged=%d err=%v",
			report.Captured, report.Unchanged, err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil { //nolint:gosec // restores only owned synthetic source bytes
		t.Fatal(err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: oldID, MaxAge: time.Hour})
	if err != nil || report.Captured != 1 || report.Failed != 0 || len(report.Sources) != 1 || report.Sources[0].ID != oldID {
		t.Fatalf("restored retained source admission: captured=%d failed=%d err=%v", report.Captured, report.Failed, err)
	}
	st, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sources, err := st.KnowledgeSources(ctx, "notes")
	if err != nil || len(sources) != 2 {
		t.Fatal("restored notebook identities merged", err)
	}
}

func TestNotesReviewedCancellationAfterCloseTerminalizesAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notesIndex(t, root, "index.db", "retainedword")
	dbPath := newDB(t)
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	now := notesNow
	t.Cleanup(func() { notesNow = now })
	calls := 0
	notesNow = func() time.Time {
		calls++
		if calls == 2 {
			cancel()
		}
		return now()
	}
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-close cancellation concealed: %v", err)
	}
	st, err := store.OpenReadOnly(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sources, err := st.KnowledgeSources(context.Background(), "notes")
	if err != nil || len(sources) != 1 || sources[0].Status != "cancelled" || sources[0].AdmittedID != 1 {
		t.Fatalf("post-close cancellation left pending or lost admission: %#v/%v", sources, err)
	}
	rows, _, err := st.Notes(context.Background(), store.NoteFilter{Query: "retainedword"})
	if err != nil || len(rows) != 1 || rows[0].CaptureID != 1 {
		t.Fatal("cancelled admission replaced prior representation", err)
	}
}
