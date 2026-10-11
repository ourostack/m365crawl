package syncer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsDiscoveryKeepsRegisteredBeforeNewLocators(t *testing.T) {
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
	if _, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: "office_shared",
		Platform: documentsPlatform, Locator: filepath.Join(root, "shared.json"), At: time.Now().UTC()},
		officedocuments.Shared, "supplied"); err != nil {
		t.Fatal(err)
	}
	report, err := syncDocuments(ctx, st, DocumentOptions{Explicit: true, Discover: true, Root: root})
	if err != nil || report.Captured != 2 || report.Sources[0].Kind != "office_shared" ||
		report.Sources[0].ID != 1 || report.Sources[1].ID != 2 {
		t.Fatal("discovery reordered an existing locator behind an unknown numeric ID")
	}
}
