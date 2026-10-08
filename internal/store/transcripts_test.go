package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

const tHost = "https://tenant.sharepoint.example.invalid"

// tNotice is a synthetic recording notice. ref is "drive:<item>" for a part with its own drive
// item, "share:<token>" for one with only a sharing link, and anything else for one with neither.
func tNotice(call, chunk, status, types, stamp, ref string) string {
	var link, item string
	switch kind, id, _ := strings.Cut(ref, ":"); kind {
	case "drive":
		item = `<item type="onedriveForBusinessTranscript" uri="` + tHost + `/teams/site-a/_api/v2.1/drives/b!d1/items/` + id + `/versions/current/media/transcripts/tr-` + id + `/content"/>`
	case "share":
		link = `<a href="` + tHost + `/:v:/t/site-a/` + id + `">Play</a>`
	}
	return `<URIObject type="Video.2/CallRecording.1">` + link + `<Identifiers><Id type="callId" value="` + call + `"/><Id type="chunkIndex" value="` + chunk + `"/></Identifiers>` +
		`<RecordingStatus status="` + status + `"/><RecordingContent contentTypes="` + types + `" timestamp="` + stamp + `" duration="0:10:00.000">` + item + `</RecordingContent></URIObject>`
}

func tTranscriptNotice(call string) string {
	return `{\"callId\":\"` + call + `\",\"isDeleted\":false}`
}

func (s *Store) tMessage(t *testing.T, a teamsdesktop.Account, thread, id, typ, sent, html string) {
	t.Helper()
	s.qExec(t, `insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,updated_at) values(?,?,?,?,?,?,?,?)`,
		a.TenantID, a.UserID, thread, id, sent, typ, html, sent)
}

const (
	tThread = "19:meeting_T@thread.v2"
	tAcctA  = "tenant-1/aaaaaaaa-0000-0000-0000-000000000001"
	tAcctB  = "tenant-2/bbbbbbbb-0000-0000-0000-000000000002"
)

// tSeed is one account's meeting chat with notices of every kind:
// call-1 has a transcribe-only part and a recorded part, each with a drive item (and the notices
// that came before each Success); call-2 has only a sharing link; call-3 only a media-service
// link; call-4 has a transcript notice and no part; call-5 has one part posted twice.
func tSeed(t *testing.T, s *Store) {
	t.Helper()
	rec := transcripts.TypeRecording
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values(?,?,?,'Meeting','Weekly title','Weekly sync','2026-11-01T00:00:00.000Z')`, acctA.TenantID, acctA.UserID, tThread)
	s.tMessage(t, acctA, tThread, "100", rec, "2026-11-03T10:00:05.000Z", tNotice("call-1", "", "Initial", "Recording+Transcript", "", ""))
	s.tMessage(t, acctA, tThread, "101", rec, "2026-11-03T11:01:00.000Z", tNotice("call-1", "0", "ChunkFinished", "Recording+Transcript", "2026-11-03T10:02:00Z", ""))
	s.tMessage(t, acctA, tThread, "102", rec, "2026-11-03T11:05:00.000Z", tNotice("call-1", "0", "Success", "Recording+Transcript", "2026-11-03T10:02:00Z", "drive:ITEM2"))
	s.tMessage(t, acctA, tThread, "103", rec, "2026-11-03T11:06:00.000Z", tNotice("call-1", "10001", "Success", "Transcript", "2026-11-03T10:00:00Z", "drive:ITEM1"))
	s.tMessage(t, acctA, tThread, "104", transcripts.TypeTranscript, "2026-11-03T11:07:00.000Z", tTranscriptNotice("call-1"))
	s.tMessage(t, acctA, tThread, "200", rec, "2026-11-04T11:05:00.000Z", tNotice("call-2", "0", "Success", "Recording+Transcript", "2026-11-04T10:00:00Z", "share:TOKEN"))
	s.tMessage(t, acctA, tThread, "300", rec, "2026-11-05T11:05:00.000Z", tNotice("call-3", "0", "Success", "Recording+Transcript", "2026-11-05T10:00:00Z", ""))
	s.tMessage(t, acctA, tThread, "400", transcripts.TypeTranscript, "2026-11-06T11:05:00.000Z", tTranscriptNotice("call-4"))
	s.tMessage(t, acctA, tThread, "401", transcripts.TypeTranscript, "2026-11-06T11:00:00.000Z", tTranscriptNotice("call-4"))
	s.tMessage(t, acctA, tThread, "402", transcripts.TypeTranscript, "2026-11-06T11:06:00.000Z", "<p>not a notice</p>")
	s.tMessage(t, acctA, tThread, "500", rec, "2026-11-07T11:05:00.000Z", tNotice("call-5", "0", "Success", "Recording+Transcript", "2026-11-07T10:00:00Z", "drive:ITEM5"))
	s.tMessage(t, acctA, tThread, "501", rec, "2026-11-07T11:09:00.000Z", tNotice("call-5", "0", "Success", "Recording+Transcript", "2026-11-07T10:00:00Z", "drive:ITEM5"))
	// Not parts: a notice that cannot be read, a deleted one, the mirror in a system conversation,
	// and call-log metadata.
	s.tMessage(t, acctA, tThread, "600", rec, "2026-11-08T11:05:00.000Z", `<URIObject><RecordingStatus status="Success"`)
	s.qExec(t, `insert into messages(tenant_id,user_id,conversation_id,id,sent_at,message_type,content_html,deleted_at,updated_at) values(?,?,?,'601','2026-11-08T11:05:00.000Z',?,?,'2026-11-09T00:00:00.000Z','2026-11-09T00:00:00.000Z')`,
		acctA.TenantID, acctA.UserID, tThread, rec, tNotice("call-deleted", "0", "Success", "Recording+Transcript", "2026-11-08T10:00:00Z", "drive:GONE"))
	s.tMessage(t, acctA, "48:notifications", "602", rec, "2026-11-08T11:05:00.000Z", tNotice("call-mirror", "0", "Success", "Recording+Transcript", "2026-11-08T10:00:00Z", "drive:MIRROR"))
	s.tMessage(t, acctA, tThread, "603", rec, "2026-11-08T11:05:00.000Z", `{"CallId":"x"}`)
}

func tDerive(t *testing.T, s *Store, accounts ...string) map[string]TranscriptCounts {
	t.Helper()
	ctx := context.Background()
	sess, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Rollback()
	n, err := sess.DeriveTranscriptParts(ctx, accounts)
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Commit(); err != nil {
		t.Fatal(err)
	}
	return n
}

func tParts(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(`select account_id, call_id, ordinal, part_key, ref_quality, message_id, transcribe_only from transcript_parts order by account_id, call_id, ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var acct, call, key, quality, msg string
		var ord int
		var only bool
		if err := rows.Scan(&acct, &call, &ord, &key, &quality, &msg, &only); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s#%d %s %s %s %v", call, ord, key, quality, msg, only))
	}
	return strings.Join(out, "\n")
}

