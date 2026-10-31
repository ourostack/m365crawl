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

func TestCoverageFlagsAreChecked(t *testing.T) {
	e, outlook := covEnv(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--day", "2026-09-01", "--since", "2026-09-01"}, "either --day or --since"},
		{[]string{"--until", "2026-09-01"}, "--until needs --since"},
		{[]string{"--day", "monday"}, "--day takes a day"},
		{[]string{"--since", "9/1"}, "--since takes a day"},
		{[]string{"--since", "2026-09-01", "--until", "soon"}, "--until takes a day"},
		{[]string{"--since", "2026-09-05", "--until", "2026-09-01"}, "--until is before --since"},
		{[]string{"--since", "2026-07-01", "--until", "2026-08-01"}, "at most 31 days"},
		{[]string{"--source", "mail,email"}, "is not a coverage source"},
		{[]string{"--tz", "Mars/Olympus"}, "not an IANA time zone"},
	} {
		code, _, stderr := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "coverage"}, c.args...)...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Fatalf("%v: exit %d %s", c.args, code, stderr)
		}
	}
	if code, _, stderr := e.run("--no-sync", "--json", "--fields", "day", "coverage"); code != 2 || !strings.Contains(stderr, "--fields") {
		t.Fatalf("fields: exit %d %s", code, stderr)
	}
	// 31 days is the most one read takes; today and yesterday are days.
	m := covRun(t, e, outlook, "--since", "2026-08-11", "--until", "2026-09-10", "--source", "calendar")
	if days := m["days"].([]any); len(days) != 31 {
		t.Fatalf("days %d", len(days))
	}
	m = covRun(t, e, outlook, "--since", "yesterday", "--source", "chats")
	if days := m["days"].([]any); len(days) != 2 || days[0].(map[string]any)["day"] != covDay(1) {
		t.Fatalf("days %v", days)
	}
	m = covRun(t, e, outlook, "--source", "chats")
	if days := m["days"].([]any); len(days) != 1 || days[0].(map[string]any)["day"] != covDay(0) {
		t.Fatalf("days %v", days)
	}
}

// Without an archive every source says it has never been read; --account keeps each source to
// its own kind of account.
func TestCoverageWithoutAnArchiveAndPerAccount(t *testing.T) {
	fixClock(t, covNow)
	e := textEnv(t)
	outlook := t.TempDir()
	m := covRun(t, e, outlook, "--day", covDay(1), "--source", "mail:sent,chats,calendar")
	wantEntry(t, entryOf(t, m, covDay(1), "mail", "sent"), "not_covered", 0, "no_profile")
	wantEntry(t, entryOf(t, m, covDay(1), "chats", "chats"), "not_covered", 0, "never_read")
	wantEntry(t, entryOf(t, m, covDay(1), "calendar", ""), "not_covered", 0, "never_read")
	if m["needs_sync"] != true {
		t.Fatalf("result %v", m)
	}

	e, outlook = covEnv(t)
	m = covRun(t, e, outlook, "--account", mailAccount, "--day", covDay(4), "--source", "mail:sent,chats")
	if entries := m["days"].([]any)[0].(map[string]any)["entries"].([]any); len(entries) != 1 || entries[0].(map[string]any)["source"] != "mail" {
		t.Fatalf("mail account %v", entries)
	}
	m = covRun(t, e, outlook, "--account", covTeams, "--day", covDay(4), "--source", "mail:sent,chats")
	if entries := m["days"].([]any)[0].(map[string]any)["entries"].([]any); len(entries) != 1 || entries[0].(map[string]any)["source"] != "chats" {
		t.Fatalf("teams account %v", entries)
	}
	m = covRun(t, e, outlook, "--account", "outlook/Other", "--day", covDay(4), "--source", "mail:sent")
	wantEntry(t, entryOf(t, m, covDay(4), "mail", "sent"), "not_covered", 0, "no_profile")
	m = covRun(t, e, outlook, "--account", "t1/other", "--day", covDay(4), "--source", "chats")
	wantEntry(t, entryOf(t, m, covDay(4), "chats", "chats"), "not_covered", 0, "never_read")
}

