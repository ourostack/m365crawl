package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func seedDocumentsCLI(t *testing.T, e *env) string {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	a, err := st.BeginDocumentAttempt(context.Background(), store.KnowledgeInput{
		Kind: "office_shared", Platform: "synthetic", Locator: "/fictional/shared.json", At: at},
		officedocuments.Shared, "supplied")
	if err != nil {
		t.Fatal(err)
	}
	err = st.CommitDocumentObservations(context.Background(), a, store.KnowledgeOutcome{
		Status: "complete", ContentDigest: "synthetic", Qualification: store.DocumentQualification, At: at},
		officedocuments.Result{Documents: []officedocuments.Document{{Ordinal: 0, Surface: officedocuments.Shared,
			Title: "Quarterly shared plan", URL: "https://example.invalid/plan",
			Modified: officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: at},
			Sharing:  &officedocuments.Sharing{DisplayName: "Synthetic sharer", Email: "sharer@example.invalid"}}}},
		store.DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	return "document-observation:1"
}

func TestDocumentsCLIIndependentOfflineListShowAndCoverage(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	for _, args := range [][]string{{"docs", "list"}, {"docs", "search", "Quarterly"},
		{"docs", "show", id}, {"docs", "sources"}} {
		code, stdout, stderr := e.run(append([]string{"--no-sync", "--outlook-root", "/irrelevant/bad"}, args...)...)
		if code != 0 {
			t.Fatalf("document-only command failed %v: %s", args, stderr)
		}
		doc := decode(t, stdout)
		if doc["source_coverage"] == nil || doc["needs_sync"] != nil || doc["sync_error"] != nil {
			t.Fatal("document-only command invoked legacy freshness or hid coverage")
		}
		if len(items(t, doc)) != 1 {
			t.Fatal("registered document-only command did not return bounded result")
		}
	}
	code, stdout, stderr := e.run("--no-sync", "--fields", "id,title", "--max-text", "5", "docs", "list", "--limit", "0")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	if len(items(t, doc)[0]) != 2 || items(t, doc)[0]["id"] != id || doc["source_coverage"] == nil {
		t.Fatal("field shaping lost stable ID or coverage")
	}
	code, stdout, stderr = e.run("--no-sync", "--format", "text", "docs", "show", id)
	if code != 0 || !strings.Contains(stdout, id) || !strings.Contains(stdout, "not_captured") {
		t.Fatalf("document detail lacks actionable text state: %s %s", stdout, stderr)
	}
}

func TestDocumentsCLIHelpAndRefusalDoNotRequireSkill(t *testing.T) {
	e := newEnv(t)
	for _, command := range []string{"docs", "docs sync", "docs show", "docs sources"} {
		args := append(strings.Fields(command), "--help")
		code, stdout, stderr := e.run(args...)
		if code != 0 || !strings.Contains(stdout, "document") {
			t.Fatalf("cold command help missing: %s %s", command, stderr)
		}
	}
	for _, id := range []string{"1", "document-observation:0", "document-observation:01", "document-observation:9223372036854775808"} {
		code, _, stderr := e.run("--no-sync", "docs", "show", id)
		if code != 2 || !strings.Contains(stderr, "document-observation:") {
			t.Fatal("invalid follow-up lacked usable ID guidance")
		}
	}
	code, _, stderr := e.run("--no-sync", "docs", "show", "document-observation:999")
	if code != 2 || !strings.Contains(stderr, "docs list") {
		t.Fatal("missing observation did not point to a usable next command")
	}
	code, stdout, stderr := e.run("--no-sync", "docs", "list")
	if code != 0 || !strings.Contains(stdout, "docs sync") {
		t.Fatalf("empty unregistered archive lacked registration guidance: %s", stderr)
	}
}

func TestDocumentsSharedSearchOnlyAndUnsupportedFilterMatrix(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	code, stdout, stderr := e.run("--no-sync", "--outlook-root", "/irrelevant/bad", "search", "Quarterly", "--source", "documents")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	hits := items(t, doc)
	if len(hits) != 1 || hits[0]["id"] != id || hits[0]["source"] != "documents" || doc["source_coverage"] == nil {
		t.Fatal("document shared search did not bypass legacy freshness or return stable follow-up")
	}
	for _, flag := range [][]string{
		{"--account", "tenant/user"}, {"--from", "someone"}, {"--conversation", "thread"}, {"--team", "team"},
		{"--folder", "Inbox"}, {"--mentions-me"}, {"--direct-mentions"}, {"--include-system"}, {"--include-deleted"}, {"--html"},
	} {
		args := append([]string{"--no-sync", "search", "Quarterly", "--source", "documents"}, flag...)
		code, _, stderr := e.run(args...)
		if code != 2 || !strings.Contains(stderr, "flag_source_conflict") {
			t.Fatalf("unsupported document filter accepted: %v %s", flag, stderr)
		}
	}
	code, stdout, stderr = e.run("--no-sync", "--fields", "id,title", "search", "no-match", "--source", "documents")
	if code != 0 || len(items(t, decode(t, stdout))) != 0 || !strings.Contains(stdout, "source_coverage") {
		t.Fatalf("zero-hit projected coverage lost: %s", stderr)
	}
}
