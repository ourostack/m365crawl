package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/store"
)

func rangeCoverage(t *testing.T, m map[string]any) []map[string]any {
	t.Helper()
	raw, _ := m["range_coverage"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func scopesOf(entries []map[string]any) string {
	var out []string
	for _, e := range entries {
		s, _ := e["scope"].(string)
		out = append(out, e["source"].(string)+":"+s)
	}
	return strings.Join(out, ",")
}

// A bounded search judges what it searched: chats like messages, mail like mail list, and no
// other source; its zero hits carry the verdict too.
func TestSearchRangeCoverage(t *testing.T) {
	e, outlook := covEnv(t)
	search := func(args ...string) map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "search"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return decode(t, out)
	}
	since := mailT0.AddDate(0, 0, -5).Format(time.RFC3339)
	until := mailT0.AddDate(0, 0, -4).Format(time.RFC3339)
	m := search("--since", since, "--until", until, "nothingmatchesthis")
	rc := rangeCoverage(t, m)
	if got := scopesOf(rc); got != "chats:chats,chats:meetings,chats:channels,mail:sent,mail:inbox" || m["count"] != float64(0) {
		t.Fatalf("all sources %s %v", got, m["count"])
	}
	wantEntry(t, rc[0], "covered", 2, "")
	wantEntry(t, rc[4], "covered", 2, "")
	if got := scopesOf(rangeCoverage(t, search("--source", "mail", "--folder", "sent", "--since", since))); got != "mail:sent" {
		t.Fatalf("mail sent %s", got)
	}
	if got := scopesOf(rangeCoverage(t, search("--source", "chats", "--since", since, "--limit", "1"))); got != "chats:chats,chats:meetings,chats:channels" {
		t.Fatalf("chats %s", got)
	}
	if m := search("--source", "mail", "--folder", "unknown", "--since", since, "words"); m["range_coverage"] != nil {
		t.Fatalf("folder unknown %v", m["range_coverage"])
	}
	if m := search("--source", "notes", "--since", since, "words"); m["range_coverage"] != nil {
		t.Fatalf("notes %v", m["range_coverage"])
	}
	if m := search("words"); m["range_coverage"] != nil {
		t.Fatalf("unbounded %v", m["range_coverage"])
	}
	// A Teams --account still searches every mailbox, so every mailbox is judged.
	if got := scopesOf(rangeCoverage(t, search("--account", covTeams, "--since", since, "--until", until, "words"))); got != "chats:chats,chats:meetings,chats:channels,mail:sent,mail:inbox" {
		t.Fatalf("teams account %s", got)
	}
	// An Outlook --account keeps mail to it and names no chats.
	if got := scopesOf(rangeCoverage(t, search("--account", mailAccount, "--source", "mail", "--since", since, "words"))); got != "mail:sent,mail:inbox" {
		t.Fatalf("mail account %s", got)
	}
	// A chat flag narrows to chats, and to the conversation it names.
	rc = rangeCoverage(t, search("--conversation", "19:a@unq.gbl.spaces", "--since", since, "--until", until))
	if scopesOf(rc) != "chats:chats" || rc[0]["narrowed_to"] != "conversation" {
		t.Fatalf("narrowed %v", rc)
	}
	for _, fail := range []struct {
		name string
		set  func(t *testing.T)
		args []string
	}{
		{"health", func(t *testing.T) {
			setSeam(t, &readSourceHealth, func(*store.Store, context.Context) ([]store.SourceHealth, error) { return nil, errors.New("broken") })
		}, []string{"--since", since, "words"}},
		{"chats", func(t *testing.T) {
			setSeam(t, &teamsAccountsOf, func(*store.Store, context.Context) ([]store.WhoamiRow, error) { return nil, errors.New("broken") })
		}, []string{"--since", since, "words"}},
		{"folders", func(t *testing.T) {
			setSeam(t, &mailFoldersOf, func(*store.Store, context.Context) ([]store.MailFolderRow, error) { return nil, errors.New("broken") })
		}, []string{"--source", "mail", "--since", since, "words"}},
	} {
		t.Run(fail.name, func(t *testing.T) {
			fail.set(t)
			if code, _, _ := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "search"}, fail.args...)...); code != 1 {
				t.Fatalf("exit %d", code)
			}
		})
	}
}

