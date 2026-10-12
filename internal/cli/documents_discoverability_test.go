package cli

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

// seedDocumentSourcesCLI registers a healthy Word recent source, an Excel shared source whose
// later capture failed, and a recommended source that never captured anything.
func seedDocumentSourcesCLI(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	commit := func(locator string, surface officedocuments.Surface, application, title string) {
		a, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.DocumentKind(surface), Platform: "synthetic",
			Locator: locator, At: at}, surface, application)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.CommitDocumentObservations(ctx, a, store.KnowledgeOutcome{Status: "complete", ContentDigest: locator,
			Qualification: store.DocumentQualification, At: at}, officedocuments.Result{Documents: []officedocuments.Document{{
			Surface: surface, Title: title, URL: "https://example.invalid/" + application,
			Modified: officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: at}}}},
			store.DocumentCaptureMetadata{Surface: surface, Application: application, AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
	fail := func(locator string, surface officedocuments.Surface, application string) {
		a, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: store.DocumentKind(surface), Platform: "synthetic",
			Locator: locator, At: at.Add(time.Hour)}, surface, application)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.RecordKnowledgeFailure(ctx, a, store.KnowledgeOutcome{Status: "failed", Reason: "office_json_unrecognized",
			At: at.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	commit("/fictional/aggmru/account-a/w-mru4-en-US-sr.json", officedocuments.Recent, "Word", "Synthetic Word recent")
	commit("/fictional/aggmru/account-a/x-swm-en-US-sr.json", officedocuments.Shared, "Excel", "Synthetic Excel shared")
	fail("/fictional/aggmru/account-a/x-swm-en-US-sr.json", officedocuments.Shared, "Excel")
	fail("/fictional/aggmru/account-a/x-rec-en-US-sr.json", officedocuments.Recommended, "Excel")
}

func sourceByKind(t *testing.T, doc map[string]any, kind string) map[string]any {
	t.Helper()
	for _, item := range items(t, doc) {
		if item["kind"] == kind {
			return item
		}
	}
	t.Fatalf("no %s source in %v", kind, doc)
	return nil
}

func TestDocumentsFailedSourcesHaveIdentityRemedyAndEffect(t *testing.T) {
	e := newEnv(t)
	seedDocumentSourcesCLI(t, e)
	code, stdout, stderr := e.run("--max-age", "0", "docs", "sources")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	never := sourceByKind(t, doc, "office_recommended")
	if never["source_id"] != nil || never["locator_tail"] != "account-a/x-rec-en-US-sr.json" ||
		!strings.Contains(never["fix"].(string), "github.com/ourostack/m365crawl/issues") ||
		!strings.Contains(never["fix"].(string), "Upgrade") ||
		!strings.Contains(never["effect"].(string), "no listed documents") {
		t.Fatalf("never-captured failed source lacks identity, fix or effect: %v", never)
	}
	kept := sourceByKind(t, doc, "office_shared")
	if kept["source_id"] == nil || !strings.Contains(kept["effect"].(string), "last good capture stays current") {
		t.Fatalf("failed source with a last good capture does not say it stays current: %v", kept)
	}
	healthy := sourceByKind(t, doc, "office_recent")
	if healthy["fix"] != nil || healthy["effect"] != nil {
		t.Fatalf("healthy source carries a remedy: %v", healthy)
	}
	code, stdout, stderr = e.run("--max-age", "0", "--format", "text", "docs", "sources")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"kind", "office_recommended", "account-a/x-rec-en-US-sr.json", "account-a/x-swm-en-US-sr.json",
		"fix: ", "last good capture stays current", "no listed documents"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("text docs sources lacks %q:\n%s", want, stdout)
		}
	}
}

func TestDocumentsEveryDocumentedFailureReasonHasARemedy(t *testing.T) {
	commands, err := os.ReadFile("../../docs/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, reason := range documentFailureReasons {
		if documentFailureFix[reason] == "" || !strings.Contains(string(commands), "`"+reason+"`") {
			t.Fatalf("failure reason %s lacks a remedy or a docs/commands.md entry", reason)
		}
	}
	if len(documentFailureFix) != len(documentFailureReasons) {
		t.Fatal("a remedy exists for an undocumented reason")
	}
}

func TestDocumentsHelpDefinesTermsClocksAndOrder(t *testing.T) {
	e := newEnv(t)
	_, stdout, _ := e.run("docs", "--help")
	for _, want := range []string{"dialog:", "recent:", "recommended:", "shared:", "association_state", "boundary",
		"Sharing.Type", "modified_at", "captured_at", "source_observed_at", "Order:", "null modified_at"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("docs --help lacks %q:\n%s", want, stdout)
		}
	}
	seedDocumentSourcesCLI(t, e)
	code, stdout, stderr := e.run("--max-age", "0", "--format", "text", "docs", "show", "document-observation:1")
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"surface recent:", "association unresolved:", "modified_at", "captured_at", "source_observed_at"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("docs show text lacks %q:\n%s", want, stdout)
		}
	}
}

func TestDocumentsApplicationAndSurfaceFilters(t *testing.T) {
	e := newEnv(t)
	seedDocumentSourcesCLI(t, e)
	for _, tt := range []struct {
		args  []string
		title string
		count int
	}{
		{[]string{"docs", "list", "--application", "word"}, "Synthetic Word recent", 1},
		{[]string{"docs", "list", "--application", "Excel"}, "Synthetic Excel shared", 1},
		{[]string{"docs", "list", "--surface", "shared"}, "Synthetic Excel shared", 1},
		{[]string{"docs", "list", "--application", "PowerPoint"}, "", 0},
		{[]string{"docs", "search", "--surface", "recent"}, "Synthetic Word recent", 1},
		{[]string{"docs", "search", "Synthetic", "--application", "Word", "--surface", "shared"}, "", 0},
	} {
		code, stdout, stderr := e.run(append([]string{"--max-age", "0"}, tt.args...)...)
		if code != 0 {
			t.Fatalf("%v: %s", tt.args, stderr)
		}
		got := items(t, decode(t, stdout))
		if len(got) != tt.count || (tt.count == 1 && got[0]["title"] != tt.title) {
			t.Fatalf("%v = %v", tt.args, got)
		}
	}
	for _, tt := range []struct{ flag, value, want string }{
		{"--application", "Visio", "Word, Excel, PowerPoint"},
		{"--surface", "pinned", "recent, shared, recommended, dialog"},
	} {
		code, _, stderr := e.run("--max-age", "0", "docs", "list", tt.flag, tt.value)
		if code == 0 || !strings.Contains(stderr, tt.want) {
			t.Fatalf("%s %s accepted or unexplained: %s", tt.flag, tt.value, stderr)
		}
	}
	_, stdout, _ := e.run("docs", "list", "--help")
	if !strings.Contains(stdout, "--application") || !strings.Contains(stdout, "--surface") {
		t.Fatalf("docs list --help lacks the filters:\n%s", stdout)
	}
}

// --application and --surface narrow observation rows only; source_coverage still lists every source.
func TestDocumentFilterHelpSaysCoverageIsUnfiltered(t *testing.T) {
	for _, command := range []any{documentsListCmd{}, documentsSearchCmd{}} {
		kind := reflect.TypeOf(command)
		for _, name := range []string{"Application", "Surface"} {
			field, _ := kind.FieldByName(name)
			if help := field.Tag.Get("help"); !strings.Contains(help, "not source_coverage") {
				t.Errorf("%s.%s help = %q", kind.Name(), name, help)
			}
		}
	}
}
