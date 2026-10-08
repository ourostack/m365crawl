package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

const trChat = "19:meeting_transcripts@thread.v2"

// trNotice is a synthetic Success recording notice: with a drive item when item is set, with only
// a media-service link otherwise.
func trNotice(call, chunk, types, stamp, item string) string {
	ref := ""
	if item != "" {
		ref = `<item type="onedriveForBusinessTranscript" uri="https://tenant.sharepoint.example.invalid/teams/site-a/_api/v2.1/drives/b!d1/items/` + item + `/versions/current/media/transcripts/tr-` + item + `/content"/>`
	}
	return `<URIObject type="Video.2/CallRecording.1"><Identifiers><Id type="callId" value="` + call + `"/><Id type="chunkIndex" value="` + chunk + `"/></Identifiers>` +
		`<RecordingStatus status="Success"/><RecordingContent contentTypes="` + types + `" timestamp="` + stamp + `" duration="0:10:00.000">` + ref + `</RecordingContent></URIObject>`
}

func trMessage(a teamsdesktop.Account, id, typ, html string, at time.Time) teamsdesktop.Message {
	return teamsdesktop.Message{TenantID: a.TenantID, UserID: a.UserID, ConversationID: trChat, ID: id, SentAt: at,
		MessageType: typ, ContentType: "Text", ContentHTML: html, Version: 1, Raw: []byte(`{"id":"` + id + `"}`)}
}

// seedNotices puts recording notices of the fixture's first account into a new archive, as an
// earlier sync would have left them: call-a with two parts, call-b with a media-service link only,
// call-c with a transcript notice and no part, and one notice that cannot be read.
func seedNotices(t *testing.T, db string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.ApplyAccount(ctx, acctA); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	rec := transcripts.TypeRecording
	if _, err := st.ApplyMessages(ctx, []teamsdesktop.Message{
		trMessage(acctA, "9001", rec, trNotice("call-a", "10001", "Transcript", "2026-11-03T10:00:00Z", "A1"), day),
		trMessage(acctA, "9002", rec, trNotice("call-a", "0", "Recording+Transcript", "2026-11-03T10:02:00Z", "A2"), day.Add(time.Minute)),
		trMessage(acctA, "9003", rec, trNotice("call-b", "0", "Recording+Transcript", "2026-11-04T10:00:00Z", ""), day.Add(24*time.Hour)),
		trMessage(acctA, "9004", transcripts.TypeTranscript, `{\"callId\":\"call-c\"}`, day.Add(48*time.Hour)),
		trMessage(acctA, "9005", rec, `<URIObject><RecordingStatus status="Success"`, day.Add(72*time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
}

var seededTranscripts = store.TranscriptCounts{Calls: 3, Parts: 3, DriveItem: 2, AMSOnly: 1, Unresolved: 1, Unparsable: 1}

// countDerives counts the source-level derivations of a run.
func countDerives(t *testing.T) *int {
	t.Helper()
	n := new(int)
	old := deriveTranscripts
	t.Cleanup(func() { deriveTranscripts = old })
	deriveTranscripts = func(x *store.Session, ctx context.Context, accounts []string) (map[string]store.TranscriptCounts, error) {
		*n++
		return old(x, ctx, accounts)
	}
	return n
}

func partRows(t *testing.T, db string) string {
	t.Helper()
	return queryStr(t, db, `select group_concat(call_id||'#'||ordinal||' '||ref_quality, '; ') from (select * from transcript_parts where call_id like 'call-%' order by call_id, ordinal)`)
}

func TestSyncReportsTranscriptCounts(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	seedNotices(t, db)
	derives := countDerives(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	// The rebuild at the start of the run and the fixture source's own derivation both cover the
	// account; its parts are counted once.
	if r.Transcripts != seededTranscripts {
		t.Fatalf("transcripts %+v, want %+v", r.Transcripts, seededTranscripts)
	}
	if *derives == 0 {
		t.Fatal("no source derived its accounts' parts")
	}
	if got := partRows(t, db); got != "call-a#1 drive_item; call-a#2 drive_item; call-b#1 ams_only; call-c#1 unresolved" {
		t.Fatalf("parts: %s", got)
	}
	if got := queryStr(t, db, `select value from meta where key='transcript_parts_mapper'`); got != "1" {
		t.Fatalf("mapper %q", got)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"transcripts":{"calls":3,"parts":3,"drive_item":2,"share_only":0,"ams_only":1,"unresolved":1,"unparsable":1}`; !strings.Contains(string(raw), want) {
		t.Fatalf("sync --json lacks %s in %s", want, raw)
	}
	// One unreadable notice is counted and the sync stays ok.
	if r.Status != StatusOK {
		t.Fatalf("status %s", r.Status)
	}
}

func TestSyncUnchangedSourceSkipsDerive(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	seedNotices(t, db)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	derives := countDerives(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != StatusUnchanged || *derives != 0 || r.Transcripts != (store.TranscriptCounts{}) {
		t.Fatalf("status %s, %d derivations, transcripts %+v", r.Status, *derives, r.Transcripts)
	}
	if got := partRows(t, db); got != "call-a#1 drive_item; call-a#2 drive_item; call-b#1 ams_only; call-c#1 unresolved" {
		t.Fatalf("parts changed: %s", got)
	}
}

// An archive whose parts an older mapper derived gets them rebuilt by a sync that reads nothing.
func TestSyncRebuildsPartsOfAnOlderMapper(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	seedNotices(t, db)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	d := openRaw(t, db)
	exec(t, d, `delete from transcript_parts`)
	exec(t, d, `delete from meta where key='transcript_parts_mapper'`)
	derives := countDerives(t)
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Status != StatusUnchanged || *derives != 0 || r.Transcripts != seededTranscripts {
		t.Fatalf("status %s, %d derivations, transcripts %+v", r.Status, *derives, r.Transcripts)
	}
	if got := partRows(t, db); got != "call-a#1 drive_item; call-a#2 drive_item; call-b#1 ams_only; call-c#1 unresolved" {
		t.Fatalf("parts: %s", got)
	}
}

func TestRunFailsWhenTheTranscriptRebuildFails(t *testing.T) {
	isolateTmp(t)
	old := ensureTranscripts
	t.Cleanup(func() { ensureTranscripts = old })
	ensureTranscripts = func(*store.Store, context.Context) (map[string]store.TranscriptCounts, error) {
		return nil, errors.New("rebuild refused")
	}
	db := newDB(t)
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError || !strings.Contains(err.Error(), "rebuild refused") {
		t.Fatalf("err = %v", err)
	}
	if st := readStatus(t, db); st.LastRun == nil || st.LastRun.Status != StatusFailed {
		t.Fatalf("last run %+v", st.LastRun)
	}
}

// A source whose transcript derivation fails keeps nothing: its messages are rolled back with it.
func TestSourceFailsWhenTheTranscriptDerivationFails(t *testing.T) {
	isolateTmp(t)
	old := deriveTranscripts
	t.Cleanup(func() { deriveTranscripts = old })
	deriveTranscripts = func(*store.Session, context.Context, []string) (map[string]store.TranscriptCounts, error) {
		return nil, errors.New("derivation refused")
	}
	db := newDB(t)
	r, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError || !strings.Contains(err.Error(), "derivation refused") || r.Status != StatusFailed {
		t.Fatalf("status %s, err = %v", r.Status, err)
	}
	if n := queryStr(t, db, `select count(*) from messages`); n != "0" {
		t.Fatalf("%s messages kept from a source that failed", n)
	}
}