// Without any mail folder, a bounded mail search says why there is no mail rather than nothing.
func TestSearchRangeCoverageWithoutMail(t *testing.T) {
	fixClock(t, covNow)
	e := textEnv(t)
	e.emptyArchive()
	outlook := t.TempDir()
	code, out, errOut := e.run("--no-sync", "--json", "--outlook-root", outlook, "search", "--source", "mail", "--since", "2026-09-01", "words")
	if code != 0 {
		t.Fatal(code, errOut)
	}
	rc := rangeCoverage(t, decode(t, out))
	if scopesOf(rc) != "mail:sent,mail:inbox" || reasonCodes(rc[0]) != "no_profile" {
		t.Fatalf("%v", rc)
	}
	code, out, errOut = e.run("--no-sync", "--json", "--outlook-root", outlook, "search", "--source", "mail", "--folder", "inbox", "--since", "2026-09-01", "words")
	if rc := rangeCoverage(t, decode(t, out)); code != 0 || scopesOf(rc) != "mail:sent,mail:inbox" {
		t.Fatalf("with --folder: %d %s %v", code, errOut, rc)
	}
}

// --conversation and --team keep messages' verdict to what the read selects, say so in
// narrowed_to, and a selection the archive does not hold is not covered.
func TestMessagesRangeCoverageNarrows(t *testing.T) {
	e, _ := covEnv(t)
	e.exec(`insert into conversations(tenant_id,user_id,id,kind,display_name,team_id,last_message_at,updated_at) values
 ('t1','u1','19:team@thread.tacv2','Space','Fixture team','19:team@thread.tacv2','2026-09-05T08:00:00.000Z','2026-09-10T08:00:00.000Z'),
 ('t1','u1','19:gen@thread.tacv2','Topic','General','19:team@thread.tacv2','2026-09-05T08:00:00.000Z','2026-09-10T08:00:00.000Z')`)
	e.exec(`insert into messages(tenant_id,user_id,conversation_id,id,sender_id,message_type,sent_at,raw_json,updated_at) values
 ('t1','u1','19:team@thread.tacv2','t1','8:orgid:u1','Text','2026-09-05T08:00:00.000Z','{"sequenceId":1}','2026-09-10T08:00:00.000Z'),
 ('t1','u1','19:gen@thread.tacv2','g1','8:orgid:u1','Text','2026-09-05T08:00:00.000Z','{"sequenceId":1}','2026-09-10T08:00:00.000Z')`)
	read := func(args ...string) []map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "messages", "--since", covDay(2), "--until", covDay(2)}, args...)...)
		if code != 0 {
			t.Fatal(args, code, errOut)
		}
		return rangeCoverage(t, decode(t, out))
	}
	all := read()
	if scopesOf(all) != "chats:chats,chats:meetings,chats:channels" || all[1]["state"] != "partial" || all[0]["narrowed_to"] != nil {
		t.Fatalf("unnarrowed %v", all)
	}
	rc := read("--conversation", "19:a@unq.gbl.spaces")
	if scopesOf(rc) != "chats:chats" || rc[0]["narrowed_to"] != "conversation" || rc[0]["state"] != "covered" {
		t.Fatalf("1:1 chat %v", rc)
	}
	rc = read("--conversation", "19:meeting_m@thread.v2")
	if scopesOf(rc) != "chats:meetings" || rc[0]["state"] != "partial" || reasonCodes(rc[0]) != "sequence_gap" {
		t.Fatalf("meeting %v", rc)
	}
	rc = read("--team", "Fixture team")
	if scopesOf(rc) != "chats:channels" || rc[0]["narrowed_to"] != "team" {
		t.Fatalf("team %v", rc)
	}
	rc = read("--conversation", "nobody")
	if scopesOf(rc) != "chats:" || rc[0]["state"] != "not_covered" || reasonCodes(rc[0]) != "conversation_not_archived" || rc[0]["account"] != covTeams ||
		!strings.Contains(rc[0]["fix"].(string), "Teams") {
		t.Fatalf("unknown %v", rc)
	}
}

