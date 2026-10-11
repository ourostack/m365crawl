package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsSharedSearchRejectsEmptyWordsWithoutDate(t *testing.T) {
	e := newEnv(t)
	if code, _, stderr := e.run("--max-age", "0", "search", "--source", "documents"); code != 2 ||
		!strings.Contains(stderr, "docs list") {
		t.Fatal("empty metadata search lacked unfiltered-list remedy")
	}
}

func TestDocumentsSharedSearchProjectionAndCoverageErrorsStayErrors(t *testing.T) {
	for _, fault := range []string{
		`alter table document_observations rename column title to broken`,
		`alter table knowledge_locators rename column kind to broken`,
	} {
		e := newEnv(t)
		seedDocumentsCLI(t, e)
		e.exec(fault)
		if code, _, stderr := e.run("--max-age", "0", "search", "Quarterly", "--source", "documents"); code == 0 ||
			!strings.Contains(stderr, "db_error") {
			t.Fatal("document shared query/source integrity failure became empty healthy result")
		}
	}
}

func TestDocumentsMixedSearchCancellationStopsRatherThanServingRetained(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{}, context.Canceled
	}
	if code, _, stderr := e.run("search", "Quarterly"); code == 0 {
		t.Fatalf("cancelled mixed metadata refresh served success: %s", stderr)
	}
}

func TestDocumentsTypedHumanCommandsKeepFollowUpAndCoverage(t *testing.T) {
	e := newEnv(t)
	seedDocumentsCLI(t, e)
	for _, command := range [][]string{{"docs", "list"}, {"search", "Quarterly", "--source", "documents"}} {
		code, stdout, stderr := e.run(append([]string{"--max-age", "0", "--format", "text"}, command...)...)
		if code != 0 || !strings.Contains(stdout, "document-observation:1") ||
			!strings.Contains(stdout, "documents coverage:") ||
			(!strings.Contains(stdout, "not_captured") && !strings.Contains(stdout, "metadata only")) {
			t.Fatalf("typed metadata text lost usable ID/state/source context: %s %s", stdout, stderr)
		}
	}
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	runDocumentsSync = func(context.Context, syncer.DocumentOptions) (syncer.DocumentReport, error) {
		return syncer.DocumentReport{Captured: 1}, nil
	}
	if code, stdout, stderr := e.run("--format", "text", "docs", "sync", "--root", "/synthetic"); code != 0 ||
		!strings.Contains(stdout, "documents: 1 captured") {
		t.Fatalf("metadata capture rendered as notes: %s %s", stdout, stderr)
	}
}
