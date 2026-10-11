package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestSyncNotesArchiveOpenAndLockErrorsRemainVisible(t *testing.T) {
	for _, operation := range []string{"read", "write", "lock"} {
		t.Run(operation, func(t *testing.T) {
			read, write, acquire := notesOpenRead, notesOpen, notesAcquire
			t.Cleanup(func() { notesOpenRead, notesOpen, notesAcquire = read, write, acquire })
			fault := errors.New("archive fault")
			switch operation {
			case "read":
				notesOpenRead = func(context.Context, string) (*store.Store, error) { return nil, fault }
			case "write":
				notesOpen = func(context.Context, string) (*store.Store, error) { return nil, fault }
			default:
				notesAcquire = func(string) (func(), error) { return nil, fault }
			}
			_, err := SyncNotes(context.Background(), NotesOptions{DBPath: filepath.Join(t.TempDir(), "archive.db"), Explicit: operation != "read"})
			if !errors.Is(err, fault) {
				t.Fatalf("archive error concealed: %v", err)
			}
		})
	}
}

func TestSyncNotesAdmissionDiagnosticFailuresAndCancellation(t *testing.T) {
	for _, failure := range []string{"selection", "begin", "observe-diagnostic", "capture-diagnostic", "commit", "cancel-before"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			notesIndex(t, root, "index.db", "fixture")
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "archive.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			observe, capture, read := notesObserve, notesCapture, notesRead
			t.Cleanup(func() { notesObserve, notesCapture, notesRead = observe, capture, read })
			options := NotesOptions{Root: root, Explicit: true, Discover: true}
			switch failure {
			case "selection":
				options.SourceID = 999
			case "begin":
				notesObserve = func(path string) (string, error) {
					signature, err := observe(path)
					_ = st.Close()
					return signature, err
				}
			case "observe-diagnostic":
				calls := 0
				now := notesNow
				t.Cleanup(func() { notesNow = now })
				notesNow = func() time.Time {
					calls++
					if calls == 2 {
						_ = st.Close()
					}
					return now()
				}
				notesObserve = func(string) (string, error) { return "observed", errors.New("observe fault") }
			case "capture-diagnostic":
				notesCapture = func(context.Context, string) (*onenote.IndexCapture, error) {
					_ = st.Close()
					return nil, errors.New("capture fault")
				}
			case "commit":
				notesRead = func(ctx context.Context, path string) (onenote.Result, error) {
					result, err := read(ctx, path)
					_ = st.Close()
					return result, err
				}
			case "cancel-before":
				cancel()
			}
			if _, err := syncNotes(ctx, st, options); err == nil {
				t.Fatal("archive failure concealed as partial success")
			}
		})
	}
}

func TestSyncNotesDiscoveryRefusalsAndVanishedRoot(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := syncNotes(context.Background(), st, NotesOptions{Discover: true}); err == nil {
		t.Fatal("implicit discovery accepted")
	}
	platform := notesPlatform
	t.Cleanup(func() { notesPlatform = platform })
	notesPlatform = "unsupported"
	if _, err := syncNotes(context.Background(), st, NotesOptions{Discover: true, Explicit: true}); err == nil {
		t.Fatal("unsupported default silently accepted")
	}
	file := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverNotes(file); err == nil {
		t.Fatal("non-directory root accepted")
	}
	if _, err := participatingNotesSources(context.Background(), st, 999, nil); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown selected coverage: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := participatingNotesSources(context.Background(), st, 0, nil); err == nil {
		t.Fatal("closed coverage query reported healthy empty")
	}
}

func TestSyncNotesCancellationBetweenIndependentPeers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	notesIndex(t, root, "first.db", "first")
	notesIndex(t, root, "second.db", "second")
	dbPath := filepath.Join(t.TempDir(), "archive.db")
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, Root: root, Discover: true, Explicit: true}); err != nil {
		t.Fatal(err)
	}
	now := notesNow
	t.Cleanup(func() { notesNow = now })
	calls := 0
	notesNow = func() time.Time {
		calls++
		if calls == 1 {
			cancel()
		}
		return now()
	}
	if _, err := SyncNotes(ctx, NotesOptions{DBPath: dbPath, MaxAge: time.Hour}); !errors.Is(err, context.Canceled) {
		t.Fatalf("between-peer cancellation concealed: %v", err)
	}
}