// The calendar is covered on a day a cache showed, per principal, with that principal's events.
func TestCoverageOfCalendarPrincipals(t *testing.T) {
	e, outlook := covEnv(t)
	setSeam(t, &calendarAgendaOf, func(_ *store.Store, _ context.Context, f store.CalendarFilter) (store.CalendarAgenda, error) {
		var a store.CalendarAgenda
		a.Accounts = []calendar.AccountCoverage{{AccountID: covTeams}, {AccountID: "outlook/Main", Uncovered: []string{f.From.Format("2006-01-02")}}}
		a.Rows = []store.CalendarRow{{AgendaItem: calendar.AgendaItem{Principal: covTeams}}, {AgendaItem: calendar.AgendaItem{Principal: covTeams}}, {AgendaItem: calendar.AgendaItem{Principal: "outlook/Main"}}}
		return a, nil
	})
	m := covRun(t, e, outlook, "--day", covDay(5), "--source", "calendar")
	entries := m["days"].([]any)[0].(map[string]any)["entries"].([]any)
	teams, outlookEntry := entries[0].(map[string]any), entries[1].(map[string]any)
	wantEntry(t, teams, "covered", 2, "")
	wantEntry(t, outlookEntry, "not_covered", 1, "day_not_shown")
	if teams["account"] != covTeams || teams["basis"] != "desktop_cache" || !strings.Contains(outlookEntry["fix"].(string), "calendar") {
		t.Fatalf("entries %v", entries)
	}
	setSeam(t, &calendarAgendaOf, func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
		return store.CalendarAgenda{}, errors.New("broken")
	})
	if code, _, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "coverage", "--source", "calendar"); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

// A failure behind any source is the read's error, not a partial answer.
func TestCoverageReportsArchiveFailures(t *testing.T) {
	fail := errors.New("broken")
	e, outlook := covEnv(t)
	run := func(t *testing.T, source string) {
		t.Helper()
		if code, _, stderr := e.run("--no-sync", "--json", "--outlook-root", outlook, "coverage", "--day", covDay(4), "--source", source); code != 1 || !strings.Contains(stderr, "db_error") {
			t.Fatalf("%s: exit %d %s", source, code, stderr)
		}
	}
	t.Run("health", func(t *testing.T) {
		setSeam(t, &readSourceHealth, func(*store.Store, context.Context) ([]store.SourceHealth, error) { return nil, fail })
		run(t, "chats")
	})
	t.Run("teams accounts", func(t *testing.T) {
		setSeam(t, &teamsAccountsOf, func(*store.Store, context.Context) ([]store.WhoamiRow, error) { return nil, fail })
		run(t, "chats")
	})
	t.Run("chat evidence", func(t *testing.T) {
		setSeam(t, &chatEvidence, func(*store.Store, context.Context, string, string, string, time.Time, time.Time) (store.ChatEvidence, error) {
			return store.ChatEvidence{}, fail
		})
		run(t, "chats")
	})
	t.Run("mail count", func(t *testing.T) {
		setSeam(t, &mailRoleCount, func(*store.Store, context.Context, string, string, time.Time, time.Time) (store.MailCopyCheck, error) {
			return store.MailCopyCheck{}, fail
		})
		run(t, "mail")
	})
	t.Run("mail folders", func(t *testing.T) {
		e2, outlook2 := covEnv(t)
		e2.exec(`alter table mail_folders rename to mail_folders_real`)
		e2.exec(`create view mail_folders as select account from mail_folders_real`)
		if code, _, _ := e2.run("--no-sync", "--json", "--outlook-root", outlook2, "coverage", "--source", "mail"); code != 1 {
			t.Fatalf("exit %d", code)
		}
	})
}