func agendaWith(accounts []calendar.AccountCoverage, principals ...string) func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
	return func(_ *store.Store, _ context.Context, f store.CalendarFilter) (store.CalendarAgenda, error) {
		var a store.CalendarAgenda
		if f.Query != "" || f.IncludeCancelled || f.Limit < 1000 {
			return a, errors.New("range_coverage must count with the default filters and no limit")
		}
		a.Accounts = accounts
		for _, p := range principals {
			a.Rows = append(a.Rows, store.CalendarRow{AgendaItem: calendar.AgendaItem{Principal: p}})
		}
		return a, nil
	}
}

// The agenda's range_coverage has one entry per principal: covered on the days a cache showed,
// partial when only some, with the principal's events counted by the default filters.
func TestCalendarRangeCoverage(t *testing.T) {
	calendarRangeEnv := func(t *testing.T) *env {
		t.Helper()
		fixClock(t, covNow)
		e := textEnv(t)
		e.emptyArchive()
		return e
	}
	e := calendarRangeEnv(t)
	cal := func(args ...string) []map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "calendar"}, args...)...)
		if code != 0 {
			t.Fatal(args, code, errOut)
		}
		return rangeCoverage(t, decode(t, out))
	}
	rc := cal("--from", "2026-09-07T00:00:00Z", "--to", "2026-09-10T00:00:00Z")
	if len(rc) != 1 || reasonCodes(rc[0]) != "never_read" {
		t.Fatalf("no calendar %v", rc)
	}
	setSeam(t, &calendarAgendaOf, agendaWith([]calendar.AccountCoverage{{AccountID: covTeams}, {AccountID: "outlook/Main", Uncovered: []string{"2026-09-08"}},
		{AccountID: "outlook/Other", Uncovered: []string{"2026-09-07", "2026-09-08", "2026-09-09"}}}, covTeams, covTeams, "outlook/Main"))
	rc = cal("--from", "2026-09-07T00:00:00Z", "--to", "2026-09-10T00:00:00Z", "--query", "plan", "--limit", "1", "--include-cancelled")
	if len(rc) != 3 {
		t.Fatalf("principals %v", rc)
	}
	wantEntry(t, rc[0], "covered", 2, "")
	wantEntry(t, rc[1], "partial", 1, "day_not_shown")
	wantEntry(t, rc[2], "not_covered", 0, "day_not_shown")
	if rc[1]["covered_from"] != "2026-09-07T00:00:00Z" || rc[1]["covered_until"] != "2026-09-10T00:00:00Z" ||
		!strings.HasPrefix(rc[1]["reasons"].([]any)[0].(map[string]any)["detail"].(string), "1 of 3 days") || rc[2]["basis"] != nil {
		t.Fatalf("outlook %v", rc[1])
	}
	// A range that starts inside a day is clipped to it.
	rc = cal("--from", "2026-09-07T12:00:00Z", "--to", "2026-09-08T06:00:00Z")
	if rc[0]["covered_from"] != "2026-09-07T12:00:00Z" || rc[0]["covered_until"] != "2026-09-08T06:00:00Z" {
		t.Fatalf("clipped %v", rc[0])
	}
	setSeam(t, &calendarAgendaOf, func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
		return store.CalendarAgenda{}, errors.New("broken")
	})
	if code, _, _ := e.run("--no-sync", "--json", "calendar"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	// Without an archive the agenda still says the calendar was never read.
	e2 := textEnv(t)
	code, out, _ := e2.run("--no-sync", "--json", "calendar")
	if rc := rangeCoverage(t, decode(t, out)); code != 0 || len(rc) != 1 || reasonCodes(rc[0]) != "never_read" {
		t.Fatalf("no archive %v", rc)
	}
}

// A range with no start routes chat counting to WorkIQ's default window, never to a year-one date.
func TestOpenStartChatRouteHasNoSince(t *testing.T) {
	e, _ := covEnv(t)
	code, out, errOut := e.run("--no-sync", "--json", "messages", "--until", covDay(2))
	if code != 0 {
		t.Fatal(code, errOut)
	}
	rc := rangeCoverage(t, decode(t, out))
	next := rc[0]["next"].([]any)
	last := next[len(next)-1].(map[string]any)
	if cmd := last["command"].(string); !strings.HasSuffix(cmd, "workiq coverage --source chats") || !strings.Contains(last["does"].(string), "default window") {
		t.Fatalf("route %v", last)
	}
}
