package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsRunnerRegistrationAndParticipatingSourceReadEdges(t *testing.T) {
	root := documentRootFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	if sources, err := selectedDocumentSources(ctx, st, report.Sources[0].ID, map[string]bool{}); err == nil || len(sources) != 0 {
		t.Fatal("excluded selected source reported a participating success")
	}
	if _, err := st.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "office_recent", Platform: "synthetic",
		Locator: filepath.Join(root, "unbound.json"), At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncDocuments(ctx, st, DocumentOptions{DBPath: path, Explicit: true}); err == nil {
		t.Fatal("bare registration silently inferred descriptor provenance")
	}
}

func TestDocumentsRunnerFailedTerminalAndCancellationEdges(t *testing.T) {
	for _, fault := range []string{"failed-terminal", "cancel-next"} {
		t.Run(fault, func(t *testing.T) {
			root := documentRootFixture(t)
			path := filepath.Join(t.TempDir(), "archive.db")
			ctx := context.Background()
			if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true}); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			}()
			now, observe := documentsNow, documentsObserve
			t.Cleanup(func() { documentsNow, documentsObserve = now, observe })
			if fault == "failed-terminal" {
				calls := 0
				documentsNow = func() time.Time {
					calls++
					if calls > 1 {
						return time.Time{}
					}
					return now()
				}
				documentsObserve = func(context.Context, string) (string, error) { return "", os.ErrPermission }
			} else {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				documentsObserve = func(ctx context.Context, path string) (string, error) {
					signature, err := observe(ctx, path)
					cancel()
					return signature, err
				}
			}
			options := DocumentOptions{DBPath: path, Explicit: true}
			if fault == "cancel-next" {
				options.Explicit, options.MaxAge = false, time.Hour
			}
			if _, err := syncDocuments(ctx, st, options); err == nil {
				t.Fatal("terminal/cancel edge became success")
			}
		})
	}
}

func TestDocumentsRunnerSameRegisteredPathHasDeterministicSelection(t *testing.T) {
	root := documentRootFixture(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Separate kinds never turn one native payload into inferred shared entities.
	for _, binding := range []struct{ kind, surface string }{{"office_recent", "recent"}, {"office_shared", "shared"}} {
		if _, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: binding.kind,
			Platform: documentsPlatform, Locator: filepath.Join(root, "recent.json"), At: time.Now().UTC()},
			officedocuments.Surface(binding.surface), "supplied"); err != nil {
			t.Fatal(err)
		}
	}
	if report, err := syncDocuments(ctx, st, DocumentOptions{Explicit: true}); err != nil || report.Captured != 1 || report.Failed != 1 {
		t.Fatal("same-path distinct surface claims merged or bypassed envelope refusal")
	}
}

func TestDocumentsRunnerFailedRegistrationNeverInventsDescriptor(t *testing.T) {
	root := documentRootFixture(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	now := documentsNow
	t.Cleanup(func() { documentsNow = now })
	documentsNow = func() time.Time { return time.Time{} }
	_, err := SyncDocuments(context.Background(), DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err == nil {
		t.Fatal("zero attempt clock registered")
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("invalid registration mislabeled cancellation")
	}
}

func TestDocumentsRunnerRegisteredLocatorOrderIsNumericNotPath(t *testing.T) {
	root := documentRootFixture(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "archive.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, surface := range []officedocuments.Surface{officedocuments.Shared, officedocuments.Recent} {
		if _, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.DocumentKind(surface),
			Platform: documentsPlatform, Locator: filepath.Join(root, string(surface)+".json"), At: time.Now().UTC()},
			surface, "supplied"); err != nil {
			t.Fatal(err)
		}
	}
	report, err := syncDocuments(ctx, st, DocumentOptions{Explicit: true})
	if err != nil || report.Captured != 2 {
		t.Fatal("registered sources did not capture")
	}
	if report.Sources[0].Kind != "office_shared" || report.Sources[0].ID != 1 ||
		report.Sources[1].Kind != "office_recent" || report.Sources[1].ID != 2 {
		t.Fatal("registered numeric locator order changed behind path sorting")
	}
}
