package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

const searchMeeting = "19:meeting_search@thread.v2"

// seedSearchTranscripts adds one recorded call to a search archive, in two fetched parts that land
// between the fixture's chat messages and the synthetic mail: part 1 at 22:14 with two entries
// that say hello, part 2 at 22:20 with one.
func seedSearchTranscripts(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	e.exec(`insert into conversations(tenant_id,user_id,id,kind,title,display_name,updated_at) values('` + tenantA + `','` + userA + `','` + searchMeeting + `','Meeting','','Search review','2023-11-01T00:00:00.000Z')`)
	for _, p := range []struct{ key, ord, starts string }{{"d:b!s/T1", "1", "2023-11-14T22:14:00.000Z"}, {"d:b!s/T2", "2", "2023-11-14T22:20:00.000Z"}} {
		e.exec(`insert into transcript_parts(account_id, call_id, part_key, thread_id, message_id, ordinal, starts_at, ref_quality, sent_at, drive_id, item_id)
		  values('` + trAccount + `','call-s1','` + p.key + `','` + searchMeeting + `','m` + p.ord + `',` + p.ord + `,'` + p.starts + `','drive_item','2023-11-14T23:00:00.000Z','b!s','T` + p.ord + `')`)
	}
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ok := func(entries ...transcripts.Entry) transcripts.FetchResult {
		return transcripts.FetchResult{State: transcripts.StateOK, HTTPStatus: 200, Entries: entries, Browser: "edge", At: trFetchedAt}
	}
	for key, r := range map[string]transcripts.FetchResult{
		"d:b!s/T1": ok(transcripts.Entry{Speaker: "Dee Example", StartMS: trMS(0), Text: "Hello all, the first point is short."},
			transcripts.Entry{Speaker: "Eve Example", StartMS: trMS(30_000), Text: "hello again from me"},
			transcripts.Entry{Speaker: "Dee Example", StartMS: trMS(60_000), Text: "nothing to add"}),
		"d:b!s/T2": ok(transcripts.Entry{Speaker: "Eve Example", StartMS: trMS(0), Text: "and hello once more"}),
	} {
		if err := st.SaveTranscript(ctx, trAccount, key, r); err != nil {
			t.Fatal(err)
		}
	}
}

func searchTranscriptEnv(t *testing.T) *env {
	t.Helper()
	e := searchMailEnv(t)
	seedSearchTranscripts(t, e)
	return e
}

func TestSearchAllMergesTranscriptsNewestFirst(t *testing.T) {
	e := searchTranscriptEnv(t)
	m := searchJSON(t, e, "Hello")
	its := items(t, m)
	if got := strings.Join(searchSourcesOf(its), " "); got != "transcripts mail transcripts chats chats mail mail mail" {
		t.Fatalf("sources = %s", got)
	}
	for i := 1; i < len(its); i++ {
		if itemAt(t, its[i]).After(itemAt(t, its[i-1])) {
			t.Fatalf("item %d is newer than item %d", i, i-1)
		}
	}
	src := m["sources"].(map[string]any)
	if tr := src["transcripts"].(map[string]any); tr["count"] != float64(2) || tr["truncated"] != false {
		t.Fatalf("sources.transcripts = %v", tr)
	}
	// The limit cuts the merged list and says which source held more.
	m = searchJSON(t, e, "Hello", "--limit", "2")
	src = m["sources"].(map[string]any)
	if m["truncated"] != true || src["transcripts"].(map[string]any)["truncated"] != true || src["transcripts"].(map[string]any)["count"] != float64(1) {
		t.Fatalf("limit 2: %v", m)
	}
	if _, has := m["total"]; has {
		t.Fatalf("the chats' total is not the result's: %v", m["total"])
	}
}