// A bounded mail list carries range_coverage for Sent Items and the Inbox among the selected
// folders, whatever its other filters, its limit and its hits; an unbounded one has none.
func TestMailListRangeCoverage(t *testing.T) {
	e, outlook := covEnv(t)
	list := func(args ...string) map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "mail", "list"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return decode(t, out)
	}
	since := mailT0.AddDate(0, 0, -5).Format(time.RFC3339)
	until := mailT0.AddDate(0, 0, -4).Format(time.RFC3339)
	m := list("--since", since, "--until", until, "--from", "nobody", "--limit", "1", "--fields", "id")
	rc := m["range_coverage"].([]any)
	if len(rc) != 2 || rc[0].(map[string]any)["scope"] != "sent" || rc[1].(map[string]any)["scope"] != "inbox" {
		t.Fatalf("range_coverage %v", rc)
	}
	wantEntry(t, rc[1].(map[string]any), "covered", 2, "")
	if m["count"] != float64(0) {
		t.Fatalf("count %v", m["count"])
	}
	if rc := list("--folder", "sent", "--since", since)["range_coverage"].([]any); len(rc) != 1 || rc[0].(map[string]any)["state"] != "partial" {
		t.Fatalf("open end %v", rc)
	}
	if m := list("--folder", "drafts", "--since", since); m["range_coverage"] != nil {
		t.Fatalf("drafts %v", m["range_coverage"])
	}
	if m := list(); m["range_coverage"] != nil {
		t.Fatalf("unbounded %v", m["range_coverage"])
	}
	// A hole inside the archived range routes to WorkIQ although the range starts inside the cache.
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	m = list("--folder", "sent", "--since", mailT0.AddDate(0, 0, -11).Format(time.RFC3339), "--until", mailT0.AddDate(0, 0, -10).Add(-time.Hour).Format(time.RFC3339))
	routes, _ := m["workiq_routes"].([]any)
	if len(routes) != 1 || routes[0].(map[string]any)["reason"] != "range_not_covered" {
		t.Fatalf("routes %v", routes)
	}
	// A read before the cache keeps its one route per account.
	m = list("--folder", "sent", "--since", "2026-01-01")
	if routes, _ := m["workiq_routes"].([]any); len(routes) != 1 || routes[0].(map[string]any)["reason"] != "range_before_cache" {
		t.Fatalf("routes %v", routes)
	}
	setSeam(t, &mailFoldersOf, func(*store.Store, context.Context) ([]store.MailFolderRow, error) { return nil, errors.New("broken") })
	if code, _, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "mail", "list", "--since", since); code != 1 {
		t.Fatalf("exit %d", code)
	}
	setSeam(t, &mailFoldersOf, (*store.Store).MailFolders)
	setSeam(t, &readSourceHealth, func(*store.Store, context.Context) ([]store.SourceHealth, error) { return nil, errors.New("broken") })
	if code, _, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "mail", "list", "--since", since); code != 1 {
		t.Fatalf("exit %d", code)
	}
	setSeam(t, &readSourceHealth, (*store.Store).ReadSourceHealth)
	setSeam(t, &mailRoleCount, func(*store.Store, context.Context, string, string, time.Time, time.Time) (store.MailCopyCheck, error) {
		return store.MailCopyCheck{}, errors.New("broken")
	})
	if code, _, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "mail", "list", "--since", since); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

