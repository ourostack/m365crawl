package cli

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

const (
	trAccount = tenantA + "/" + userA
	trWeekly  = "19:meeting_weekly@thread.v2"
	trAdhoc   = "19:meeting_adhoc@thread.v2"
)

var trFetchedAt = time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)

// trNotice is a synthetic Success recording notice: with a drive item when ref is "drive:<item>",
// with only a sharing link when it is "share:<token>".
func trNotice(call, chunk, types, stamp, ref string) string {
	var link, item string
	switch kind, id, _ := strings.Cut(ref, ":"); kind {
	case "drive":
		item = `<item type="onedriveForBusinessTranscript" uri="https://tenant.sharepoint.example.invalid/teams/site-a/_api/v2.1/drives/b!d1/items/` + id + `/versions/current/media/transcripts/tr-` + id + `/content"/>`
	case "share":
		link = `<a href="https://tenant.sharepoint.example.invalid/:v:/t/site-a/` + id + `">Play</a>`
	}
	return `<URIObject type="Video.2/CallRecording.1">` + link + `<Identifiers><Id type="callId" value="` + call + `"/><Id type="chunkIndex" value="` + chunk + `"/></Identifiers>` +
		`<RecordingStatus status="Success"/><RecordingContent contentTypes="` + types + `" timestamp="` + stamp + `" duration="0:30:00.000">` + item + `</RecordingContent>` +
		`<RecordingStorage type="SharedSharePoint"/></URIObject>`
}

func trMS(n int64) *int64 { return &n }

// trEvent is the weekly meeting's occurrence of 2026-11-03, 10:00 to 11:00 UTC.
func trEvent() calendar.Event {
	start := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	return calendar.Event{UnknownDeclared: true, Source: calendar.SourceTeams, AccountID: trAccount, SourceID: "weekly-1103", GlobalID: "uid-weekly-1103",
		ICalUID: "uid-weekly-1103", EventType: calendar.EventSingle, Subject: "Weekly sync", Start: start, End: start.Add(time.Hour),
		AllDay: calendar.TriFalse, Cancelled: calendar.TriFalse, IsOnlineMeeting: calendar.TriTrue, TeamsThreadID: trWeekly, Response: "accepted"}
}

// trEventID is what calendar prints for the weekly occurrence.
func trEventID() string { return store.EventID(trAccount, calendar.Key(trEvent())) }