func TestSearchTranscriptsSource(t *testing.T) {
	e := searchTranscriptEnv(t)
	m := searchJSON(t, e, "hello", "--source", "transcripts")
	its := items(t, m)
	if len(its) != 2 || m["note"] != nil {
		t.Fatalf("items %v note %v", its, m["note"])
	}
	src := m["sources"].(map[string]any)
	if _, has := src["chats"]; has || src["mail"] != nil {
		t.Fatalf("only transcripts were searched: %v", src)
	}
	want := map[string]any{"source": "transcripts", "call_id": "call-s1", "event_key": nil, "title": "Search review", "ordinal": float64(2), "speaker": "Eve Example",
		"at": "2023-11-14T22:20:00Z", "text": "and hello once more", "matches": float64(1), "fetched_at": "2026-11-09T08:00:00Z"}
	for k, v := range want {
		if its[0][k] != v {
			t.Errorf("%s = %v, want %v", k, its[0][k], v)
		}
	}
	if len(its[0]) != len(want) {
		t.Errorf("keys %v", its[0])
	}
	// --fields takes the keys of a transcript item; --max-text cuts its text.
	m = searchJSON(t, e, "hello", "--source", "transcripts", "--fields", "source,call_id,ordinal,matches", "--max-text", "5")
	if got := items(t, m)[1]; len(got) != 4 || got["matches"] != float64(2) {
		t.Fatalf("fields: %v", got)
	}
	m = searchJSON(t, e, "hello", "--source", "transcripts", "--max-text", "5")
	if got := items(t, m)[0]; got["text"] != "and h…" || got["text_truncated"] != true {
		t.Fatalf("max-text: %v", got)
	}
	// Bounds are by each entry's time.
	m = searchJSON(t, e, "hello", "--source", "transcripts", "--since", "2023-11-14T22:14:10Z", "--until", "2023-11-14T22:15:00Z")
	if its := items(t, m); len(its) != 1 || its[0]["speaker"] != "Eve Example" || its[0]["matches"] != float64(1) {
		t.Fatalf("bounds: %v", its)
	}
	// Nothing matched in the transcripts that are there.
	m = searchJSON(t, e, "zzzqqq", "--source", "transcripts")
	if m["note"] != "no transcript matched the search words and filters" || m["count"] != float64(0) {
		t.Fatalf("no match: %v", m)
	}
	// With every source searched and nothing matched, the transcripts are named too.
	m = searchJSON(t, e, "zzzqqq")
	if m["note"] != "no chat message, mail or transcript matched the search words and filters" {
		t.Fatalf("no match anywhere: %v", m["note"])
	}
	// No words and no filter is a usage error for transcripts too.
	if er := searchFails(t, e, 2, "", "--source", "transcripts"); !strings.Contains(er["message"].(string), "at least one filter") {
		t.Fatalf("no words: %v", er)
	}
}

func TestSearchTranscriptsCollapsePerPart(t *testing.T) {
	e := searchTranscriptEnv(t)
	its := items(t, searchJSON(t, e, "hello", "--source", "transcripts"))
	// Part 1 has two matching entries: one item, its first match, and how many matched.
	p1 := its[1]
	if p1["ordinal"] != float64(1) || p1["matches"] != float64(2) || p1["speaker"] != "Dee Example" || p1["text"] != "Hello all, the first point is short." || p1["at"] != "2023-11-14T22:14:00Z" {
		t.Fatalf("part 1: %v", p1)
	}
}

func TestSearchTranscriptsFromIsSpeaker(t *testing.T) {
	e := searchTranscriptEnv(t)
	its := items(t, searchJSON(t, e, "hello", "--source", "transcripts", "--from", "eve ex"))
	if len(its) != 2 || its[0]["ordinal"] != float64(2) || its[1]["speaker"] != "Eve Example" || its[1]["at"] != "2023-11-14T22:14:30Z" || its[1]["matches"] != float64(1) {
		t.Fatalf("from eve: %v", its)
	}
	// --from alone is a filter: no words needed.
	its = items(t, searchJSON(t, e, "--source", "transcripts", "--from", "Dee"))
	if len(its) != 1 || its[0]["matches"] != float64(2) || its[0]["text"] != "Hello all, the first point is short." {
		t.Fatalf("from dee: %v", its)
	}
	// The words look in what was said, not in who said it.
	if its := items(t, searchJSON(t, e, "Dee", "--source", "transcripts")); len(its) != 0 {
		t.Fatalf("a speaker's name as words: %v", its)
	}
}

