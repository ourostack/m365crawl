package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

func seedNotesCLI(t *testing.T, e *env) string {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	attempt, err := st.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{
		Kind: "notes", Platform: "synthetic", Locator: "/fictional/index.db", AcquisitionSignature: "metadata", At: at,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = st.CommitNotes(context.Background(), attempt, store.KnowledgeOutcome{
		Status: "complete", AcquisitionSignature: "metadata", ContentDigest: "bytes", Qualification: store.NotesQualification, At: at, SourceObservedAt: at,
	}, onenote.Result{
		Entities: []onenote.Entity{
			{RowID: 4, Type: 4, GOID: "notebook", GUID: "guid-notebook", Title: "Synthetic notebook"},
			{RowID: 1, Type: 1, GOID: "page", GUID: "guid-page", ParentGOID: "notebook", Title: "Quarterly plan notes", ModifiedAt: at},
		},
		Elements: []onenote.Element{{RowID: 8, EntityRowID: 1, GOID: "element", Text: "Quarterly plan from indexed notes", Jcid: 393230, Kind: "rich_text"}},
		Flags:    []onenote.Flag{{RowID: 9, ElementRowID: 8, Type: 1, Shape: 2, Status: 3, Label: "Synthetic priority"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := st.Notes(context.Background(), store.NoteFilter{})
	if err != nil || len(rows) != 1 {
		t.Fatal("notes seed failed")
	}
	return fmt.Sprintf("note:%d", rows[0].ID)
}

func TestNotesCLIIndependentOfflineListShowAndCoverage(t *testing.T) {
	e := newEnv(t)
	id := seedNotesCLI(t, e)
	for _, args := range [][]string{
		{"notes", "list"},
		{"notes", "search", "Quarterly"},
		{"notes", "show", id},
		{"notes", "sources"},
	} {
		code, stdout, stderr := e.run(append([]string{"--no-sync", "--outlook-root", "/irrelevant/bad"}, args...)...)
		if code != 0 {
			t.Fatalf("%v exit%d: %s", args, code, stderr)
		}
		doc := decode(t, stdout)
		if doc["source_coverage"] == nil {
			t.Fatalf("coverage missing outside items: %v", doc)
		}
		if doc["needs_sync"] != nil || doc["sync_error"] != nil {
			t.Fatal("notes-only path ran legacy freshness")
		}
	}
	code, stdout, stderr := e.run("--no-sync", "--fields", "id,title", "--max-text", "5", "notes", "list")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	if len(items(t, doc)[0]) != 2 || doc["source_coverage"] == nil {
		t.Fatal("field shaping hid source coverage")
	}
	code, stdout, stderr = e.run("--no-sync", "--format", "text", "notes", "list")
	if code != 0 || !strings.Contains(stdout, id) || !strings.Contains(stdout, "indexed") {
		t.Fatalf("text notes: %d/%s/%s", code, stdout, stderr)
	}
}

func TestNotesSearchOnlyBypassesLegacyAndMixedParticipationMatrix(t *testing.T) {
	e := newEnv(t)
	id := seedNotesCLI(t, e)
	code, stdout, stderr := e.run("--no-sync", "--outlook-root", "/irrelevant/bad", "search", "Quarterly", "--source", "notes")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	rows := items(t, doc)
	if len(rows) != 1 || rows[0]["source"] != "notes" || rows[0]["id"] != id || doc["source_coverage"] == nil {
		t.Fatalf("notes-only shared search: %v", doc)
	}
	for _, flag := range [][]string{
		{"--account", "tenant/user"}, {"--from", "someone"}, {"--team", "team"},
		{"--conversation", "thread"}, {"--folder", "Inbox"}, {"--mentions-me"},
		{"--direct-mentions"}, {"--include-system"}, {"--include-deleted"}, {"--html"},
	} {
		args := append([]string{"--no-sync", "search", "Quarterly", "--source", "notes"}, flag...)
		if code, _, stderr := e.run(args...); code != 2 {
			t.Fatalf("explicit notes accepted %v: %d/%s", flag, code, stderr)
		}
	}
	for _, flag := range [][]string{{"--account", "tenant/user"}, {"--from", "someone"}, {"--mentions-me"}, {"--folder", "Inbox"}} {
		args := append([]string{"--no-sync", "search", "Quarterly"}, flag...)
		code, stdout, stderr := e.run(args...)
		if code != 0 {
			t.Fatalf("all-source narrowing %v: %d/%s", flag, code, stderr)
		}
		doc := decode(t, stdout)
		for _, row := range items(t, doc) {
			if row["source"] == "notes" {
				t.Fatal("unresolved notes leaked into ownership/author filter")
			}
		}
		if !strings.Contains(fmt.Sprint(doc["notices"]), "notes") {
			t.Fatalf("notes exclusion notice missing for %v", flag)
		}
	}
}

func TestNotesCLIErrorsEmptyNoMatchesAndHelp(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("--no-sync", "notes", "list")
	if code != 0 || decode(t, stdout)["note"] == nil {
		t.Fatalf("never-captured state: %d/%s/%s", code, stdout, stderr)
	}
	seedNotesCLI(t, e)
	code, stdout, stderr = e.run("--no-sync", "notes", "search", "nomatchword")
	if code != 0 || decode(t, stdout)["source_coverage"] == nil || len(items(t, decode(t, stdout))) != 0 {
		t.Fatalf("no-hit coverage: %d/%s/%s", code, stdout, stderr)
	}
	for _, args := range [][]string{
		{"notes", "show", "bad"}, {"notes", "show", "note:0"}, {"notes", "show", "note:999"},
		{"notes", "list", "--source", "-1"}, {"notes", "list", "--limit", "0"},
		{"--account", "tenant/user", "notes", "list"},
	} {
		if code, _, stderr := e.run(append([]string{"--no-sync"}, args...)...); code == 0 {
			t.Fatalf("invalid note request accepted %v: %s", args, stderr)
		}
	}
	for _, args := range [][]string{{"notes", "--help"}, {"notes", "list", "--help"}, {"notes", "sync", "--help"}} {
		code, stdout, stderr := e.run(args...)
		if code != 0 || !strings.Contains(stdout, "notes") {
			t.Fatalf("notes help %v: %d/%s/%s", args, code, stdout, stderr)
		}
	}
}

func TestNotesSearchParticipationNeverClaimsChatOnlyTotal(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--no-sync", "search", "Fixture", "--limit", "1")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	if _, exists := doc["total"]; exists || doc["truncated"] != true || doc["source_coverage"] == nil {
		t.Fatal("participating unknown notes produced a chat-only total")
	}
}

func TestNotesShowTextIncludesNativeDetailsNotLegacyFreshness(t *testing.T) {
	e := newEnv(t)
	id := seedNotesCLI(t, e)
	code, stdout, stderr := e.run("--no-sync", "--format", "text", "notes", "show", id)
	if code != 0 {
		t.Fatal(stderr)
	}
	for _, want := range []string{"Synthetic notebook", "element 8", "Synthetic priority", "notes coverage"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("native note detail %q missing: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, "never synced") {
		t.Fatal("notes-only text claimed a missing legacy sync")
	}
}