// seedTranscripts builds an archive with no sync: a synced run, the weekly meeting's chat and its
// 2026-11-03 occurrence, and recording notices for four calls in every state:
//   - call-1 (weekly, Nov 3): part 1 transcribe-only and fetched, part 2 fetched, part 3 not fetched;
//   - call-2 (weekly, Nov 10): one part, fetched;
//   - call-3 (an ad hoc call with no chat name): only a sharing link;
//   - call-4 (ad hoc, Nov 12): one part, not fetched.
func seedTranscripts(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	e.emptyArchive()
	rec := transcripts.TypeRecording
	e.exec(`insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('` + tenantA + `','` + userA + `','` + trWeekly + `','Meeting','','Weekly sync','2026-11-01T00:00:00.000Z')`)
	for _, m := range []struct{ thread, id, sent, html string }{
		{trWeekly, "1001", "2026-11-03T10:31:00.000Z", trNotice("call-1", "10001", "Transcript", "2026-11-03T10:00:00Z", "drive:P1")},
		{trWeekly, "1002", "2026-11-03T11:05:00.000Z", trNotice("call-1", "0", "Recording+Transcript", "2026-11-03T10:02:00Z", "drive:P2")},
		{trWeekly, "1003", "2026-11-03T11:06:00.000Z", trNotice("call-1", "1", "Recording+Transcript", "2026-11-03T10:40:00Z", "drive:P3")},
		{trWeekly, "2001", "2026-11-10T11:05:00.000Z", trNotice("call-2", "0", "Recording+Transcript", "2026-11-10T10:00:00Z", "drive:Q1")},
		{trAdhoc, "3001", "2026-11-11T15:05:00.000Z", trNotice("call-3", "0", "Recording+Transcript", "2026-11-11T14:00:00Z", "share:TOKEN")},
		{trAdhoc, "4001", "2026-11-12T15:05:00.000Z", trNotice("call-4", "0", "Recording+Transcript", "2026-11-12T14:00:00Z", "drive:R1")},
	} {
		e.exec(`insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at) values('` + tenantA + `','` + userA + `','` + m.thread + `','` + m.id + `','` + m.sent + `','` + rec + `','` + m.html + `','` + m.sent + `')`)
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	w := calendar.Window{Source: calendar.SourceTeams, AccountID: trAccount, Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
		SyncedAt: trFetchedAt, CacheFreshAt: trFetchedAt}
	if _, err := calendar.ApplySnapshot(ctx, db, w, []calendar.Event{trEvent()}, trFetchedAt); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
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
	ok := func(entries ...transcripts.Entry) transcripts.FetchResult {
		return transcripts.FetchResult{State: transcripts.StateOK, HTTPStatus: 200, Entries: entries, Browser: "edge", At: trFetchedAt}
	}
	for key, r := range map[string]transcripts.FetchResult{
		"d:b!d1/P1": ok(
			transcripts.Entry{Speaker: "Ada Example", StartMS: trMS(0), EndMS: trMS(2500), Text: "Good morning."},
			transcripts.Entry{Speaker: "Ada Example", StartMS: trMS(2500), EndMS: trMS(4000), Text: "Shall we start?"},
			transcripts.Entry{Speaker: "Bo Example", StartMS: trMS(65000), EndMS: trMS(66000), Text: "Yes, the agenda is short."}),
		"d:b!d1/P2": ok(
			transcripts.Entry{Speaker: "Bo Example", StartMS: trMS(3_725_500), EndMS: trMS(3_727_000), Text: "First item: the release."},
			transcripts.Entry{Text: "(inaudible)"}),
		"d:b!d1/Q1": ok(transcripts.Entry{Speaker: "Cy Example", StartMS: trMS(1000), EndMS: trMS(2000), Text: "Short one today."}),
	} {
		if err := st.SaveTranscript(ctx, trAccount, key, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RecordRun(ctx, store.Run{StartedAt: trFetchedAt, FinishedAt: trFetchedAt, Status: "ok", Accounts: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
}

func trEnv(t *testing.T) *env {
	t.Helper()
	e := textEnv(t)
	seedTranscripts(t, e)
	return e
}

func (e *env) tr(args ...string) (int, string, string) {
	return e.run(append([]string{"--max-age", "0"}, args...)...)
}

func trJSON(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.tr(append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func trFails(t *testing.T, e *env, wantCode string, wantExit int, args ...string) map[string]any {
	t.Helper()
	code, _, errOut := e.tr(append([]string{"--json"}, args...)...)
	if code != wantExit {
		t.Fatalf("%v: exit %d, want %d: %s", args, code, wantExit, errOut)
	}
	er := errorOf(t, errOut)
	if er["code"] != wantCode {
		t.Fatalf("%v: code %v, want %s: %v", args, er["code"], wantCode, er)
	}
	return er
}

func callIDs(its []map[string]any) string {
	var out []string
	for _, it := range its {
		out = append(out, it["call_id"].(string)+"="+it["state"].(string))
	}
	return strings.Join(out, " ")
}

var reArchiveAge = regexp.MustCompile(`"archive_age_seconds": \d+`)

// jsonGolden indents a JSON document and stands a fixed value in for the archive's age.
func jsonGolden(t *testing.T, out string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(out), "", "  "); err != nil {
		t.Fatal(err)
	}
	return reArchiveAge.ReplaceAllString(buf.String(), `"archive_age_seconds": 0`)
}

func TestTranscriptsListGolden(t *testing.T) {
	e := trEnv(t)
	checkTranscriptGoldens(t, e, "transcripts_list", "transcripts")
}

func TestTranscriptsMeetingGolden(t *testing.T) {
	e := trEnv(t)
	checkTranscriptGoldens(t, e, "transcripts_meeting", "transcripts", trWeekly)
}

func TestShowGolden(t *testing.T) {
	e := trEnv(t)
	checkTranscriptGoldens(t, e, "transcripts_show", "transcripts", "show", "call-1")
}

// checkTranscriptGoldens compares the plain, color and JSON output of one command with its goldens.
func checkTranscriptGoldens(t *testing.T, e *env, name string, args ...string) {
	t.Helper()
	for _, color := range []bool{false, true} {
		suffix := "plain"
		t.Setenv("CLICOLOR_FORCE", "")
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.tr(append([]string{"--format", "text"}, args...)...)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", name, code, errOut)
		}
		if !color && strings.Contains(out, "\x1b") {
			t.Errorf("%s: escape in plain output", name)
		}
		if color && !strings.Contains(out, "\x1b[") {
			t.Errorf("%s: no color", name)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if last := lines[len(lines)-1]; !strings.Contains(last, "source: archive") {
			t.Errorf("%s: the last line says where the result came from: %q", name, last)
		}
		checkGolden(t, name+"."+suffix, e.scrub(out))
	}
	code, out, errOut := e.tr(append([]string{"--json"}, args...)...)
	if code != 0 {
		t.Fatalf("%s: exit %d: %s", name, code, errOut)
	}
	checkGolden(t, name+".json", jsonGolden(t, out))
}

func TestTranscriptsList(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "transcripts")
	if m["source"] != "archive" || m["count"] != float64(4) || m["truncated"] != false {
		t.Fatalf("envelope %v", m)
	}
	its := items(t, m)
	if got := callIDs(its); got != "call-4=not_fetched call-3=unfetchable call-2=fetched call-1=partial" {
		t.Fatalf("calls %s", got)
	}
	c1 := its[3]
	if c1["thread_id"] != trWeekly || c1["title"] != "Weekly sync" || c1["event_key"] != calendar.Key(trEvent()) || c1["started_at"] != "2026-11-03T10:00:00Z" ||
		c1["parts_total"] != float64(3) || c1["parts_fetchable"] != float64(3) || c1["parts_fetched"] != float64(2) {
		t.Fatalf("call-1 %v", c1)
	}
	if _, ok := c1["parts"]; ok {
		t.Fatal("the list of every meeting carries no parts")
	}
	if its[1]["title"] != nil || its[1]["event_key"] != nil || its[1]["parts_fetchable"] != float64(0) {
		t.Fatalf("call-3 %v", its[1])
	}
	notices := m["notices"].([]any)
	if len(notices) != 1 || notices[0] != "2 of the 4 meetings listed have transcript parts that are not fetched yet; run m365crawl transcripts fetch <call-id>" {
		t.Fatalf("notices %v", notices)
	}
	for _, args := range [][]string{
		{"transcripts", "--state", "fetched"},
		{"transcripts", "--state", "unfetchable"},
		{"transcripts", "--since", "2026-11-10T00:00:00Z", "--until", "2026-11-12T00:00:00Z"},
		{"transcripts", "--limit", "1"},
		{"--account", trAccount, "transcripts", "--state", "partial"},
	} {
		m := trJSON(t, e, args...)
		got := callIDs(items(t, m))
		want := map[string]string{"fetched": "call-2=fetched", "unfetchable": "call-3=unfetchable", "2026-11-10T00:00:00Z": "call-3=unfetchable call-2=fetched", "1": "call-4=not_fetched", "partial": "call-1=partial"}[args[len(args)-1]]
		if args[len(args)-1] == "2026-11-12T00:00:00Z" {
			want = "call-3=unfetchable call-2=fetched"
		}
		if got != want {
			t.Errorf("%v: %s, want %s", args, got, want)
		}
		if args[len(args)-1] == "1" && m["truncated"] != true {
			t.Errorf("--limit 1 must be truncated")
		}
	}
	// A date alone as --until includes that whole day.
	if got := callIDs(items(t, trJSON(t, e, "transcripts", "--since", "2026-11-12T00:00:00Z", "--until", "2026-11-12"))); got != "call-4=not_fetched" {
		t.Fatalf("--until a day: %s", got)
	}
	// Every state fetched: no notice.
	if m := trJSON(t, e, "transcripts", "--state", "fetched"); m["notices"] != nil {
		t.Fatalf("notices %v", m["notices"])
	}
}

func TestTranscriptsMeeting(t *testing.T) {
	e := trEnv(t)
	for _, ref := range []string{"call-1", trWeekly, trEventID(), calendar.Key(trEvent()),
		"https://teams.microsoft.com/l/message/19%3Ameeting_weekly%40thread.v2/1700000046000?context=%7B%7D"} {
		m := trJSON(t, e, "transcripts", ref)
		its := items(t, m)
		want := "call-1=partial"
		if ref == trWeekly || strings.HasPrefix(ref, "https://") {
			want = "call-2=fetched call-1=partial"
		}
		if got := callIDs(its); got != want {
			t.Errorf("%s: %s, want %s", ref, got, want)
			continue
		}
		parts := its[len(its)-1]["parts"].([]any)
		if len(parts) != 3 {
			t.Fatalf("%s: %d parts", ref, len(parts))
		}
		p1, p3 := parts[0].(map[string]any), parts[2].(map[string]any)
		f1 := p1["fetch"].(map[string]any)
		if p1["ordinal"] != float64(1) || p1["transcribe_only"] != true || p1["state"] != "ok" || p1["reason"] != nil || p1["fetchable"] != true ||
			f1["state"] != "ok" || f1["fetched_at"] != "2026-11-09T08:00:00Z" || f1["entries"] != float64(3) || f1["browser"] != "edge" || f1["http_status"] != float64(200) {
			t.Fatalf("%s: part 1 %v", ref, p1)
		}
		if p3["state"] != "not_fetched" || p3["fetch"] != nil || p3["reason"] != "not fetched yet; run m365crawl transcripts fetch call-1" || p3["part_key"] != "d:b!d1/P3" {
			t.Fatalf("%s: part 3 %v", ref, p3)
		}
	}
	m := trJSON(t, e, "transcripts", "call-3")
	p := items(t, m)[0]["parts"].([]any)[0].(map[string]any)
	if p["state"] != "unfetchable" || p["fetchable"] != false || p["ref_quality"] != "share_only" || p["reason"] != "only a sharing link is cached for this part; m365crawl cannot fetch it from ids" {
		t.Fatalf("share-only part %v", p)
	}
	if m["notices"] != nil {
		t.Fatalf("an unfetchable meeting has nothing to fetch: %v", m["notices"])
	}
	if m := trJSON(t, e, "transcripts", "call-4"); m["notices"].([]any)[0] != "this meeting has transcript parts that are not fetched yet; run m365crawl transcripts fetch <call-id>" {
		t.Fatalf("notice %v", m["notices"])
	}
	if got := ofListed(1, 3); got != "1 of the 3 meetings listed has" {
		t.Fatalf("ofListed %q", got)
	}
	// Filters apply to the meeting's calls too.
	if m := trJSON(t, e, "transcripts", trWeekly, "--state", "fetched"); callIDs(items(t, m)) != "call-2=fetched" {
		t.Fatalf("filtered meeting %v", m)
	}
}

func TestShow(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "transcripts", "show", "call-1")
	if m["call_id"] != "call-1" || m["source"] != "archive" || m["complete"] != false || m["title"] != "Weekly sync" || m["event_key"] != calendar.Key(trEvent()) || m["notices"] != nil {
		t.Fatalf("show %v", m)
	}
	segs := m["segments"].([]any)
	if len(segs) != 3 {
		t.Fatalf("%d segments", len(segs))
	}
	s1, s2, s3 := segs[0].(map[string]any), segs[1].(map[string]any), segs[2].(map[string]any)
	if s1["transcribe_only"] != true || s1["state"] != "ok" || s1["fetched_at"] != "2026-11-09T08:00:00Z" || len(s1["entries"].([]any)) != 3 {
		t.Fatalf("segment 1 %v", s1)
	}
	e2 := s2["entries"].([]any)[0].(map[string]any)
	if e2["start"] != "2026-11-03T11:04:05.5Z" || e2["end"] != "2026-11-03T11:04:07Z" || e2["offset"] != "1:02:05" || e2["speaker"] != "Bo Example" {
		t.Fatalf("absolute times from the part's start %v", e2)
	}
	bare := s2["entries"].([]any)[1].(map[string]any)
	if bare["speaker"] != nil || bare["start"] != nil || bare["offset"] != nil || bare["text"] != "(inaudible)" {
		t.Fatalf("an entry with no speaker or offset %v", bare)
	}
	if s3["state"] != "not_fetched" || s3["fetched_at"] != nil || len(s3["entries"].([]any)) != 0 || s3["reason"] != "not fetched yet; run m365crawl transcripts fetch call-1" {
		t.Fatalf("segment 3 %v", s3)
	}
	// Every part fetched: complete.
	if m := trJSON(t, e, "transcripts", "show", "call-2"); m["complete"] != true {
		t.Fatalf("call-2 %v", m)
	}
	// The event names call-1 alone.
	if m := trJSON(t, e, "transcripts", "show", trEventID()); m["call_id"] != "call-1" || m["notices"] != nil {
		t.Fatalf("by event %v", m)
	}
	if m := trJSON(t, e, "--fields", "call_id,complete", "transcripts", "show", "call-1"); len(m) != 3 || m["complete"] != false || m["archive_age_seconds"] == nil {
		t.Fatalf("--fields %v", m)
	}
}

func TestShowMarksMissingPartAtSeam(t *testing.T) {
	e := trEnv(t)
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	// Part 2's text was never fetched, and SharePoint refused it.
	e.exec(`delete from transcript_fetches where part_key='d:b!d1/P2'`)
	if err := st.SaveTranscript(context.Background(), trAccount, "d:b!d1/P2", transcripts.FetchResult{State: transcripts.StateNoAccess, HTTPStatus: 403, Browser: "edge", At: trFetchedAt}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	m := trJSON(t, e, "transcripts", "show", "call-1")
	s2 := m["segments"].([]any)[1].(map[string]any)
	if m["complete"] != false || s2["state"] != "no_access" || s2["reason"] != "SharePoint refused access (HTTP 403); you may have lost access to this file" || len(s2["entries"].([]any)) != 0 {
		t.Fatalf("segment 2 %v", s2)
	}
	_, out, _ := e.tr("--format", "text", "transcripts", "show", "call-1")
	if !strings.Contains(out, "── part 2 of 3 · 10:02 · not fetched: SharePoint refused access (HTTP 403); you may have lost access to this file ──\n── part 3 of 3 · 10:40 · not fetched yet; run m365crawl transcripts fetch call-1 ──") {
		t.Fatalf("the gap is not marked at its seam:\n%s", out)
	}
	if !strings.Contains(out, "source: archive · 1 of 3 parts fetched") {
		t.Fatalf("footer:\n%s", out)
	}
}

func TestShowMaxText(t *testing.T) {
	e := trEnv(t)
	m := trJSON(t, e, "--max-text", "5", "transcripts", "show", "call-1")
	es := m["segments"].([]any)[0].(map[string]any)["entries"].([]any)
	first, third := es[0].(map[string]any), es[2].(map[string]any)
	if first["text"] != "Good …" || first["text_truncated"] != true || third["text"] != "Yes, …" || m["text_truncated"] != true {
		t.Fatalf("entries %v", es)
	}
	if m := trJSON(t, e, "transcripts", "show", "call-1"); m["text_truncated"] != nil {
		t.Fatal("nothing cut, no text_truncated")
	}
	_, out, _ := e.tr("--format", "text", "--max-text", "5", "transcripts", "show", "call-1")
	if !strings.Contains(out, "entries cut by --max-text") || !strings.Contains(out, "Ada Example: Good … Shall…") {
		t.Fatalf("text:\n%s", out)
	}
	// --fields with text keeps text_truncated beside it.
	if m := trJSON(t, e, "--max-text", "5", "--fields", "segments", "transcripts", "show", "call-1"); m["text_truncated"] != true || m["call_id"] != nil {
		t.Fatalf("--fields %v", m)
	}
}

func TestShowThreadManyCallsNotice(t *testing.T) {
	e := trEnv(t)
	for _, ref := range []string{trWeekly, "https://teams.microsoft.com/l/message/19%3Ameeting_weekly%40thread.v2/1700000046000"} {
		m := trJSON(t, e, "transcripts", "show", ref)
		notices, _ := m["notices"].([]any)
		if m["call_id"] != "call-2" || len(notices) != 1 || notices[0] != "this chat has 2 recorded calls; pass a call id from: m365crawl transcripts '"+trWeekly+"'" {
			t.Fatalf("%s: %v", ref, m)
		}
	}
	_, out, _ := e.tr("--format", "text", "transcripts", "show", trWeekly)
	if !strings.Contains(out, "this chat has 2 recorded calls; pass a call id from: m365crawl transcripts '"+trWeekly+"'") {
		t.Fatalf("text:\n%s", out)
	}
	// An event with two calls says so too, as a meeting.
	e.exec(`update transcript_parts set message_id='1002' where call_id='call-2'`)
	e.exec(`update messages set sent_at='2026-11-03T11:30:00.000Z' where id='2001'`)
	m := trJSON(t, e, "transcripts", "show", trEventID())
	if n := m["notices"].([]any); len(n) != 1 || !strings.HasPrefix(n[0].(string), "this meeting has 2 recorded calls; pass a call id from: m365crawl transcripts ev_") {
		t.Fatalf("event notice %v", m["notices"])
	}
}

func TestTranscriptsEmptyArchiveNote(t *testing.T) {
	e := textEnv(t)
	// No archive at all.
	m := trJSON(t, e, "transcripts")
	if m["note"] != "no archive yet: run m365crawl sync" || m["count"] != float64(0) || m["needs_sync"] != true || m["source"] != "archive" {
		t.Fatalf("no archive %v", m)
	}
	if m := trJSON(t, e, "transcripts", "show", "call-1"); m["needs_sync"] != true || m["call_id"] != nil {
		t.Fatalf("show with no archive %v", m)
	}
	// An archive with no recorded meeting, with and without filters.
	e.emptyArchive()
	for _, args := range [][]string{{"transcripts"}, {"transcripts", "--state", "fetched"}} {
		if m := trJSON(t, e, args...); m["note"] != "no recorded meetings in the archive yet; run m365crawl sync" {
			t.Fatalf("%v: %v", args, m)
		}
	}
	_, out, _ := e.tr("--format", "text", "transcripts")
	if !strings.Contains(out, "note: no recorded meetings in the archive yet; run m365crawl sync") || !strings.HasSuffix(out, "source: archive\n") {
		t.Fatalf("text:\n%s", out)
	}
	trFails(t, e, "unknown_meeting", 2, "transcripts", "show", "call-1")
	// An account the archive does not hold says so.
	if m := trJSON(t, e, "--account", "t/u", "transcripts"); !strings.HasPrefix(m["note"].(string), "the archive holds no data for account t/u") {
		t.Fatalf("unknown account %v", m)
	}
}

func TestTranscriptsFilterEmptyNote(t *testing.T) {
	e := trEnv(t)
	for _, args := range [][]string{
		{"transcripts", "--since", "2027-01-01T00:00:00Z"},
		{"transcripts", "--state", "fetched", "--until", "2026-11-01T00:00:00Z"},
		{"transcripts", "call-3", "--state", "fetched"},
	} {
		m := trJSON(t, e, args...)
		if m["note"] != "no recorded meeting matched the filters" || m["count"] != float64(0) || m["notices"] != nil {
			t.Fatalf("%v: %v", args, m)
		}
	}
}

// An archive from before the transcript tables reads as empty and asks for a sync.
func TestTranscriptsArchiveFromBeforeTheTables(t *testing.T) {
	e := trEnv(t)
	e.exec(`drop table transcript_parts`)
	m := trJSON(t, e, "transcripts")
	if m["note"] != noTranscriptTables || m["needs_sync"] != true || m["hint"] != noTranscriptTables {
		t.Fatalf("list %v", m)
	}
	if m := trJSON(t, e, "transcripts", "show", "call-1"); m["needs_sync"] != true || m["hint"] != noTranscriptTables {
		t.Fatalf("show %v", m)
	}
}

func TestTranscriptsUsageErrors(t *testing.T) {
	e := trEnv(t)
	for _, c := range []struct {
		args []string
		code string
		exit int
		fix  string
	}{
		{[]string{"transcripts", "nothing"}, "unknown_meeting", 2, "List meetings with recordings: m365crawl transcripts"},
		{[]string{"transcripts", "show", "nothing"}, "unknown_meeting", 2, "List meetings with recordings: m365crawl transcripts"},
		{[]string{"transcripts", "--state", "done"}, "usage", 2, "Pick one of the states in the message."},
		{[]string{"transcripts", "--limit", "0"}, "usage", 2, ""},
		{[]string{"transcripts", "--limit", "1001"}, "usage", 2, "Use --limit 1000 or less, and narrow the list with --since, --until or --state."},
		{[]string{"transcripts", "--since", "soon"}, "usage", 2, ""},
		{[]string{"transcripts", "--until", "soon"}, "usage", 2, ""},
		{[]string{"--fields", "nope", "transcripts"}, "usage", 2, "Pick keys from the list in the message."},
		{[]string{"--fields", "call_id,parts", "transcripts"}, "usage", 2, "Run `m365crawl transcripts <meeting> --fields call_id,parts`, taking the call_id from the list."},
		{[]string{"--fields", "nope", "transcripts", "show", "call-1"}, "usage", 2, "Pick keys from the list in the message."},
		{[]string{"transcripts", "https://example.com/x"}, "usage", 2, "Pass a link like https://teams.microsoft.com/l/message/<threadId>/<messageId>, or a thread id, a call id or an event id."},
		{[]string{"transcripts", "show", "https://teams.microsoft.com/l/message/"}, "usage", 2, "Pass a link like https://teams.microsoft.com/l/message/<threadId>/<messageId>, or a thread id, a call id or an event id."},
		{[]string{"transcripts", "show"}, "usage", 2, "Run `m365crawl transcripts show --help` to see the accepted arguments and flags."},
	} {
		er := trFails(t, e, c.code, c.exit, c.args...)
		if c.fix != "" && er["fix"] != c.fix {
			t.Errorf("%v: fix %q", c.args, er["fix"])
		}
	}
	// --fields and --max-text are accepted: both commands are list commands.
	if m := trJSON(t, e, "--fields", "call_id,parts", "transcripts", "call-2"); len(items(t, m)[0]) != 2 {
		t.Fatalf("--fields %v", m)
	}
}

// --fields in text mode prints the kept keys.
func TestTranscriptsTextWithFields(t *testing.T) {
	e := trEnv(t)
	_, out, _ := e.tr("--format", "text", "--fields", "call_id,state", "transcripts")
	if !strings.Contains(out, "call_id") || !strings.Contains(out, "call-4") || strings.Contains(out, "Weekly sync") {
		t.Fatalf("list:\n%s", out)
	}
	_, out, _ = e.tr("--format", "text", "--fields", "call_id,complete", "transcripts", "show", "call-1")
	if !strings.Contains(out, "call-1") || !strings.Contains(out, "complete") || strings.Contains(out, "Good morning") {
		t.Fatalf("show:\n%s", out)
	}
	// No archive: show prints only the archive state.
	empty := textEnv(t)
	_, out, _ = empty.tr("--format", "text", "transcripts", "show", "call-1")
	if !strings.Contains(out, "archive age: never synced") {
		t.Fatalf("empty show:\n%s", out)
	}
	_, out, _ = e.tr("--format", "text", "transcripts", "--limit", "1")
	if !strings.Contains(out, "1 items (more exist; raise --limit)") {
		t.Fatalf("truncated:\n%s", out)
	}
}

type noNetwork struct{ t *testing.T }

func (n noNetwork) RoundTrip(r *http.Request) (*http.Response, error) {
	n.t.Errorf("network request to %s", r.URL.Host)
	return nil, errors.New("no network in this test")
}

func TestTranscriptsNoNetwork(t *testing.T) {
	old := http.DefaultTransport
	http.DefaultTransport = noNetwork{t}
	t.Cleanup(func() { http.DefaultTransport = old })
	e := trEnv(t)
	for _, args := range [][]string{{"transcripts"}, {"transcripts", trWeekly}, {"transcripts", "show", "call-1"}, {"--format", "text", "transcripts", "show", trEventID()}} {
		if code, _, errOut := e.tr(args...); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut)
		}
	}
}

func TestTranscriptsHelpIsTheContract(t *testing.T) {
	texts := helpTexts(t)
	for where, want := range map[string]string{
		"transcripts":      "List meetings with recordings and their transcript parts, in order, with what is fetched. Offline: reads only the archive.",
		"transcripts list": "List meetings with recordings and their transcript parts, in order, with what is fetched. Offline: reads only the archive.",
		"transcripts show": "Print a meeting's transcript from the archive: every part in time order, with each seam marked and where each part came from. Offline.",
	} {
		if !strings.HasPrefix(texts[where], want) {
			t.Errorf("%s: %q", where, texts[where])
		}
	}
	for _, flag := range []string{"--since", "--until", "--state", "--limit"} {
		if texts["transcripts list "+flag] == "" {
			t.Errorf("transcripts list has no %s", flag)
		}
	}
	e := textEnv(t)
	for _, args := range [][]string{{"transcripts", "--help"}, {"transcripts", "show", "--help"}, {"transcripts", "list", "--help"}} {
		code, out, _ := e.run(append(args, "--format", "text")...)
		// Help wraps to the terminal's width, which differs by host: compare with whitespace collapsed.
		flat := strings.Join(strings.Fields(out), " ")
		if code != 0 || !strings.Contains(flat, "transcripts --help") && !strings.Contains(flat, "A recorded meeting is one call.") {
			t.Fatalf("%v: exit %d\n%s", args, code, out)
		}
	}
	if !contains(listCommands, "transcripts") || !contains(listCommands, "transcripts show") {
		t.Fatal("both commands take --fields and --max-text")
	}
}

func TestOffsetText(t *testing.T) {
	for ms, want := range map[int64]string{0: "0:00:00", 999: "0:00:00", 61_000: "0:01:01", 3_725_500: "1:02:05", 36_000_000: "10:00:00"} {
		if got := offsetText(ms); got != want {
			t.Errorf("offsetText(%d) = %q, want %q", ms, got, want)
		}
	}
	if clock(time.Time{}) != "-" {
		t.Fatal("no time is -")
	}
}

// failTranscriptCalls makes the nth read of recorded calls from now on fail.
func failTranscriptCalls(t *testing.T, nth int) {
	t.Helper()
	old, calls := transcriptCallsOf, 0
	transcriptCallsOf = func(st *store.Store, ctx context.Context, f store.TranscriptFilter) ([]store.TranscriptCall, bool, error) {
		if calls++; calls == nth {
			return nil, false, errors.New("disk I/O error")
		}
		return old(st, ctx, f)
	}
	t.Cleanup(func() { transcriptCallsOf = old })
}

func TestTranscriptsReadFailuresSurface(t *testing.T) {
	e := trEnv(t)
	for _, c := range []struct {
		nth  int
		args []string
	}{
		{1, []string{"transcripts"}},
		{2, []string{"transcripts", "--state", "fetched", "--since", "2027-01-01T00:00:00Z"}},
		{1, []string{"transcripts", "show", "call-1"}},
	} {
		failTranscriptCalls(t, c.nth)
		trFails(t, e, "db_error", 1, c.args...)
	}
	// The entries of a fetched part cannot be read.
	e.exec(`alter table transcript_entries rename column text to body`)
	trFails(t, e, "db_error", 1, "transcripts", "show", "call-1")
}

// A call with only a transcript notice has a placeholder part whose kind is unknown: nothing says
// whether it was recorded or only transcribed.
func TestTranscriptsUnresolvedKindIsUnknown(t *testing.T) {
	e := trEnv(t)
	e.exec(`insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at) values('` + tenantA + `','` + userA + `','` + trAdhoc + `','5001','2026-11-13T15:05:00.000Z','` + transcripts.TypeTranscript + `','{\"callId\":\"call-5\"}','2026-11-13T15:05:00.000Z')`)
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
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
	_ = st.Close()
	p := items(t, trJSON(t, e, "transcripts", "call-5"))[0]["parts"].([]any)[0].(map[string]any)
	if p["transcribe_only"] != false || p["ref_quality"] != "unresolved" || p["state"] != "unfetchable" {
		t.Fatalf("placeholder %v", p)
	}
	_, out, _ := e.tr("--format", "text", "transcripts", "call-5")
	if !regexp.MustCompile(`(?m)^1\s+-\s+unknown\s+unfetchable`).MatchString(out) {
		t.Fatalf("kind:\n%s", out)
	}
	_, out, _ = e.tr("--format", "text", "transcripts", "show", "call-5")
	if !strings.Contains(out, "── part 1 of 1 · - · not fetched: Teams posted a transcript notice but no file reference ──") {
		t.Fatalf("seam:\n%s", out)
	}
}