func TestSearchTranscriptsFlagConflict(t *testing.T) {
	e := searchTranscriptEnv(t)
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--folder", "inbox"}, []string{"--folder applies to mail only", "--source transcripts searches meeting transcripts"}},
		{[]string{"--mentions-me"}, []string{"--mentions-me applies to Teams chats only", "--source transcripts"}},
		{[]string{"--mentions-me", "--html", "--folder", "inbox"}, []string{"--mentions-me and --html apply to Teams chats only and --folder applies to mail only"}},
		{[]string{"--account", searchMailAccount}, []string{"names a mail account", "--source transcripts"}},
	} {
		er := searchFails(t, e, 2, append([]string{"hello", "--source", "transcripts"}, c.args...)...)
		for _, w := range c.want {
			if er["code"] != "flag_source_conflict" || !strings.Contains(er["message"].(string), w) {
				t.Errorf("%v: %v", c.args, er)
			}
		}
	}
	// With every source, a one-source flag narrows transcripts away and says so.
	m := searchJSON(t, e, "hello", "--folder", "inbox")
	if _, has := m["sources"].(map[string]any)["transcripts"]; has || !strings.Contains(m["note"].(string), "meeting transcripts were not searched") {
		t.Fatalf("folder: %v", m)
	}
	m = searchJSON(t, e, "hello", "--account", searchMailAccount)
	if _, has := m["sources"].(map[string]any)["transcripts"]; has || !strings.Contains(m["note"].(string), "meeting transcripts were not searched") {
		t.Fatalf("mail account: %v", m)
	}
	// A Teams account narrows the transcripts to that account.
	if its := items(t, searchJSON(t, e, "hello", "--source", "transcripts", "--account", "00000000-0000-4000-8000-000000000002/00000000-0000-4000-8000-0000000000a2")); len(its) != 0 {
		t.Fatalf("another account: %v", its)
	}
}

func TestSearchTranscriptsNoneFetchedNote(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "hello", "--source", "transcripts")
	if m["note"] != noTranscriptText || m["count"] != float64(0) {
		t.Fatalf("none fetched: %v", m)
	}
	if tr := m["sources"].(map[string]any)["transcripts"].(map[string]any); tr["count"] != float64(0) {
		t.Fatalf("sources: %v", tr)
	}
	// With every source the transcripts add no note: nothing of theirs could match.
	m = searchJSON(t, e, "zzzqqq")
	if m["note"] != "no chat message or mail matched the search words and filters" {
		t.Fatalf("all: %v", m["note"])
	}
	// An archive from before the transcript tables says to sync.
	for _, tbl := range []string{"transcript_fts", "transcript_entries", "transcript_fetches", "transcript_parts"} {
		e.exec("drop table " + tbl)
	}
	m = searchJSON(t, e, "hello", "--source", "transcripts")
	if m["note"] != noTranscriptTables || m["sources"].(map[string]any)["transcripts"] != nil {
		t.Fatalf("no tables: %v", m)
	}
	if m = searchJSON(t, e, "Hello"); m["count"] != float64(6) || m["note"] != nil {
		t.Fatalf("no tables, every source: %v", m)
	}
	// No archive at all.
	fresh := searchEnv(t)
	if m := searchJSON(t, fresh, "hello", "--source", "transcripts"); m["count"] != float64(0) {
		t.Fatalf("no archive: %v", m)
	}
}

func TestSearchTranscriptsTextOutput(t *testing.T) {
	e := searchTranscriptEnv(t)
	for _, color := range []bool{false, true} {
		suffix := "plain"
		t.Setenv("CLICOLOR_FORCE", "")
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.run("--format", "text", "--max-age", "0", "search", "hello", "--until", "2023-11-14T23:00:00Z")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errOut)
		}
		checkGolden(t, "search_transcripts."+suffix, e.scrub(out))
	}
}

func TestSearchTranscriptsFailuresAreReported(t *testing.T) {
	for _, broken := range []string{"drop table transcript_fts", "alter table transcript_entries rename column text to said"} {
		e := searchTranscriptEnv(t)
		e.exec(broken)
		code, _, errOut := e.run("--max-age", "0", "--json", "search", "hello", "--source", "transcripts")
		if code == 0 || errorOf(t, errOut)["code"] == nil {
			t.Fatalf("%s: a broken transcript table must fail the search: %d %s", broken, code, errOut)
		}
	}
}