// A bounded messages read carries range_coverage for chats, meeting chats and channels.
func TestMessagesRangeCoverage(t *testing.T) {
	e, _ := covEnv(t)
	read := func(args ...string) (int, map[string]any) {
		t.Helper()
		code, out, _ := e.run(append([]string{"--no-sync", "--json", "messages"}, args...)...)
		if code != 0 {
			return code, nil
		}
		return code, decode(t, out)
	}
	since := mailT0.AddDate(0, 0, -5).Format(time.RFC3339)
	_, m := read("--since", since, "--until", mailT0.AddDate(0, 0, -4).Format(time.RFC3339), "--limit", "1")
	rc := m["range_coverage"].([]any)
	if len(rc) != 3 || rc[0].(map[string]any)["scope"] != "chats" || rc[2].(map[string]any)["scope"] != "channels" {
		t.Fatalf("range_coverage %v", rc)
	}
	wantEntry(t, rc[0].(map[string]any), "covered", 2, "")
	if _, m := read("--since", since); len(m["range_coverage"].([]any)) != 3 || m["range_coverage"].([]any)[0].(map[string]any)["state"] != "partial" {
		t.Fatalf("open end %v", m["range_coverage"])
	}
	if _, m := read(); m["range_coverage"] != nil {
		t.Fatalf("unbounded %v", m["range_coverage"])
	}
	setSeam(t, &teamsAccountsOf, func(*store.Store, context.Context) ([]store.WhoamiRow, error) { return nil, errors.New("broken") })
	if code, _ := read("--since", since); code != 1 {
		t.Fatalf("exit %d", code)
	}
	setSeam(t, &readSourceHealth, func(*store.Store, context.Context) ([]store.SourceHealth, error) { return nil, errors.New("broken") })
	if code, _ := read("--since", since); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func TestCoverageText(t *testing.T) {
	e, outlook := covEnv(t)
	code, out, errOut := e.run("--no-sync", "--format", "text", "--outlook-root", outlook, "coverage", "--tz", "UTC", "--day", covDay(1), "--source", "mail:inbox,chats,calendar")
	if code != 0 {
		t.Fatalf("exit %d %s", code, errOut)
	}
	for _, want := range []string{
		"day         source    scope  account       state        items  covered until         reasons",
		covDay(1) + "  mail      inbox  outlook/Main  partial      1      2026-09-09 08:00 UTC  after_newest_mail",
		covDay(1) + "  chats     chats  t1/u1         covered      0      -                     -",
		"fix: " + covDay(1) + " mail inbox outlook/Main: Run `m365crawl sync` again after more mail has arrived.",
		"fix: " + covDay(1) + " calendar: Run `m365crawl sync`.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	code, out, _ = e.run("--no-sync", "--format", "text", "--outlook-root", outlook, "mail", "list", "--folder", "inbox", "--since", mailT0.AddDate(0, 0, -2).Format(time.RFC3339))
	if code != 0 || !strings.Contains(out, "coverage: mail inbox outlook/Main: partial (1 item; covered 2026-09-08 08:00 UTC to 2026-09-09 08:00 UTC) after_last_read") {
		t.Fatalf("mail list text:\n%s", out)
	}
	code, out, _ = e.run("--no-sync", "--format", "log", "--outlook-root", outlook, "coverage", "--day", covDay(1), "--source", "chats")
	if code != 0 || !strings.HasPrefix(out, "coverage={") {
		t.Fatalf("log:\n%s", out)
	}
}

// An archive no sync of this version read judges mail by the last read's ranges, and says so.
func TestCoverageOnALegacyArchiveSaysSo(t *testing.T) {
	e, outlook := covEnv(t)
	e.exec(`delete from meta where key like 'outlook_mail_span:%'`)
	e.exec(`insert into sync_runs(started_at,finished_at,source,status) values('2026-09-10T09:00:00.000Z','2026-09-10T09:00:00.000Z','outlook|Main|mail','ok')`)
	m := covRun(t, e, outlook, "--day", covDay(4), "--source", "mail:inbox")
	wantEntry(t, entryOf(t, m, covDay(4), "mail", "inbox"), "covered", 0, "")
	if n, _ := m["notices"].([]any); len(n) != 1 || !strings.Contains(n[0].(string), "rests on the last read's ranges") {
		t.Fatalf("notices %v", m["notices"])
	}
	code, out, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "mail", "list", "--folder", "inbox", "--since", mailT0.AddDate(0, 0, -5).Format(time.RFC3339))
	if code != 0 || !strings.Contains(out, "rests on the last read's ranges") {
		t.Fatalf("mail list %s", out)
	}
}
