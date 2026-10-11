package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

func notesIndex(t *testing.T, root, name, text string) string {
	t.Helper()
	path := filepath.Join(root, name)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
create table Entities(rowid integer primary key,Type integer,GOID text,GUID text,GOSID text,ParentGOID text,GrandparentGOIDs text,LastModifiedTime integer,Title text);
create table PageElements(rowid integer primary key,GOID text,Text text,Jcid integer,EntityRowId integer);
create table NoteFlags(rowid integer primary key,Type integer,Shape integer,Status integer,Label text,PageElementRowId integer);
create table Hashtags(rowid integer primary key,PageElementRowId integer);
insert into Entities values(4,4,'notebook','notebook-guid',null,null,null,null,'Notebook'),(1,1,'page','page-guid',null,'notebook',null,116444736000000000,'Fixture page');
insert into PageElements values(10,'text',?,393230,1);`, text)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("fixture: %v/%v", err, closeErr)
	}
	return path
}

func TestSyncNotesRegistersAndRootlessRefreshesExplicitLocator(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := notesIndex(t, root, "index.db", "originalword")
	dbPath := newDB(t)
	report, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || len(report.Sources) != 1 {
		t.Fatalf("registration: %#v/%v", report, err)
	}
	sourceID := report.Sources[0].ID
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: sourceID, MaxAge: time.Hour})
	if err != nil || report.Skipped != 1 || report.Captured != 0 {
		t.Fatalf("recent unchanged metadata recopied: %#v/%v", report, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update PageElements set Text='repairedword'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: sourceID, MaxAge: time.Hour})
	if err != nil || report.Captured != 1 || report.Sources[0].ID != sourceID {
		t.Fatalf("rootless repaired capture: %#v/%v", report, err)
	}
	st, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, _, err := st.Notes(ctx, store.NoteFilter{Query: "repairedword"})
	if err != nil || len(rows) != 1 {
		t.Fatal("registered explicit source was not refreshed")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: sourceID, MaxAge: time.Hour})
	if err != nil || report.Failed != 1 || report.Sources[0].Status != "unavailable" {
		t.Fatalf("disappeared registered source concealed: %#v/%v", report, err)
	}
	rows, _, err = st.Notes(ctx, store.NoteFilter{Query: "repairedword"})
	if err != nil || len(rows) != 1 {
		t.Fatal("missing registered source erased last-good")
	}
}

func TestSyncNotesFailedPeerRetryUsesAttemptedMetadata(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notesIndex(t, root, "good.db", "healthyword")
	bad := notesIndex(t, root, "bad.db", "badword")
	if err := os.WriteFile(bad+"-wal", []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := newDB(t)
	report, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Failed != 1 || len(report.Sources) != 2 {
		t.Fatalf("independent peers: %#v/%v", report, err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, MaxAge: time.Hour})
	if err != nil || report.Skipped != 2 {
		t.Fatalf("recent failed metadata retry repeated: %#v/%v", report, err)
	}
	if err := os.Remove(bad + "-wal"); err != nil {
		t.Fatal(err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, MaxAge: time.Hour})
	if err != nil || report.Captured != 1 || report.Skipped != 1 {
		t.Fatalf("repaired peer eligibility: %#v/%v", report, err)
	}
	report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Unchanged != 2 {
		t.Fatalf("explicit identical-content qualification: %#v/%v", report, err)
	}
}

func TestSyncNotesZeroAgeNeverDiscoversOrWrites(t *testing.T) {
	dbPath := newDB(t)
	report, err := SyncNotes(context.Background(), NotesOptions{DBPath: dbPath, Root: "/missing", Discover: true})
	if err != nil || len(report.Sources) != 0 {
		t.Fatalf("zero-age optional read: %#v/%v", report, err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("zero-age notes read created archive")
	}
}

func TestSyncNotesUnregisteredOrdinaryReadDoesNotCreateOrRepairLegacyArchive(t *testing.T) {
	ctx := context.Background()
	dbPath := newDB(t)
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, MaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatal("ordinary unregistered notes created an archive")
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`drop table sync_runs`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, MaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	ro, err := store.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if _, err := ro.NeedsUpgrade(ctx); err == nil {
		t.Fatal("notes-only read silently repaired a broken legacy sync log")
	}
}

func TestSyncNotesExplicitRootDoesNotCaptureUnrelatedRegistration(t *testing.T) {
	dbPath := newDB(t)
	first, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notesIndex(t, first, "first.db", "firstword")
	notesIndex(t, second, "second.db", "secondword")
	for _, root := range []string{first, second} {
		report, err := SyncNotes(context.Background(), NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
		if err != nil || report.Captured != 1 || report.Unchanged != 0 {
			t.Fatalf("explicit root touched unrelated registration: %#v/%v", report, err)
		}
	}
}

func TestSyncNotesSelectedCoverageExcludesOtherSources(t *testing.T) {
	ctx := context.Background()
	dbPath := newDB(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notesIndex(t, root, "first.db", "first")
	notesIndex(t, root, "second.db", "second")
	initial, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	id := initial.Sources[0].ID
	for _, age := range []time.Duration{0, time.Hour} {
		report, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, SourceID: id, MaxAge: age})
		if err != nil || len(report.Sources) != 1 || report.Sources[0].ID != id {
			t.Fatalf("selected source leaked unrelated coverage: %#v/%v", report, err)
		}
	}
}

func TestSyncNotesDiscoveryBoundsAndUnsafePeers(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "cap", "directory-leaf", "linked-leaf"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing":
				root = filepath.Join(root, "missing")
			case "cap":
				for i := range 65 {
					if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.db", i)), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "directory-leaf":
				if err := os.Mkdir(filepath.Join(root, "directory.db"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "linked-leaf":
				path := notesIndex(t, root, "good.db", "good")
				if err := os.Symlink(path, filepath.Join(root, "linked.db")); err != nil {
					t.Skipf("host cannot create symlink: %v", err)
				}
			}
			report, err := SyncNotes(context.Background(), NotesOptions{
				DBPath: newDB(t), Root: root, Explicit: true, Discover: true,
			})
			switch kind {
			case "missing", "cap":
				if err == nil {
					t.Fatal("invalid discovery accepted")
				}
			case "empty":
				if err != nil || report.Captured != 0 || report.Failed != 0 {
					t.Fatalf("scoped empty: %#v/%v", report, err)
				}
			default:
				if err != nil || report.Failed != 1 {
					t.Fatalf("unsafe leaf silently ignored: %#v/%v", report, err)
				}
			}
		})
	}
}

func TestSyncNotesCaptureParseCancellationAndPartialTransitions(t *testing.T) {
	for _, failure := range []string{"capture", "read", "cancelled", "partial"} {
		t.Run(failure, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			notesIndex(t, root, "index.db", "retainedword")
			capture, read := notesCapture, notesRead
			t.Cleanup(func() { notesCapture, notesRead = capture, read })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "capture":
				notesCapture = func(context.Context, string) (*onenote.IndexCapture, error) {
					return nil, errors.New("capture fault")
				}
			case "read", "cancelled", "partial":
				notesRead = func(ctx context.Context, path string) (onenote.Result, error) {
					if failure == "read" {
						return onenote.Result{}, errors.New("parse fault")
					}
					if failure == "cancelled" {
						cancel()
						return onenote.Result{}, context.Canceled
					}
					result, err := read(ctx, path)
					result.Losses = []onenote.Loss{{Code: "onenote_element_unmapped", Count: 1}}
					return result, err
				}
			}
			dbPath := newDB(t)
			report, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
			switch failure {
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled capture returned success: %v", err)
				}
				st, err := store.OpenReadOnly(context.Background(), dbPath)
				if err != nil {
					t.Fatal(err)
				}
				sources, err := st.KnowledgeSources(context.Background(), "notes")
				_ = st.Close()
				if err != nil || len(sources) != 1 || sources[0].Status != "cancelled" {
					t.Fatal("cancelled attempt diagnostics not persisted")
				}
			case "partial":
				if err != nil || report.Captured != 1 || report.Sources[0].Coverage != "partial" {
					t.Fatalf("partial admission: %#v/%v", report, err)
				}
				notesRead = read
				report, err = SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Explicit: true, Discover: true})
				if err != nil || report.Unchanged != 1 || report.Sources[0].Coverage != "partial" {
					t.Fatal("unchanged bytes promoted partial to complete")
				}
			default:
				if err != nil || report.Failed != 1 {
					t.Fatalf("failure diagnostics: %#v/%v", report, err)
				}
			}
		})
	}
}

func TestSyncNotesRootResolutionUsesExplicitSourceOnUnsupportedPlatform(t *testing.T) {
	platform, home := notesPlatform, notesHome
	t.Cleanup(func() { notesPlatform, notesHome = platform, home })
	notesPlatform = "windows"
	if _, err := notesRoot(""); err == nil {
		t.Fatal("unsupported default discovery accepted")
	}
	if _, err := notesRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	notesPlatform = "darwin"
	notesHome = func() (string, error) { return "", errors.New("home fault") }
	if _, err := notesRoot(""); err == nil {
		t.Fatal("home error concealed")
	}
	notesHome = func() (string, error) { return "/fictional/home", nil }
	if root, err := notesRoot(""); err != nil || filepath.Base(root) != "FullTextSearchIndex" {
		t.Fatal("default Mac locator invalid")
	}
}
