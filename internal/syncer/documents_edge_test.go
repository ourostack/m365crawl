package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsRunnerPartialAndChangedSourceBypassThrottle(t *testing.T) {
	root := documentRootFixture(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(root, "recent.json"),
		[]byte(`{"documents":{"items":[{"title":"Valid original","url":"https://example.invalid/valid"},null]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	var recent store.KnowledgeSource
	for _, source := range report.Sources {
		if source.Kind == "office_recent" {
			recent = source
		}
	}
	if recent.Coverage != "partial" || recent.Losses["office_document_unmapped"] != 1 {
		t.Fatal("partial source losses not admitted")
	}
	if err := os.WriteFile(filepath.Join(root, "recent.json"),
		[]byte(`{"documents":{"items":[{"title":"Changed current","url":"https://example.invalid/changed"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: recent.ID, MaxAge: time.Hour})
	if err != nil || refreshed.Captured != 1 || refreshed.Skipped != 0 {
		t.Fatal("changed registered source was throttled as unchanged")
	}
}

func TestDocumentsRunnerAtomicFailureAndStaleAttemptEdges(t *testing.T) {
	for _, fault := range []string{"missing-binding", "registration-write", "failed-terminal", "commit", "stale", "mid-loop-cancel", "read-sources"} {
		t.Run(fault, func(t *testing.T) {
			root := documentRootFixture(t)
			path := filepath.Join(t.TempDir(), "archive.db")
			ctx := context.Background()
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
			now, observe, capture, open := documentsNow, documentsObserve, documentsCapture, documentsOpen
			t.Cleanup(func() { documentsNow, documentsObserve, documentsCapture, documentsOpen = now, observe, capture, open })
			options := DocumentOptions{DBPath: path, SourceID: report.Sources[0].ID, Explicit: true}
			switch fault {
			case "missing-binding":
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
			case "registration-write":
				documentsNow = func() time.Time { return time.Time{} }
			case "failed-terminal":
				documentsNow = func() time.Time { return time.Time{} }
				documentsObserve = func(context.Context, string) (string, error) { return "", os.ErrPermission }
			case "commit":
				calls := 0
				documentsNow = func() time.Time {
					calls++
					if calls > 1 {
						return time.Time{}
					}
					return now()
				}
			case "stale":
				documentsCapture = func(ctx context.Context, input officecapture.DocumentInput, consume func(*officecapture.JSONCapture) error) error {
					if err := capture(ctx, input, consume); err != nil {
						return err
					}
					_, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{
						Kind: store.DocumentKind(input.Surface), Platform: report.Sources[0].Platform,
						Locator: input.Path, At: now()}, input.Surface, input.Application)
					return err
				}
			case "mid-loop-cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				options.SourceID = 0
				documentsObserve = func(ctx context.Context, name string) (string, error) {
					signature, err := observe(ctx, name)
					cancel()
					return signature, err
				}
			case "read-sources":
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				_, err := selectedDocumentSources(ctx, st, 0, nil)
				if err == nil {
					t.Fatal("archive source read fault hidden")
				}
				return
			}
			result, err := syncDocuments(ctx, st, options)
			if err == nil {
				t.Fatalf("fatal/stale edge admitted: %d", result.Captured)
			}
			if fault == "stale" && !errors.Is(err, store.ErrStaleKnowledgeAttempt) {
				t.Fatal("stale terminal reason lost")
			}
		})
	}
}
