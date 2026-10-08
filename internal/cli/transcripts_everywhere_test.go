package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// failTranscriptQuestion makes the tables or the text question fail for the rest of the test.
func failTranscriptQuestion(t *testing.T, which string) {
	t.Helper()
	oldTables, oldText := transcriptTablesOf, transcriptTextOf
	t.Cleanup(func() { transcriptTablesOf, transcriptTextOf = oldTables, oldText })
	fail := func(*store.Store, context.Context) (bool, error) { return false, errors.New("injected " + which) }
	if which == "tables" {
		transcriptTablesOf = fail
	} else {
		transcriptTextOf = fail
	}
}

func TestTranscriptQuestionsFailTheCommands(t *testing.T) {
	for _, c := range []struct {
		which string
		args  []string
	}{
		{"tables", []string{"search", "hello", "--source", "transcripts"}},
		{"text", []string{"search", "hello", "--source", "transcripts"}},
		{"tables", []string{"status"}},
		{"tables", []string{"calendar", "event", trEventID()}},
	} {
		t.Run(c.which+" "+c.args[0], func(t *testing.T) {
			e := trEnv(t)
			failTranscriptQuestion(t, c.which)
			code, _, errOut := e.tr(append([]string{"--json"}, c.args...)...)
			if code == 0 || !strings.Contains(errOut, "injected "+c.which) {
				t.Fatalf("exit %d: %s", code, errOut)
			}
		})
	}
}

func TestStatusTranscriptsBlock(t *testing.T) {
	e := trEnv(t)
	trSharePointHosts(e)
	m := trJSON(t, e, "status")
	tr, ok := m["transcripts"].(map[string]any)
	// call-1 (3 parts), call-2 (1), call-3 (1, a sharing link only), call-4 (1); P1, P2 and Q1 fetched.
	if !ok || tr["calls"] != float64(4) || tr["parts"] != float64(6) || tr["fetchable"] != float64(5) || tr["fetched"] != float64(3) || tr["last_fetch_at"] != "2026-11-09T08:00:00Z" {
		t.Fatalf("status transcripts %v", m["transcripts"])
	}
	code, out, errOut := e.tr("--format", "text", "status")
	if code != 0 || !strings.Contains(out, "4 recorded meetings, 6 parts, 3 of 5 fetchable parts fetched; last fetch 2026-11-09 08:00") {
		t.Fatalf("text: exit %d\n%s%s", code, out, errOut)
	}
	// whoami's archive block carries no transcripts.
	if w := trJSON(t, e, "whoami"); w["archive"].(map[string]any)["transcripts"] != nil {
		t.Fatalf("whoami: %v", w["archive"])
	}
	// A broken transcript table fails status.
	e.exec("alter table transcript_parts rename column ordinal to place")
	if code, _, errOut := e.tr("--json", "status"); code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("broken table: exit %d: %s", code, errOut)
	}
}

func TestStatusTranscriptsBlockAbsentWithoutTables(t *testing.T) {
	e := trEnv(t)
	for _, tbl := range []string{"transcript_fts", "transcript_entries", "transcript_fetches", "transcript_parts"} {
		e.exec("drop table " + tbl)
	}
	if m := trJSON(t, e, "status"); m["transcripts"] != nil {
		t.Fatalf("no tables: %v", m["transcripts"])
	}
}

func TestCalendarEventResolveFailureSurfaces(t *testing.T) {
	e := trEnv(t)
	e.exec("alter table transcript_parts rename column call_id to call")
	code, _, errOut := e.tr("--json", "calendar", "event", trEventID())
	if code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("a broken transcript table must fail the event: %d %s", code, errOut)
	}
}
