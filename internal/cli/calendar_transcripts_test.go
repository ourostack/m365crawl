package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// trSecondCall adds a second recorded call to the weekly occurrence of 2026-11-03 (call-5, one
// part, fetched), so that occurrence holds a partial call and a fetched one.
func trSecondCall(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	e.exec(`insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at) values('` + tenantA + `','` + userA + `','` + trWeekly +
		`','1005','2026-11-03T10:58:00.000Z','` + transcripts.TypeRecording + `','` + trNotice("call-5", "0", "Recording+Transcript", "2026-11-03T10:45:00Z", "drive:S1") + `','2026-11-03T10:58:00.000Z')`)
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	sess, err := st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.DeriveTranscriptParts(ctx, []string{trAccount}); err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit(); err != nil {
		t.Fatal(err)
	}
	r := transcripts.FetchResult{State: transcripts.StateOK, HTTPStatus: 200, Browser: "edge", At: trFetchedAt.Add(-24 * 60 * 60 * 1e9),
		Entries: []transcripts.Entry{{Speaker: "Ada Example", StartMS: trMS(0), Text: "A last word."}}}
	if err := st.SaveTranscript(ctx, trAccount, "d:b!d1/S1", r); err != nil {
		t.Fatal(err)
	}
}

func eventTranscripts(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, ok := m["transcripts"].([]any)
	if !ok {
		t.Fatalf("no transcripts: %v", m["transcripts"])
	}
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func TestCalendarEventTranscripts(t *testing.T) {
	e := trEnv(t)
	trSecondCall(t, e)
	m := trJSON(t, e, "calendar", "event", trEventID())
	got := eventTranscripts(t, m)
	if len(got) != 2 {
		t.Fatalf("transcripts %v", got)
	}
	want := []map[string]any{
		{"call_id": "call-5", "source": "archive", "state": "fetched", "parts_total": float64(1), "parts_fetchable": float64(1), "parts_fetched": float64(1), "last_fetched_at": "2026-11-08T08:00:00Z"},
		{"call_id": "call-1", "source": "archive", "state": "partial", "parts_total": float64(3), "parts_fetchable": float64(3), "parts_fetched": float64(2), "last_fetched_at": "2026-11-09T08:00:00Z"},
	}
	for i, w := range want {
		for k, v := range w {
			if got[i][k] != v {
				t.Errorf("call %d %s = %v, want %v", i, k, got[i][k], v)
			}
		}
		if len(got[i]) != len(w) {
			t.Errorf("call %d keys %v", i, got[i])
		}
	}
	notice := "1 of 4 transcript parts are not fetched; run m365crawl transcripts fetch " + trEventID()
	if !strings.Contains(strings.Join(noticesOf(m), "\n"), notice) {
		t.Fatalf("notices %v, want %q", m["notices"], notice)
	}
	// The text output has the calls and the notice.
	code, out, errOut := e.tr("--format", "text", "calendar", "event", trEventID())
	if code != 0 || !strings.Contains(out, "transcript call") || !strings.Contains(out, "call-5") || !strings.Contains(out, "2 of 3") || !strings.Contains(out, notice) {
		t.Fatalf("text: exit %d\n%s%s", code, out, errOut)
	}
}

func TestCalendarEventTranscriptsAllFetched(t *testing.T) {
	e := trEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTranscript(context.Background(), trAccount, "d:b!d1/P3", transcripts.FetchResult{State: transcripts.StateOK, At: trFetchedAt}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	m := trJSON(t, e, "calendar", "event", trEventID())
	if got := eventTranscripts(t, m); len(got) != 1 || got[0]["state"] != "fetched" {
		t.Fatalf("transcripts %v", got)
	}
	if strings.Contains(strings.Join(noticesOf(m), "\n"), "transcript parts") {
		t.Fatalf("nothing left to fetch, no notice: %v", m["notices"])
	}
}

func TestCalendarEventNoTranscripts(t *testing.T) {
	e := trEnv(t)
	// An occurrence with no recorded call: the field is omitted and nothing is said.
	e.exec(`delete from messages where conversation_id='` + trWeekly + `'`)
	m := trJSON(t, e, "calendar", "event", trEventID())
	if _, has := m["transcripts"]; has || strings.Contains(strings.Join(noticesOf(m), "\n"), "transcript") {
		t.Fatalf("no recorded call: %v %v", m["transcripts"], m["notices"])
	}
	// An archive from before the transcript tables: the same.
	for _, tbl := range []string{"transcript_fts", "transcript_entries", "transcript_fetches", "transcript_parts"} {
		e.exec("drop table " + tbl)
	}
	if m := trJSON(t, e, "calendar", "event", trEventID()); m["transcripts"] != nil {
		t.Fatalf("no tables: %v", m["transcripts"])
	}
}

func TestCalendarEventFieldsTranscripts(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "calendar", "event", trEventID(), "--fields", "event_id,transcripts")
	if got := eventTranscripts(t, m); len(got) != 1 || got[0]["call_id"] != "call-1" || m["subject"] != nil {
		t.Fatalf("fields: %v", m)
	}
	// The agenda carries no transcripts: the key points at calendar event.
	er := trFails(t, e, "usage", 2, "calendar", "--fields", "transcripts")
	if !strings.Contains(er["message"].(string), "calendar event") {
		t.Fatalf("agenda: %v", er)
	}
}

func TestCalendarEventTranscriptsFailuresSurface(t *testing.T) {
	e := trEnv(t)
	e.exec("alter table transcript_parts rename column ordinal to place")
	code, _, errOut := e.tr("--json", "calendar", "event", trEventID())
	if code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("a broken transcript table must fail the event: %d %s", code, errOut)
	}
}

// noticesOf is a result's notices as strings.
func noticesOf(m map[string]any) []string {
	var out []string
	raw, _ := m["notices"].([]any)
	for _, n := range raw {
		out = append(out, n.(string))
	}
	return out
}
