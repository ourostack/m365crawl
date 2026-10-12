package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsSuccessfulEmptyResponseIsCompleteAndRetainsEarlierObservations(t *testing.T) {
	root := documentRootFixture(t)
	path := newDB(t)
	ctx := context.Background()
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Captured != 2 {
		t.Fatalf("registration failed: %v", err)
	}
	var recent store.KnowledgeSource
	for _, source := range report.Sources {
		if source.Kind == "office_recent" {
			recent = source
		}
	}
	if recent.Reason != "" {
		t.Fatalf("populated capture carried empty-response reason: %q", recent.Reason)
	}
	empty := `{"response_code":1,"delta_token":"synthetic","diagnostics_data":{"http_status_code":200}}`
	if err := os.WriteFile(filepath.Join(root, "recent.json"), []byte(empty), 0o600); err != nil {
		t.Fatal(err)
	}
	check := func(label string, wantCaptured, wantUnchanged int) {
		t.Helper()
		got, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: recent.ID, Explicit: true})
		if err != nil || got.Failed != 0 || got.Captured != wantCaptured || got.Unchanged != wantUnchanged || len(got.Sources) != 1 {
			t.Fatalf("%s: empty service response report = %+v, %v", label, got, err)
		}
		source := got.Sources[0]
		wantStatus := "complete"
		if wantUnchanged == 1 {
			wantStatus = "unchanged"
		}
		if source.Status != wantStatus || source.Coverage != "observations" ||
			source.Reason != officedocuments.NoDocumentsListed || len(source.Losses) != 0 || source.CompleteAt == nil {
			t.Fatalf("%s: empty service response source = %+v", label, source)
		}
		s, err := store.OpenReadOnly(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := s.DocumentObservations(ctx, store.DocumentFilter{SourceID: recent.ID, Limit: 10})
		if closeErr := s.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil || len(rows) != 1 || !rows[0].Retained {
			t.Fatalf("%s: earlier observation not kept as retained: %+v, %v", label, rows, err)
		}
	}
	check("first", 1, 0)
	check("repeat", 0, 1)
}
