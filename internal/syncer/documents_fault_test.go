package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsRunnerFailureBoundaries(t *testing.T) {
	for _, fault := range []string{"read", "lock", "open", "discover", "observe", "capture", "panic", "empty-capture", "cancel-capture", "close"} {
		t.Run(fault, func(t *testing.T) {
			root := documentRootFixture(t)
			path := newDB(t)
			ctx := context.Background()
			report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
			if err != nil {
				t.Fatal(err)
			}
			read, acquire, open, discover, observe, capture := documentsRead, documentsAcquire, documentsOpen, documentsDiscover, documentsObserve, documentsCapture
			t.Cleanup(func() {
				documentsRead, documentsAcquire, documentsOpen, documentsDiscover, documentsObserve, documentsCapture = read, acquire, open, discover, observe, capture
			})
			options := DocumentOptions{DBPath: path, Explicit: true, SourceID: report.Sources[0].ID}
			switch fault {
			case "read":
				documentsRead = func(context.Context, string) (*store.Store, error) { return nil, os.ErrPermission }
				options.Explicit = false
				options.MaxAge = time.Hour
			case "lock":
				documentsAcquire = func(string) (func(), error) { return nil, os.ErrPermission }
			case "open":
				documentsOpen = func(context.Context, string) (*store.Store, error) { return nil, os.ErrPermission }
			case "discover":
				options.SourceID, options.Root, options.Discover = 0, root, true
				documentsDiscover = func(context.Context, string) ([]officecapture.DocumentInput, error) { return nil, os.ErrPermission }
			case "observe":
				documentsObserve = func(context.Context, string) (string, error) {
					return "", &officecapture.ReadError{Code: "office_json_input_unqualified"}
				}
			case "capture", "panic", "empty-capture", "cancel-capture":
				documentsCapture = func(context.Context, officecapture.DocumentInput, func(*officecapture.JSONCapture) error) error {
					switch fault {
					case "panic":
						panic("private source payload")
					case "empty-capture":
						return nil
					case "cancel-capture":
						return context.Canceled
					default:
						return &officecapture.ReadError{Code: "office_json_policy_restore_failed"}
					}
				}
			case "close":
				documentsOpen = func(ctx context.Context, name string) (*store.Store, error) {
					s, err := open(ctx, name)
					if err != nil {
						return nil, err
					}
					if err := s.Close(); err != nil {
						return nil, err
					}
					return s, nil
				}
			}
			failed, err := SyncDocuments(ctx, options)
			sourceFailure := fault == "observe" || fault == "capture" || fault == "panic" || fault == "empty-capture" || fault == "cancel-capture"
			if sourceFailure {
				if err != nil || failed.Failed != 1 || len(failed.Sources) != 1 ||
					(failed.Sources[0].Status != "failed" && failed.Sources[0].Status != "cancelled") {
					t.Fatalf("optional failure not terminal/report-only: %v", err)
				}
			} else if err == nil {
				t.Fatal("fatal archive/discovery fault returned success")
			}
			if err != nil && strings.Contains(err.Error(), "private source payload") {
				t.Fatal("panic payload reached diagnostics")
			}
		})
	}
}

func TestDocumentsRunnerAgeZeroPreservesArchiveAndSource(t *testing.T) {
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	dbBefore, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	observe, capture, discover := documentsObserve, documentsCapture, documentsDiscover
	t.Cleanup(func() { documentsObserve, documentsCapture, documentsDiscover = observe, capture, discover })
	documentsObserve = func(context.Context, string) (string, error) { t.Fatal("age0 observed a source"); return "", nil }
	documentsCapture = func(context.Context, officecapture.DocumentInput, func(*officecapture.JSONCapture) error) error {
		t.Fatal("age0 captured a source")
		return nil
	}
	documentsDiscover = func(context.Context, string) ([]officecapture.DocumentInput, error) {
		t.Fatal("age0 discovered a source")
		return nil, nil
	}
	if report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, MaxAge: 0}); err != nil || len(report.Sources) != 2 {
		t.Fatal("age0 failed to read existing source coverage")
	}
	after, err := os.Stat(path)
	if err != nil || after.Size() != dbBefore.Size() || !after.ModTime().Equal(dbBefore.ModTime()) {
		t.Fatal("age0 wrote/migrated the archive")
	}
}

func TestDocumentsRunnerRejectsInvalidOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "archive.db")
	for _, options := range []DocumentOptions{
		{DBPath: path, SourceID: -1}, {DBPath: path, MaxAge: -1},
		{DBPath: path, Root: "/unused", SourceID: 1, Discover: true, Explicit: true},
		{DBPath: path, Discover: true}, {DBPath: path, Root: "/unused"},
	} {
		if _, err := SyncDocuments(context.Background(), options); err == nil {
			t.Fatal("invalid options reached archive open")
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("invalid selection created archive parent")
	}
}

func TestDocumentsRunnerLateCancelledCaptureDoesNotAdmit(t *testing.T) {
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	capture := documentsCapture
	t.Cleanup(func() { documentsCapture = capture })
	documentsCapture = func(ctx context.Context, input officecapture.DocumentInput, consume func(*officecapture.JSONCapture) error) error {
		if err := capture(ctx, input, consume); err != nil {
			return err
		}
		return context.Canceled
	}
	failed, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: report.Sources[0].ID, Explicit: true})
	if err != nil || failed.Failed != 1 || failed.Sources[0].Status != "cancelled" ||
		failed.Sources[0].AdmittedID != report.Sources[0].AdmittedID {
		t.Fatal("staged result published before wrapper terminal success")
	}
}

func TestDocumentsRunnerSelectedUnknownNeverRefreshes(t *testing.T) {
	path := newDB(t)
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncDocuments(context.Background(), DocumentOptions{DBPath: path, SourceID: 999, MaxAge: time.Hour}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatal("unknown selected source was inferred")
	}
}
