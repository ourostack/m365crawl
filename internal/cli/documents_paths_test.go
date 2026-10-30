package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsCLICommandValidationAndSuccessfulSync(t *testing.T) {
	e := newEnv(t)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{Captured: 1, Sources: []store.KnowledgeSource{{ID: 1, Kind: "office_recent",
			Locator: "/synthetic/recent.json", Status: "partial", Coverage: "partial"}}}, nil
	}
	code, stdout, stderr := e.run("docs", "sync", "--root", "/synthetic")
	if code != 0 || !strings.Contains(stdout, `"captured":1`) || !strings.Contains(stdout, "office_recent") {
		t.Fatalf("sync report lost actual source participation: %s", stderr)
	}
	for _, args := range [][]string{
		{"--account", "tenant/user", "docs", "sync"},
		{"docs", "sync", "--root", "/synthetic", "--source-id", "1"},
		{"docs", "sync", "--source-id", "-1"}, {"docs", "list", "--limit", "-1"},
		{"docs", "list", "--since", "bad"}, {"docs", "list", "--until", "bad"},
		{"docs", "search"}, {"docs", "sources", "--source-id", "-1"},
		{"--account", "tenant/user", "docs", "sources"}, {"--account", "tenant/user", "docs", "show", "document-observation:1"},
		{"docs", "list", "--source-id", "-1"}, {"--fields", "no-key", "docs", "list"},
		{"--fields", "no-key", "docs", "search", "word"}, {"--fields", "no-key", "docs", "show", "document-observation:1"},
		{"--fields", "no-key", "docs", "sources"},
	} {
		code, _, stderr := e.run(args...)
		if code != 2 {
			t.Fatalf("invalid document command accepted: %v %s", args, stderr)
		}
	}
}

func TestDocumentsCLIReadFailuresAndMissingSelections(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	for _, args := range [][]string{
		{"docs", "list", "--source-id", "999"}, {"docs", "sources", "--source-id", "999"},
	} {
		code, _, stderr := e.run(append([]string{"--no-sync"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "document_source_unknown") {
			t.Fatal("nonexistent source became successful empty selection")
		}
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	rt := newRuntime(context.Background(), &Globals{}, os.Stdout, os.Stderr)
	if _, err := st.DocumentSnapshot(rt.ctx, store.DocumentFilter{}); err == nil {
		t.Fatal("closed archive coverage read hidden")
	}
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, context.Canceled
	}
	code, _, _ := e.run("docs", "list")
	if code == 0 {
		t.Fatal("cancelled refresh served successful rows")
	}
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, errors.New("archive refresh failed")
	}
	code, _, stderr := e.run("docs", "list", "--source-id", "999")
	if code == 0 || !strings.Contains(stderr, "db_error") {
		t.Fatal("failed selected refresh guessed a source")
	}
}

func TestDocumentsOptionalCoverageMergeKeepsFailuresAndSources(t *testing.T) {
	if mergedOptionalCoverage(nil, nil) != nil {
		t.Fatal("unknown optional coverage fabricated")
	}
	notes := notesCoverage(nil)
	docs := (&runtime{}).documentsCoverage(nil)
	if mergedOptionalCoverage(notes, nil) != notes || mergedOptionalCoverage(nil, docs) != docs {
		t.Fatal("single participating source scope replaced")
	}
	if mergedOptionalCoverage(notes, docs).State != "unknown" {
		t.Fatal("empty registrations became available")
	}
	notes.RefreshError = &syncError{Code: "notes-failed", Message: "notes unavailable"}
	docs.RefreshError = &syncError{Code: "docs-failed", Message: "documents unavailable"}
	joined := mergedOptionalCoverage(notes, docs)
	if joined.RefreshError == nil || !strings.Contains(joined.RefreshError.Message, "documents unavailable") ||
		notes.RefreshError.Message != "notes unavailable" {
		t.Fatal("optional failure merge discarded or mutated a peer's failure")
	}
	notes.RefreshError = nil
	joined = mergedOptionalCoverage(notes, docs)
	if joined.RefreshError == nil || joined.RefreshError.Code != "docs-failed" {
		t.Fatal("single document refresh failure dropped")
	}
}

func TestDocumentsCLIMissingArchiveAndNoSourceRemainIndependent(t *testing.T) {
	e := newEnv(t)
	missing := filepath.Join(t.TempDir(), "missing.db")
	code, stdout, stderr := e.run("--db", missing, "--no-sync", "docs", "sources")
	if code != 0 || !strings.Contains(stdout, "docs sync") {
		t.Fatalf("absent document sources lost next action: %s", stderr)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only sources created an archive")
	}
	code, stdout, stderr = e.run("--no-sync", "docs", "search", "words")
	if code != 0 || !strings.Contains(stdout, "docs sync") {
		t.Fatalf("unregistered search required legacy apps: %s", stderr)
	}
}
