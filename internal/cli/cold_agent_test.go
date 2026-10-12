package cli

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

// seedPeopleCLI registers one native Outlook people source with one observation.
func seedPeopleCLI(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	a, err := s.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "outlook_people", Platform: "synthetic",
		Locator: "/fixture/Profile/HxStore.hxd", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPeopleObservations(ctx, a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "one",
		Qualification: store.PeopleQualification, At: at}, outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2,
		Key: 1, DisplayName: "Native Fixture", Email: "native@example.invalid"}}},
		store.PeopleCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

// A cold agent learns every data kind from the overview: each has a row with counts and freshness.
func TestOverviewReportsEveryDataKind(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	teamsArchive(t, e)
	seedDocumentSourcesCLI(t, e)
	seedNotesCLI(t, e)
	seedPeopleCLI(t, e)
	code, out, stderr := e.run("--json")
	if code != 0 {
		t.Fatal(stderr)
	}
	sources := map[string]map[string]any{}
	for _, s := range decode(t, out)["sources"].([]any) {
		src := s.(map[string]any)
		sources[src["source"].(string)] = src
	}
	for _, kind := range []string{"chats", "mail", "calendar", "documents", "notes", "people", "transcripts"} {
		if sources[kind] == nil {
			t.Fatalf("overview lacks %s: %s", kind, out)
		}
	}
	docs := sources["documents"]
	if docs["state"] != "ok" || docs["documents"] != float64(2) || docs["registered_sources"] != float64(3) ||
		docs["failed_sources"] != float64(2) || docs["last_sync_at"] == nil || !strings.Contains(docs["note"].(string), "docs sources") {
		t.Fatalf("documents row = %v", docs)
	}
	if notes := sources["notes"]; notes["state"] != "ok" || notes["notes"] != float64(1) || notes["registered_sources"] != float64(1) {
		t.Fatalf("notes row = %v", notes)
	}
	people := sources["people"]
	if people["state"] != "ok" || people["people"] == nil || people["observations"] != float64(1) {
		t.Fatalf("people row = %v", people)
	}
	if tr := sources["transcripts"]; tr["state"] != "empty" || tr["calls"] != float64(0) {
		t.Fatalf("transcripts row = %v", tr)
	}
	code, out, _ = e.run("--format", "text")
	for _, want := range []string{"documents", "2 document observations from 3 sources; 2 failed", "notes", "1 note from 1 source",
		"native Outlook observation", "transcripts", "m365crawl docs list", `m365crawl notes search "words"`, "m365crawl people "} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("text overview lacks %q:\n%s", want, out)
		}
	}
}

func TestOverviewWithoutOptionalSourcesSaysHowToAddThem(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	teamsArchive(t, e)
	for i, want := range map[int]string{3: "docs sync", 4: "notes sync"} {
		src := overviewSourceOf(t, e, i)
		if src["state"] != "not_registered" || !strings.Contains(src["note"].(string), want) {
			t.Fatalf("source %d = %v", i, src)
		}
	}
	e = newEnv(t)
	code, out, _ := e.run("--json")
	if code != 0 || len(decode(t, out)["sources"].([]any)) != 7 {
		t.Fatalf("no-archive overview does not list every kind: %s", out)
	}
}

// Unscoped people says what it mixed in and the ownership caveat, in JSON and text.
func TestPeopleUnscopedSaysWhatItIncluded(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, out, stderr := e.run("--max-age", "0", "people")
	if code != 0 || !strings.Contains(out, "no native Outlook people sources are registered") {
		t.Fatalf("legacy unscoped people lacks its inclusion notice: %s %s", out, stderr)
	}
	seedPeopleCLI(t, e)
	code, out, stderr = e.run("--max-age", "0", "people")
	notices, _ := decode(t, out)["notices"].([]any)
	if code != 0 || len(notices) == 0 || !strings.Contains(notices[0].(string), "native Outlook people observations") ||
		!strings.Contains(notices[0].(string), "unresolved account ownership") {
		t.Fatalf("connected unscoped people lacks its inclusion notice: %s %s", out, stderr)
	}
	code, out, _ = e.run("--max-age", "0", "--format", "text", "people")
	if code != 0 || !strings.Contains(out, "notice: included Teams people") {
		t.Fatalf("text people lacks the notice:\n%s", out)
	}
}

func TestPeopleHelpExplainsRecencyIDsAndAccount(t *testing.T) {
	e := newEnv(t)
	_, out, _ := e.run("people", "--help")
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"last_seen_at", "not the last chat with you", "8:orgid:", "28:", "bot", "conversations",
		"messages --conversation", "whoami"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("people --help lacks %q:\n%s", want, out)
		}
	}
}

var zonedStamp = regexp.MustCompile(`\d{4}-\d\d-\d\d \d\d:\d\d UTC`)

// Text times name their zone, so a text day is never confused with JSON's UTC day.
func TestTextTimesNameTheirZone(t *testing.T) {
	e := textEnv(t)
	e.sync()
	seedDocumentSourcesCLI(t, e)
	seedNotesCLI(t, e)
	for _, args := range [][]string{{"docs", "list"}, {"notes", "list"}, {"people"}} {
		code, out, stderr := e.run(append([]string{"--max-age", "0", "--format", "text"}, args...)...)
		if code != 0 || !zonedStamp.MatchString(out) {
			t.Fatalf("%v text times lack the zone:\n%s%s", args, out, stderr)
		}
	}
	if got := stamp(time.Date(2026, 10, 12, 7, 2, 0, 0, time.UTC)); got != "2026-10-12 07:02 UTC" {
		t.Fatalf("stamp = %q", got)
	}
}

func TestOverviewTranscriptsAndEmptyOrScopedSources(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	seedTranscripts(t, e)
	tr := overviewSourceOf(t, e, 6)
	if tr["state"] != "ok" || tr["calls"] == float64(0) || tr["fetched"] == float64(0) || tr["note"] != nil {
		t.Fatalf("transcripts row = %v", tr)
	}
	if _, out, _ := e.run("--format", "text"); !strings.Contains(out, "recorded meetings, ") || !strings.Contains(out, " fetched") {
		t.Fatalf("text transcripts row:\n%s", out)
	}
	e.exec("delete from transcript_fetches")
	if tr := overviewSourceOf(t, e, 6); tr["fetched"] != float64(0) || !strings.Contains(tr["note"].(string), "transcripts fetch") {
		t.Fatalf("unfetched transcripts row = %v", tr)
	}

	e = newEnv(t)
	teamsArchive(t, e)
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	a, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: "office_recent", Platform: "synthetic",
		Locator: "/fictional/recent.json", At: at}, "recent", "supplied")
	if err == nil {
		err = st.CommitDocumentObservations(ctx, a, store.KnowledgeOutcome{Status: "complete", Reason: "office_response_listed_no_documents",
			ContentDigest: "empty", Qualification: store.DocumentQualification, At: at}, officedocuments.Result{},
			store.DocumentCaptureMetadata{Surface: "recent", Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at})
	}
	if err := errors.Join(err, st.Close()); err != nil {
		t.Fatal(err)
	}
	if docs := overviewSourceOf(t, e, 3); docs["state"] != "empty" || docs["documents"] != float64(0) {
		t.Fatalf("registered empty documents row = %v", docs)
	}
	if docs := overviewSourceOf(t, e, 3, "--account", tenantA+"/"+userA); !strings.Contains(docs["note"].(string), "not narrowed by --account") {
		t.Fatalf("scoped documents row = %v", docs)
	}
}
