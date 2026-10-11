package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func documentRootFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native source admission is Darwin/Windows")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payloads := map[string]string{
		"recent.json": `{"documents":{"items":[{"title":"Synthetic recent","url":"https://example.invalid/recent"}]}}`,
		"shared.json": `{"shared_documents":[{"FileName":"Synthetic shared","DocumentUrl":"https://example.invalid/shared","SharedByUserName":"Synthetic sharer"}]}`,
	}
	for name, payload := range payloads {
		if err := os.WriteFile(filepath.Join(root, name), []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestDocumentsRegisteredCaptureReuseAndRootlessRefresh(t *testing.T) {
	root := documentRootFixture(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil || report.Captured != 2 || report.Failed != 0 || len(report.Sources) != 2 {
		t.Fatalf("explicit source registration failed: %v", err)
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.DocumentObservations(ctx, store.DocumentFilter{Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatal("registered capture not searchable")
	}
	id := rows[0].ID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	repeat, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil || repeat.Unchanged != 2 || repeat.Captured != 0 {
		t.Fatalf("identical copies were readmitted as new representations: %v", err)
	}
	refresh, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, MaxAge: time.Hour})
	if err != nil || refresh.Skipped != 2 {
		t.Fatal("rootless registered unchanged freshness did not throttle")
	}
	s, err = store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	detail, err := s.DocumentObservation(ctx, id)
	if err != nil || detail.Observation.Application != "supplied" || detail.Source.AdmittedID == detail.Observation.CaptureID {
		t.Fatal("reused first representation or persisted registration provenance changed")
	}
}

func TestDocumentsFailureKeepsLastGoodAndSelectedPeersIndependent(t *testing.T) {
	root := documentRootFixture(t)
	path := filepath.Join(t.TempDir(), "archive.db")
	ctx := context.Background()
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	var recent, shared store.KnowledgeSource
	for _, source := range report.Sources {
		if source.Kind == "office_recent" {
			recent = source
		} else {
			shared = source
		}
	}
	if err := os.WriteFile(filepath.Join(root, "recent.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	failed, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: recent.ID, Explicit: true})
	if err != nil || failed.Failed != 1 || len(failed.Sources) != 1 || failed.Sources[0].Status != "failed" {
		t.Fatalf("missing-envelope failure not independently reported: %v", err)
	}
	s, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows, _, err := s.DocumentObservations(ctx, store.DocumentFilter{SourceID: recent.ID, Query: "Synthetic", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatal("failed optional input erased last-good document metadata")
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range sources {
		if source.ID == shared.ID && source.AttemptID != shared.AttemptID {
			t.Fatal("selected failed input refreshed healthy peer")
		}
	}
}

func TestDocumentsOrdinaryAbsentAndAgeZeroNeverCreateArchive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "missing", "archive.db")
	for _, maxAge := range []time.Duration{0, time.Hour} {
		report, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, MaxAge: maxAge})
		if err != nil || len(report.Sources) != 0 {
			t.Fatal("unregistered ordinary read acquired a source")
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatal("unregistered read created archive directory")
		}
	}
	if _, err := SyncDocuments(ctx, DocumentOptions{DBPath: path, SourceID: 1, MaxAge: 0}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatal("missing selected source became successful empty read")
	}
}