func TestMigrate6To7(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	tSeed(t, s)
	for _, q := range []string{`drop table transcript_parts`, `drop table transcript_fetches`, `drop table transcript_entries`, `drop table transcript_fts`, `update schema_migrations set version = 6`} {
		s.qExec(t, q)
	}
	if ok, err := s.HasTranscriptTables(ctx); ok || err != nil {
		t.Fatalf("a v6 archive has no transcript tables: %v, %v", ok, err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open v6 archive: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := rowCount(t, s, `select max(version) from schema_migrations`); v != 7 || SchemaVersion != 7 {
		t.Fatalf("version %d, SchemaVersion %d", v, SchemaVersion)
	}
	if ok, err := s.HasTranscriptTables(ctx); !ok || err != nil {
		t.Fatalf("tables: %v, %v", ok, err)
	}
	for _, tbl := range []string{"transcript_parts", "transcript_fetches", "transcript_entries", "transcript_fts"} {
		if n := rowCount(t, s, `select count(*) from `+tbl); n != 0 {
			t.Fatalf("%s holds %d rows after the upgrade", tbl, n)
		}
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where type='index' and name in ('transcript_parts_thread','transcript_parts_message','transcript_parts_starts')`); n != 3 {
		t.Fatalf("indexes: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from meta where key='transcript_parts_mapper'`); n != 0 {
		t.Fatal("the upgrade alone derives nothing: the next sync does")
	}
	if n := rowCount(t, s, `select count(*) from messages`); n != 16 {
		t.Fatalf("messages touched: %d", n)
	}
}

func TestOpenVersion8ArchiveNewer(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.qExec(t, `update schema_migrations set version = 8`)
	_ = s.Close()
	var coded *errs.Coded
	if _, err := Open(ctx, path); !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer || !strings.Contains(coded.Message, "schema version 8; this build writes version 7") {
		t.Fatalf("Open = %v, want archive_newer", err)
	}
	if _, err := OpenReadOnly(ctx, path); !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer {
		t.Fatalf("OpenReadOnly = %v, want archive_newer", err)
	}
}

const tSeedParts = `call-1#1 d:b!d1/ITEM1 drive_item 103 true
call-1#2 d:b!d1/ITEM2 drive_item 102 false
call-2#1 u:%s share_only 200 false
call-3#1 m:300 ams_only 300 false
call-4#1 call:call-4 unresolved 400 false
call-5#1 d:b!d1/ITEM5 drive_item 501 false`

func tShareKey(t *testing.T) string {
	t.Helper()
	p, ok, err := transcripts.ParseRecording("200", tThread, tNotice("call-2", "0", "Success", "Recording+Transcript", "2026-11-04T10:00:00Z", "share:TOKEN"), time.Time{})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	return strings.TrimPrefix(p.PartKey, "u:")
}

func TestDeriveTranscriptPartsFromMessages(t *testing.T) {
	s := newStore(t)
	tSeed(t, s)
	// Another account's notice stays out of this account's rows and counts.
	s.tMessage(t, acctB, "19:meeting_B@thread.v2", "900", transcripts.TypeRecording, "2026-11-03T11:05:00.000Z", tNotice("call-b", "0", "Success", "Recording+Transcript", "2026-11-03T10:00:00Z", "drive:ITEMB"))
	got := tDerive(t, s, tAcctA)
	want := TranscriptCounts{Calls: 5, Parts: 5, DriveItem: 3, ShareOnly: 1, AMSOnly: 1, Unresolved: 1, Unparsable: 1}
	if len(got) != 1 || got[tAcctA] != want {
		t.Fatalf("counts %+v, want %+v", got, want)
	}
	if parts := tParts(t, s); parts != fmt.Sprintf(tSeedParts, tShareKey(t)) {
		t.Fatalf("parts:\n%s", parts)
	}
	var thread, host, root, drive, item, tr, starts, sent string
	var dur float64
	if err := s.db.QueryRow(`select thread_id, host, site_root, drive_id, item_id, transcript_id, starts_at, sent_at, duration_seconds from transcript_parts where part_key='d:b!d1/ITEM2'`).
		Scan(&thread, &host, &root, &drive, &item, &tr, &starts, &sent, &dur); err != nil {
		t.Fatal(err)
	}
	if thread != tThread || host != "tenant.sharepoint.example.invalid" || root != "/teams/site-a" || drive != "b!d1" || item != "ITEM2" || tr != "tr-ITEM2" ||
		starts != "2026-11-03T10:02:00.000Z" || sent != "2026-11-03T11:05:00.000Z" || dur != 600 {
		t.Fatalf("row %q %q %q %q %q %q %q %q %v", thread, host, root, drive, item, tr, starts, sent, dur)
	}
	// Deriving again changes nothing, and both accounts derive together.
	both := tDerive(t, s, tAcctA, tAcctB)
	if both[tAcctA] != want || both[tAcctB] != (TranscriptCounts{Calls: 1, Parts: 1, DriveItem: 1}) {
		t.Fatalf("both %+v", both)
	}
	if n := rowCount(t, s, `select count(*) from transcript_parts`); n != 7 {
		t.Fatalf("rows %d", n)
	}
	var sum TranscriptCounts
	sum.Add(both[tAcctA])
	sum.Add(both[tAcctB])
	if sum != (TranscriptCounts{Calls: 6, Parts: 6, DriveItem: 4, ShareOnly: 1, AMSOnly: 1, Unresolved: 1, Unparsable: 1}) {
		t.Fatalf("sum %+v", sum)
	}
	// An account with no notices derives to nothing.
	if none := tDerive(t, s, "tenant-9/nobody"); none["tenant-9/nobody"] != (TranscriptCounts{}) {
		t.Fatalf("none %+v", none)
	}
}

func okResult(at time.Time, entries ...transcripts.Entry) transcripts.FetchResult {
	return transcripts.FetchResult{State: transcripts.StateOK, HTTPStatus: 200, Entries: entries, Browser: "edge", At: at}
}

func ms(n int64) *int64 { return &n }

func TestDeriveKeepsFetchState(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tSeed(t, s)
	tDerive(t, s, tAcctA)
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM2", okResult(at, transcripts.Entry{Speaker: "Ada", StartMS: ms(0), EndMS: ms(1500), Text: "hello needle"})))
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM5", okResult(at, transcripts.Entry{Speaker: "Bo", Text: "kept after delete"})))
	// The notice of call-5's part is deleted in Teams; the rebuild drops the part and keeps its text.
	s.qExec(t, `update messages set deleted_at='2026-11-10T00:00:00.000Z' where id in ('500','501')`)
	got := tDerive(t, s, tAcctA)
	if got[tAcctA].Calls != 4 || got[tAcctA].DriveItem != 2 {
		t.Fatalf("counts %+v", got)
	}
	if n := rowCount(t, s, `select count(*) from transcript_parts where call_id='call-5'`); n != 0 {
		t.Fatal("the part of a deleted notice must go")
	}
	if n := rowCount(t, s, `select count(*) from transcript_fetches`); n != 2 {
		t.Fatalf("fetch rows %d", n)
	}
	for _, key := range []string{"d:b!d1/ITEM2", "d:b!d1/ITEM5"} {
		es, err := s.TranscriptEntries(ctx, tAcctA, key)
		if err != nil || len(es) != 1 {
			t.Fatalf("%s: %v, %v", key, es, err)
		}
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts where transcript_fts match 'needle'`); n != 1 {
		t.Fatalf("index rows %d", n)
	}
}

func TestEnsureTranscriptPartsOnUpgrade(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tSeed(t, s)
	s.tMessage(t, acctB, "19:meeting_B@thread.v2", "900", transcripts.TypeRecording, "2026-11-03T11:05:00.000Z", tNotice("call-b", "0", "Success", "Recording+Transcript", "2026-11-03T10:00:00Z", "drive:ITEMB"))
	// A row of an account whose notices are all gone is cleared by the rebuild too.
	s.qExec(t, `insert into transcript_parts(account_id, call_id, part_key, ordinal, ref_quality) values('tenant-3/gone','c','d:x/y',1,'drive_item')`)
	got, err := s.EnsureTranscriptParts(ctx)
	if err != nil || len(got) != 3 || got[tAcctA].Parts != 5 || got[tAcctB].Parts != 1 || got["tenant-3/gone"] != (TranscriptCounts{}) {
		t.Fatalf("first ensure: %+v, %v", got, err)
	}
	if n := rowCount(t, s, `select count(*) from transcript_parts`); n != 7 {
		t.Fatalf("rows %d", n)
	}
	if v := rowCount(t, s, `select value from meta where key='transcript_parts_mapper'`); v != transcripts.PartsMapper {
		t.Fatalf("mapper %d", v)
	}
	// Current: nothing is due, even though a new notice arrived (the next source read derives it).
	s.tMessage(t, acctA, tThread, "700", transcripts.TypeRecording, "2026-11-11T11:05:00.000Z", tNotice("call-7", "0", "Success", "Recording+Transcript", "2026-11-11T10:00:00Z", "drive:ITEM7"))
	if got, err := s.EnsureTranscriptParts(ctx); got != nil || err != nil {
		t.Fatalf("second ensure: %+v, %v", got, err)
	}
	// An older mapper is rebuilt; a newer one is left alone.
	s.qExec(t, `update meta set value='0' where key='transcript_parts_mapper'`)
	if got, err := s.EnsureTranscriptParts(ctx); err != nil || got[tAcctA].Parts != 6 {
		t.Fatalf("older mapper: %+v, %v", got, err)
	}
	s.qExec(t, `update meta set value='99' where key='transcript_parts_mapper'`)
	if got, err := s.EnsureTranscriptParts(ctx); got != nil || err != nil {
		t.Fatalf("newer mapper: %+v, %v", got, err)
	}
	// An archive with no notices at all is stamped too, so the check is one row from then on.
	empty := newStore(t)
	if got, err := empty.EnsureTranscriptParts(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty: %+v, %v", got, err)
	}
	if got, err := empty.EnsureTranscriptParts(ctx); got != nil || err != nil {
		t.Fatalf("empty again: %+v, %v", got, err)
	}
}

func TestSaveTranscriptReplacesEntries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/1", okResult(first,
		transcripts.Entry{Speaker: "Ada", StartMS: ms(0), EndMS: ms(1500), Text: "first alpha"},
		transcripts.Entry{Speaker: "Bo", StartMS: ms(1500), Text: "second alpha"},
		transcripts.Entry{Speaker: "Ada", Text: "third alpha"})))
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/2", okResult(first, transcripts.Entry{Speaker: "Cy", Text: "other part alpha"})))
	es := must(s.TranscriptEntries(ctx, tAcctA, "d:x/1"))
	if len(es) != 3 || es[0].Speaker != "Ada" || *es[0].StartMS != 0 || *es[0].EndMS != 1500 || es[1].EndMS != nil || *es[1].StartMS != 1500 || es[2].StartMS != nil || es[2].Text != "third alpha" {
		t.Fatalf("entries %+v", es)
	}
	second := first.Add(time.Hour)
	r := okResult(second, transcripts.Entry{Speaker: "Di", Text: "replacement beta"})
	r.Browser = "chrome"
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/1", r))
	es = must(s.TranscriptEntries(ctx, tAcctA, "d:x/1"))
	if len(es) != 1 || es[0].Speaker != "Di" {
		t.Fatalf("after replace %+v", es)
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts where transcript_fts match 'alpha'`); n != 1 {
		t.Fatalf("the replaced part's index rows must go, the other part's stay: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts f join transcript_entries e on e.rowid=f.rowid where transcript_fts match 'beta' and e.part_key='d:x/1'`); n != 1 {
		t.Fatalf("index row of the new entry: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts where transcript_fts match 'speaker:Cy'`); n != 1 {
		t.Fatalf("speaker is indexed: %d", n)
	}
	var state, fetched, attempted, browser string
	var status, count int
	if err := s.db.QueryRow(`select state, fetched_at, attempted_at, http_status, entry_count, browser from transcript_fetches where part_key='d:x/1'`).Scan(&state, &fetched, &attempted, &status, &count, &browser); err != nil {
		t.Fatal(err)
	}
	if state != "ok" || fetched != "2026-11-09T09:00:00.000Z" || attempted != fetched || status != 200 || count != 1 || browser != "chrome" {
		t.Fatalf("fetch row %q %q %q %d %d %q", state, fetched, attempted, status, count, browser)
	}
	// No entries of an unknown part, and an empty transcript is still a fetched one.
	if es := must(s.TranscriptEntries(ctx, tAcctA, "d:none/0")); es != nil {
		t.Fatalf("unknown part %+v", es)
	}
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/3", okResult(first)))
	if n := rowCount(t, s, `select count(*) from transcript_fetches where part_key='d:x/3' and state='ok' and entry_count=0 and fetched_at is not null`); n != 1 {
		t.Fatal("an empty transcript must be recorded as fetched")
	}
}

func TestSaveTranscriptFailureKeepsEarlierEntries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	first := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/1", okResult(first, transcripts.Entry{Speaker: "Ada", Text: "kept gamma"})))
	later := first.Add(24 * time.Hour)
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/1", transcripts.FetchResult{State: transcripts.StateNoAccess, HTTPStatus: 403, Browser: "edge", At: later}))
	if es := must(s.TranscriptEntries(ctx, tAcctA, "d:x/1")); len(es) != 1 || es[0].Text != "kept gamma" {
		t.Fatalf("a later 403 erased the text: %+v", es)
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts where transcript_fts match 'gamma'`); n != 1 {
		t.Fatalf("index rows %d", n)
	}
	var state, fetched, attempted string
	var status, count int
	if err := s.db.QueryRow(`select state, fetched_at, attempted_at, http_status, entry_count from transcript_fetches where part_key='d:x/1'`).Scan(&state, &fetched, &attempted, &status, &count); err != nil {
		t.Fatal(err)
	}
	if state != "no_access" || fetched != "2026-11-09T08:00:00.000Z" || attempted != "2026-11-10T08:00:00.000Z" || status != 403 || count != 1 {
		t.Fatalf("fetch row %q %q %q %d %d", state, fetched, attempted, status, count)
	}
	// A part never fetched: the failure is recorded with no fetch time.
	must0(s.SaveTranscript(ctx, tAcctA, "d:x/2", transcripts.FetchResult{State: transcripts.StateNotFound, HTTPStatus: 404, At: later}))
	if n := rowCount(t, s, `select count(*) from transcript_fetches where part_key='d:x/2' and state='not_found' and fetched_at is null and entry_count=0 and attempted_at is not null`); n != 1 {
		t.Fatal("the failed first attempt must be recorded without a fetch time")
	}
}

func TestSaveTranscript20kEntries(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	entries := make([]transcripts.Entry, 20000)
	for i := range entries {
		entries[i] = transcripts.Entry{Speaker: fmt.Sprintf("Speaker %d", i%7), StartMS: ms(int64(i) * 1000), EndMS: ms(int64(i)*1000 + 900), Text: fmt.Sprintf("synthetic utterance number %d of a long meeting", i)}
	}
	began := time.Now()
	if err := s.SaveTranscript(ctx, tAcctA, "d:x/big", okResult(began, entries...)); err != nil {
		t.Fatal(err)
	}
	took := time.Since(began)
	if n := rowCount(t, s, `select count(*) from transcript_entries where part_key='d:x/big'`); n != 20000 {
		t.Fatalf("entries %d", n)
	}
	if n := rowCount(t, s, `select count(*) from transcript_fts where transcript_fts match '"number 19999"'`); n != 1 {
		t.Fatalf("index rows %d", n)
	}
	if n := rowCount(t, s, `select entry_count from transcript_fetches where part_key='d:x/big'`); n != 20000 {
		t.Fatalf("entry_count %d", n)
	}
	// The race detector slows every statement several times over; the bound is the plain build's.
	if !raceEnabled && took > 2*time.Second {
		t.Fatalf("saving 20,000 entries took %v, more than 2s", took)
	}
	es := must(s.TranscriptEntries(ctx, tAcctA, "d:x/big"))
	if len(es) != 20000 || es[19999].Text != entries[19999].Text || *es[19999].StartMS != 19999000 {
		t.Fatalf("read back %d", len(es))
	}
}

// tEventSeed adds, to the calendar archive of qSeed, recording notices in the series' chat: two
// calls in the Nov 3 occurrence (one of them the recap's call) and one in the Nov 10 occurrence.
func tEventSeed(t *testing.T) *Store {
	t.Helper()
	s := qArchive(t)
	rec := transcripts.TypeRecording
	s.tMessage(t, acctA, qChat, "t-1", rec, "2026-11-03T11:10:00.000Z", tNotice("call-1", "0", "Success", "Recording+Transcript", "2026-11-03T10:02:00Z", "drive:E1"))
	s.tMessage(t, acctA, qChat, "t-1b", rec, "2026-11-03T11:40:00.000Z", tNotice("call-1b", "0", "Success", "Recording+Transcript", "2026-11-03T10:30:00Z", "drive:E1B"))
	s.tMessage(t, acctA, qChat, "t-2", rec, "2026-11-10T11:05:00.000Z", tNotice("call-2", "0", "Success", "Recording+Transcript", "2026-11-10T10:01:00Z", "drive:E2"))
	tDerive(t, s, qTeams)
	return s
}

func resolve(t *testing.T, s *Store, acct *teamsdesktop.Account, ref string) (string, string) {
	t.Helper()
	calls, kind, err := s.ResolveMeeting(context.Background(), acct, ref)
	if err != nil {
		t.Fatalf("ResolveMeeting(%q): %v", ref, err)
	}
	return strings.Join(calls, ","), kind
}

func unknownMeeting(t *testing.T, s *Store, acct *teamsdesktop.Account, ref string) {
	t.Helper()
	calls, kind, err := s.ResolveMeeting(context.Background(), acct, ref)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeUnknownMeeting || coded.Exit != errs.ExitUsage || calls != nil || kind != "" ||
		coded.Fix != "List meetings with recordings: m365crawl transcripts" || !strings.Contains(coded.Message, fmt.Sprintf("%q", ref)) {
		t.Fatalf("ResolveMeeting(%q) = %v, %q, %v; want unknown_meeting", ref, calls, kind, err)
	}
}

func TestResolveMeetingByCall(t *testing.T) {
	s := tEventSeed(t)
	if calls, kind := resolve(t, s, nil, "call-1b"); calls != "call-1b" || kind != MeetingByCall {
		t.Fatalf("%q %q", calls, kind)
	}
	if calls, kind := resolve(t, s, &acctA, "call-2"); calls != "call-2" || kind != MeetingByCall {
		t.Fatalf("own account: %q %q", calls, kind)
	}
	unknownMeeting(t, s, &acctB, "call-2") // another account's call is not this account's
}

func TestResolveMeetingByThreadID(t *testing.T) {
	s := tEventSeed(t)
	if calls, kind := resolve(t, s, nil, qChat); calls != "call-2,call-1b,call-1" || kind != MeetingByThread {
		t.Fatalf("%q %q", calls, kind)
	}
	if calls, kind := resolve(t, s, &acctA, qChat); calls != "call-2,call-1b,call-1" || kind != MeetingByThread {
		t.Fatalf("own account: %q %q", calls, kind)
	}
	unknownMeeting(t, s, &acctB, qChat)
}

func TestResolveMeetingByEvent(t *testing.T) {
	s := tEventSeed(t)
	o1 := calendar.Key(qEvent("o1", 3))
	for _, ref := range []string{o1, EventID(qTeams, o1)} {
		if calls, kind := resolve(t, s, nil, ref); calls != "call-1b,call-1" || kind != MeetingByEvent {
			t.Fatalf("%s: %q %q", ref, calls, kind)
		}
	}
	if calls, kind := resolve(t, s, &acctA, EventID(qTeams, calendar.Key(qEvent("o2", 10)))); calls != "call-2" || kind != MeetingByEvent {
		t.Fatalf("o2: %q %q", calls, kind)
	}
	// An occurrence with no recording is no recorded meeting, and neither is the master.
	unknownMeeting(t, s, nil, EventID(qTeams, calendar.Key(qEvent("o3", 17))))
	// A call the recap names counts even when its notice was posted too late to match the occurrence.
	s.qExec(t, `update messages set sent_at='2026-11-08T09:00:00.000Z' where id='t-1'`)
	tDerive(t, s, qTeams)
	if calls, _ := resolve(t, s, nil, o1); calls != "call-1b,call-1" {
		t.Fatalf("recap call: %q", calls)
	}
	// A recording message that is no part (the plain rows of qSeed) names no call on its own.
	s.qExec(t, `delete from transcript_parts where call_id in ('call-1','call-1b')`)
	unknownMeeting(t, s, nil, o1)
}

// Teams re-posts a recording notice, sometimes days later. The part keeps the newest notice of its
// file, which falls outside the occurrence's window; the call still belongs to the occurrence its
// first notice fell in, for ResolveMeeting and for the event key the list prints.
func TestResolveMeetingSurvivesARepostedNotice(t *testing.T) {
	ctx := context.Background()
	s := tEventSeed(t)
	o1 := calendar.Key(qEvent("o1", 3))
	s.tMessage(t, acctA, qChat, "t-1b-again", transcripts.TypeRecording, "2026-11-05T11:40:00.000Z", tNotice("call-1b", "0", "Success", "Recording+Transcript", "2026-11-03T10:30:00Z", "drive:E1B"))
	tDerive(t, s, qTeams)
	if n := rowCount(t, s, `select count(*) from transcript_parts where call_id='call-1b' and message_id='t-1b-again'`); n != 1 {
		t.Fatalf("the part keeps the newest notice: %d", n)
	}
	for _, acct := range []*teamsdesktop.Account{nil, &acctA} {
		if calls, kind := resolve(t, s, acct, o1); calls != "call-1b,call-1" || kind != MeetingByEvent {
			t.Fatalf("o1: %q %q", calls, kind)
		}
	}
	calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{Calls: []string{"call-1b"}})
	if err != nil || len(calls) != 1 || calls[0].EventKey != o1 {
		t.Fatalf("call-1b's event: %+v, %v", calls, err)
	}
	// A transcript notice of the call counts as one of its notices too.
	s.qExec(t, `update messages set sent_at='2026-11-05T11:41:00.000Z' where id='t-1b'`)
	s.tMessage(t, acctA, qChat, "t-1b-tr", transcripts.TypeTranscript, "2026-11-03T11:41:00.000Z", tTranscriptNotice("call-1b"))
	tDerive(t, s, qTeams)
	if calls, _ := resolve(t, s, nil, o1); calls != "call-1b,call-1" {
		t.Fatalf("by the transcript notice: %q", calls)
	}
}

func TestResolveMeetingByEventPrefix(t *testing.T) {
	s := tEventSeed(t)
	id := EventID(qTeams, calendar.Key(qEvent("o2", 10)))
	if calls, kind := resolve(t, s, nil, id[:9]); calls != "call-2" || kind != MeetingByEvent {
		t.Fatalf("%q %q", calls, kind)
	}
	// A prefix several events share is the calendar's own usage error, not an unknown meeting.
	_, _, err := s.ResolveMeeting(context.Background(), nil, "uid-")
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeUsage || !strings.Contains(coded.Message, "matches") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
}

func TestResolveMeetingUnknown(t *testing.T) {
	s := tEventSeed(t)
	for _, ref := range []string{"nothing", "ev_0000000000", "19:meeting_none@thread.v2", ""} {
		unknownMeeting(t, s, nil, ref)
	}
	// An event with no meeting chat cannot have recordings.
	s.qApply(t, calendar.SourceTeams, qTeams, qEvent("solo", 20, func(e *calendar.Event) { e.TeamsThreadID = ""; e.SeriesKey = "" }))
	unknownMeeting(t, s, nil, EventID(qTeams, calendar.Key(qEvent("solo", 20, func(e *calendar.Event) { e.TeamsThreadID = ""; e.SeriesKey = "" }))))
	// An archive from before the calendar tables resolves calls and chats, and nothing else.
	old := newStore(t)
	tSeed(t, old)
	tDerive(t, old, tAcctA)
	old.qExec(t, `drop table calendar_recaps`)
	if calls, kind := resolve(t, old, nil, "call-1"); calls != "call-1" || kind != MeetingByCall {
		t.Fatalf("%q %q", calls, kind)
	}
	unknownMeeting(t, old, nil, "ev_0000000000")
}

func callStates(calls []TranscriptCall) string {
	var out []string
	for _, c := range calls {
		fetchable, fetched := c.Fetchable()
		out = append(out, fmt.Sprintf("%s=%s(%d/%d/%d)", c.CallID, c.State, fetched, fetchable, len(c.Parts)))
	}
	return strings.Join(out, " ")
}

func TestTranscriptCallsStates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	tSeed(t, s)
	tDerive(t, s, tAcctA)
	at := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	calls, truncated, err := s.TranscriptCalls(ctx, TranscriptFilter{})
	if err != nil || truncated {
		t.Fatal(truncated, err)
	}
	// Newest first; nothing fetched yet.
	if got := callStates(calls); got != "call-5=not_fetched(0/1/1) call-4=unfetchable(0/0/1) call-3=unfetchable(0/0/1) call-2=unfetchable(0/0/1) call-1=not_fetched(0/2/2)" {
		t.Fatalf("states: %s", got)
	}
	c1 := calls[4]
	if c1.AccountID != tAcctA || c1.ThreadID != tThread || c1.Title != "Weekly sync" || c1.EventKey != "" || !c1.StartedAt.Equal(time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("call-1 %+v", c1)
	}
	p := c1.Parts[0]
	if p.Ordinal != 1 || !p.TranscribeOnly || !p.Fetchable || p.Fetch != nil || p.HasText() || p.State() != transcripts.StateNotFetched ||
		p.Reason != "not fetched yet; run m365crawl transcripts fetch call-1" || p.DurationSeconds != 600 || p.Host != "tenant.sharepoint.example.invalid" {
		t.Fatalf("part %+v", p)
	}
	if u := calls[1]; !u.StartedAt.Equal(time.Date(2026, 11, 6, 11, 5, 0, 0, time.UTC)) || u.Parts[0].Reason != "Teams posted a transcript notice but no file reference" || u.Parts[0].Fetchable {
		t.Fatalf("unresolved call %+v", u)
	}
	if calls[2].Parts[0].Reason != "only a Teams media-service link is cached; it is not a SharePoint file" || calls[3].Parts[0].Reason != "only a sharing link is cached for this part; m365crawl cannot fetch it from ids" {
		t.Fatalf("reasons %q %q", calls[2].Parts[0].Reason, calls[3].Parts[0].Reason)
	}
	// One of call-1's two parts fetched: partial. Both: fetched. A failed attempt keeps not_fetched.
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM1", okResult(at, transcripts.Entry{Speaker: "Ada", Text: "one"}, transcripts.Entry{Speaker: "Bo", Text: "two"})))
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM5", transcripts.FetchResult{State: transcripts.StateNoAccess, HTTPStatus: 403, Browser: "edge", At: at}))
	calls, _, err = s.TranscriptCalls(ctx, TranscriptFilter{Account: &acctA})
	if err != nil {
		t.Fatal(err)
	}
	if got := callStates(calls); got != "call-5=not_fetched(0/1/1) call-4=unfetchable(0/0/1) call-3=unfetchable(0/0/1) call-2=unfetchable(0/0/1) call-1=partial(1/2/2)" {
		t.Fatalf("states: %s", got)
	}
	f := calls[4].Parts[0].Fetch
	if f == nil || f.State != "ok" || !f.FetchedAt.Equal(at) || !f.AttemptedAt.Equal(at) || f.HTTPStatus != 200 || f.EntryCount != 2 || f.Browser != "edge" ||
		calls[4].Parts[0].Reason != "" || calls[4].Parts[0].State() != transcripts.StateOK {
		t.Fatalf("fetch %+v", f)
	}
	denied := calls[0].Parts[0]
	if denied.Fetch.FetchedAt != nil || denied.HasText() || denied.State() != transcripts.StateNoAccess || denied.Fetch.HTTPStatus != 403 ||
		denied.Reason != "SharePoint refused access (HTTP 403); you may have lost access to this file" {
		t.Fatalf("denied part %+v %+v", denied, denied.Fetch)
	}
	must0(s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM2", okResult(at)))
	for state, want := range map[string]string{
		CallFetched:     "call-1=fetched(2/2/2)",
		CallNotFetched:  "call-5=not_fetched(0/1/1)",
		CallUnfetchable: "call-4=unfetchable(0/0/1) call-3=unfetchable(0/0/1) call-2=unfetchable(0/0/1)",
		CallPartial:     "",
	} {
		calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{State: state})
		if err != nil || callStates(calls) != want {
			t.Fatalf("--state %s: %s, %v", state, callStates(calls), err)
		}
	}
	// Bounds are by the call's start: Since is inclusive, Until exclusive.
	since, until := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 6, 11, 5, 0, 0, time.UTC)
	if calls, _, _ := s.TranscriptCalls(ctx, TranscriptFilter{Since: since, Until: until}); callStates(calls) != "call-3=unfetchable(0/0/1) call-2=unfetchable(0/0/1)" {
		t.Fatalf("bounds: %s", callStates(calls))
	}
	if calls, truncated, _ := s.TranscriptCalls(ctx, TranscriptFilter{Limit: 2}); !truncated || callStates(calls) != "call-5=not_fetched(0/1/1) call-4=unfetchable(0/0/1)" {
		t.Fatalf("limit: %v %s", truncated, callStates(calls))
	}
	if calls, truncated, _ := s.TranscriptCalls(ctx, TranscriptFilter{Limit: 5}); truncated || len(calls) != 5 {
		t.Fatalf("exact limit: %v %d", truncated, len(calls))
	}
	if calls, _, _ := s.TranscriptCalls(ctx, TranscriptFilter{Calls: []string{"call-3", "call-1", "nope"}}); callStates(calls) != "call-3=unfetchable(0/0/1) call-1=fetched(2/2/2)" {
		t.Fatalf("calls: %s", callStates(calls))
	}
	if calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{Account: &acctB}); err != nil || calls != nil {
		t.Fatalf("other account: %v, %v", calls, err)
	}
}

// A call takes its event from the occurrence its notices belong to, and its title from its chat.
func TestTranscriptCallsNameTheEvent(t *testing.T) {
	ctx := context.Background()
	s := tEventSeed(t)
	// Two calls that start at the same instant list by call id; a chat with no display name uses its title.
	rec := transcripts.TypeRecording
	s.qExec(t, `insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('tenant-1','aaaaaaaa-0000-0000-0000-000000000001','19:other@thread.v2','Meeting','Only a title','','2026-11-01T00:00:00.000Z')`)
	s.tMessage(t, acctA, "19:other@thread.v2", "z-1", rec, "2026-11-10T11:06:00.000Z", tNotice("call-0", "0", "Success", "Recording+Transcript", "2026-11-10T10:01:00Z", "drive:Z"))
	s.tMessage(t, acctA, "19:unnamed@thread.v2", "y-1", rec, "2026-11-01T11:06:00.000Z", tNotice("call-y", "0", "Success", "Recording+Transcript", "2026-11-01T10:00:00Z", "drive:Y"))
	tDerive(t, s, qTeams)
	calls, _, err := s.TranscriptCalls(ctx, TranscriptFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range calls {
		got = append(got, c.CallID+"|"+c.Title+"|"+c.EventKey)
	}
	o1, o2 := calendar.Key(qEvent("o1", 3)), calendar.Key(qEvent("o2", 10))
	want := []string{"call-0|Only a title|", "call-2|Q meeting|" + o2, "call-1b|Q meeting|" + o1, "call-1|Q meeting|" + o1, "call-y||"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Without calendar tables the title still comes from the chat.
	s.qExec(t, `drop table calendar_recaps`)
	calls, _, err = s.TranscriptCalls(ctx, TranscriptFilter{Calls: []string{"call-2"}})
	if err != nil || len(calls) != 1 || calls[0].Title != "Q meeting" || calls[0].EventKey != "" {
		t.Fatalf("no calendar: %+v, %v", calls, err)
	}
}

// eventKeyOf is the event key TranscriptCalls prints for one call.
func eventKeyOf(t *testing.T, s *Store, call string) string {
	t.Helper()
	calls, _, err := s.TranscriptCalls(context.Background(), TranscriptFilter{Calls: []string{call}})
	if err != nil || len(calls) != 1 {
		t.Fatalf("%s: %+v, %v", call, calls, err)
	}
	return calls[0].EventKey
}

// A notice re-posted inside a later occurrence's window does not move the call: a call belongs to
// the occurrence of its earliest Success recording notice, and to that one alone.
func TestRepostInALaterOccurrenceKeepsTheCall(t *testing.T) {
	s := tEventSeed(t)
	o1, o3 := calendar.Key(qEvent("o1", 3)), calendar.Key(qEvent("o3", 17))
	s.tMessage(t, acctA, qChat, "t-1b-again", transcripts.TypeRecording, "2026-11-17T10:30:00.000Z", tNotice("call-1b", "0", "Success", "Recording+Transcript", "2026-11-03T10:30:00Z", "drive:E1B"))
	tDerive(t, s, qTeams)
	if calls, _ := resolve(t, s, nil, o1); calls != "call-1b,call-1" {
		t.Fatalf("o1: %q", calls)
	}
	// The re-post does fall in o3's window: the event lists it among its recordings.
	d, err := s.CalendarEvent(context.Background(), nil, o3)
	if err != nil || len(d.Recordings) != 1 || d.Recordings[0].MessageID != "t-1b-again" {
		t.Fatalf("o3's recordings: %+v, %v", d.Recordings, err)
	}
	unknownMeeting(t, s, nil, EventID(qTeams, o3)) // o3 has no recording of its own
	if got := eventKeyOf(t, s, "call-1b"); got != o1 {
		t.Fatalf("call-1b's event %q, want %q", got, o1)
	}
}

// A call's transcript notice in another occurrence's window does not put the call there too. A
// call with only a transcript notice belongs to the occurrence that notice fell in.
func TestTranscriptNoticeInAnotherOccurrence(t *testing.T) {
	s := tEventSeed(t)
	o2, o3 := calendar.Key(qEvent("o2", 10)), calendar.Key(qEvent("o3", 17))
	s.tMessage(t, acctA, qChat, "t-2-tr", transcripts.TypeTranscript, "2026-11-17T10:30:00.000Z", tTranscriptNotice("call-2"))
	tDerive(t, s, qTeams)
	if calls, _ := resolve(t, s, nil, o2); calls != "call-2" {
		t.Fatalf("o2: %q", calls)
	}
	if d, err := s.CalendarEvent(context.Background(), nil, o3); err != nil || len(d.Recordings) != 1 || d.Recordings[0].MessageID != "t-2-tr" {
		t.Fatalf("o3's recordings: %+v, %v", d.Recordings, err)
	}
	unknownMeeting(t, s, nil, EventID(qTeams, o3))
	if got := eventKeyOf(t, s, "call-2"); got != o2 {
		t.Fatalf("call-2's event %q, want %q", got, o2)
	}
	// Only a transcript notice: the call is o3's.
	s.tMessage(t, acctA, qChat, "t-9-tr", transcripts.TypeTranscript, "2026-11-17T10:40:00.000Z", tTranscriptNotice("call-9"))
	tDerive(t, s, qTeams)
	if calls, _ := resolve(t, s, nil, o3); calls != "call-9" {
		t.Fatalf("o3: %q", calls)
	}
	if got := eventKeyOf(t, s, "call-9"); got != o3 {
		t.Fatalf("call-9's event %q, want %q", got, o3)
	}
}

func TestTranscriptFailuresRollBack(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		tSeed(t, s)
		tDerive(t, s, tAcctA)
		must0(s.SaveTranscript(context.Background(), tAcctA, "d:b!d1/ITEM1", okResult(base, transcripts.Entry{Speaker: "Ada", Text: "old"})))
	}
	t.Run("derive", func(t *testing.T) {
		sweepFaults(t, seed, func(ctx context.Context, s *Store) error {
			sess, err := s.Begin(ctx)
			if err != nil {
				return err
			}
			defer sess.Rollback()
			// A new notice makes the rebuild write something a failure must take back.
			if _, err := sess.tx.ExecContext(ctx, `update messages set deleted_at='2026-11-10T00:00:00.000Z' where id='200'`); err != nil {
				return err
			}
			if _, err := sess.DeriveTranscriptParts(ctx, []string{tAcctA}); err != nil {
				return err
			}
			return sess.Commit()
		})
	})
	t.Run("ensure", func(t *testing.T) {
		sweepFaults(t, func(t *testing.T, s *Store) {
			tSeed(t, s)
		}, func(ctx context.Context, s *Store) error {
			_, err := s.EnsureTranscriptParts(ctx)
			return err
		})
	})
	t.Run("save ok", func(t *testing.T) {
		sweepFaults(t, seed, func(ctx context.Context, s *Store) error {
			return s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM1", okResult(base.Add(time.Hour), transcripts.Entry{Speaker: "Bo", Text: "new"}, transcripts.Entry{Text: "more"}))
		})
	})
	t.Run("save failed", func(t *testing.T) {
		sweepFaults(t, seed, func(ctx context.Context, s *Store) error {
			return s.SaveTranscript(ctx, tAcctA, "d:b!d1/ITEM1", transcripts.FetchResult{State: transcripts.StateFailed, At: base.Add(time.Hour)})
		})
	})
}

func TestTranscriptReadFailuresSurface(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		qSeed(t, s)
		rec := transcripts.TypeRecording
		s.tMessage(t, acctA, qChat, "t-1", rec, "2026-11-03T11:10:00.000Z", tNotice("call-1", "0", "Success", "Recording+Transcript", "2026-11-03T10:02:00Z", "drive:E1"))
		s.tMessage(t, acctA, qChat, "t-2", rec, "2026-11-10T11:05:00.000Z", tNotice("call-2", "0", "Success", "Recording+Transcript", "2026-11-10T10:01:00Z", "drive:E2"))
		tDerive(t, s, qTeams)
		must0(s.SaveTranscript(context.Background(), qTeams, "d:b!d1/E1", okResult(base, transcripts.Entry{Speaker: "Ada", StartMS: ms(1), EndMS: ms(2), Text: "x"})))
	}
	o1 := calendar.Key(qEvent("o1", 3))
	ops := map[string]func(context.Context, *Store) error{
		"TranscriptCalls": func(ctx context.Context, s *Store) error {
			_, _, err := s.TranscriptCalls(ctx, TranscriptFilter{Account: &acctA})
			return err
		},
		"TranscriptEntries": func(ctx context.Context, s *Store) error {
			_, err := s.TranscriptEntries(ctx, qTeams, "d:b!d1/E1")
			return err
		},
		"HasTranscriptTables": func(ctx context.Context, s *Store) error { _, err := s.HasTranscriptTables(ctx); return err },
		"ResolveMeeting call": func(ctx context.Context, s *Store) error {
			_, _, err := s.ResolveMeeting(ctx, &acctA, "call-1")
			return err
		},
		"ResolveMeeting event": func(ctx context.Context, s *Store) error {
			_, _, err := s.ResolveMeeting(ctx, nil, o1)
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, seed, op) })
	}
}
