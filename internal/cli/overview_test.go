package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/store"
)

// startHereBlock is the exact "Start here" block the root help and the overview print.
const startHereBlock = `Start here:
  m365crawl sync              read Teams, Outlook mail and the calendar into the archive
  m365crawl                   what the archive holds and how fresh it is
  m365crawl search "words"    find anything across chats and mail
  m365crawl calendar          today's meetings; calendar event <id> for one meeting with its chat and mail
  m365crawl mail unread       unread mail by folder
  m365crawl unread            unread Teams chats
  m365crawl mail list --has-attachments   mail with files; mail show <id> lists each file's name, size and type
`

// pinMailSupported makes the overview treat mail as readable (or not) whatever the host is.
func pinMailSupported(t *testing.T, on bool) {
	t.Helper()
	old := mailSupported
	mailSupported = func() bool { return on }
	t.Cleanup(func() { mailSupported = old })
}

// The root help opens with the Start here block, verbatim, before the usage line, in every format.
func TestRootHelpStartsWithStartHere(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{{"--format", "text", "--no-color", "--help"}, {"--json", "--help"}, {"-h"}} {
		_, out, _ := e.run(args...)
		i, u := strings.Index(out, startHereBlock), strings.Index(out, "Usage: m365crawl")
		if i < 0 || u < 0 || i > u {
			t.Fatalf("%v: Start here block missing or after the usage line:\n%s", args, out)
		}
		if !strings.Contains(strings.Join(strings.Fields(out), " "), appDescription) {
			t.Fatalf("%v: help lacks the description", args)
		}
	}
	// A command's own help does not repeat it.
	if _, out, _ := e.run("calendar", "--help"); strings.Contains(out, "Start here:") {
		t.Fatalf("calendar --help carries the Start here block:\n%s", out)
	}
}

// The overview's next steps are the Start here lines this operating system runs: no mail lines
// where mail is not read.
func TestStartHereFollowsThePlatform(t *testing.T) {
	pinMailSupported(t, true)
	if got := startHereText(stepsHere()); got != startHereBlock {
		t.Fatalf("with mail:\n%s", got)
	}
	pinMailSupported(t, false)
	got := startHereText(stepsHere())
	if strings.Contains(got, "mail unread") || strings.Contains(got, "--has-attachments") || !strings.Contains(got, "m365crawl unread ") {
		t.Fatalf("without mail:\n%s", got)
	}
}

var reTodayStamp = regexp.MustCompile(time.Now().UTC().Format("2006-01-02") + ` \d\d:\d\d`)

// overviewGoldens checks the text (plain and color) and JSON overview of e against name's goldens.
func overviewGoldens(t *testing.T, e *env, name string) {
	t.Helper()
	for _, color := range []bool{false, true} {
		suffix := "plain"
		t.Setenv("CLICOLOR_FORCE", "")
		if color {
			suffix = "color"
			t.Setenv("CLICOLOR_FORCE", "1")
		}
		code, out, errOut := e.run("--format", "text")
		if code != 0 || errOut != "" {
			t.Fatalf("%s: exit %d, stderr %q", name, code, errOut)
		}
		checkGolden(t, name+"."+suffix, reTodayStamp.ReplaceAllString(e.scrub(out), "<stamp>"))
	}
	code, out, errOut := e.run("--json")
	if code != 0 || errOut != "" {
		t.Fatalf("%s json: exit %d, stderr %q", name, code, errOut)
	}
	m := decode(t, out)
	if m["archive_path"] != e.db {
		t.Fatalf("archive_path = %v", m["archive_path"])
	}
	delete(m, "archive_path") // differs per run
	for _, s := range m["sources"].([]any) {
		s := s.(map[string]any)
		if v, ok := s["last_sync_at"].(string); ok && s["source"] != "mail" && strings.HasPrefix(v, time.Now().UTC().Format("2006-01-02")) {
			s["last_sync_at"] = "<stamp>"
		}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		t.Fatal(err)
	}
	checkGolden(t, name+".json", b.String())
}

// With no archive the overview says so, prints where to start and exits 0; it creates nothing.
func TestOverviewWithNoArchive(t *testing.T) {
	pinMailSupported(t, true)
	e := textEnv(t)
	overviewGoldens(t, e, "overview_none")
	if _, err := os.Stat(e.db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the overview created the archive: %v", err)
	}
}

// An archive with Teams chats only: mail and calendar are empty and say how to fill them.
func TestOverviewTeamsOnly(t *testing.T) {
	pinMailSupported(t, true)
	e := textEnv(t)
	teamsArchive(t, e)
	overviewGoldens(t, e, "overview_teams")
}

// fakeCalendar makes the overview see two calendar sources and a covered today, without a sync.
func fakeCalendar(t *testing.T) {
	t.Helper()
	oldS, oldA := calendarSourcesOf, calendarAgendaOf
	calendarSourcesOf = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
		return store.CalendarSources{Rows: []store.CalendarSource{
			{Source: "teams", WindowStart: noteT0.AddDate(0, 0, -14), WindowEnd: noteT0.AddDate(0, 0, 14), EventsLive: 30},
			{Source: "outlook", WindowStart: noteT0.AddDate(0, 0, -30), WindowEnd: noteT0.AddDate(0, 0, 7), EventsLive: 12},
		}}, nil
	}
	calendarAgendaOf = func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
		return store.CalendarAgenda{Accounts: []calendar.AccountCoverage{{SyncedAt: noteT0}, {SyncedAt: noteT0.Add(time.Hour)}}}, nil
	}
	t.Cleanup(func() { calendarSourcesOf, calendarAgendaOf = oldS, oldA })
}

// An archive with chats, mail and a calendar. It is built, not synced, and the overview never
// syncs it, whatever --max-age says.
func TestOverviewFull(t *testing.T) {
	pinMailSupported(t, true)
	e := textEnv(t)
	teamsArchive(t, e)
	seedMail(t, e, seedMessages())
	fakeCalendar(t)
	overviewGoldens(t, e, "overview_full")
	if code, _, errOut := e.run("--json", "--max-age", "1ns"); code != 0 || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if n := syncRunCount(t, e); n != 0 {
		t.Fatalf("the overview synced: %d runs", n)
	}
}

// A day the cached calendar does not cover is flagged.
func TestOverviewFlagsAnUncoveredToday(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	teamsArchive(t, e)
	fakeCalendar(t)
	old := calendarAgendaOf
	calendarAgendaOf = func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
		return store.CalendarAgenda{Gap: true}, nil
	}
	t.Cleanup(func() { calendarAgendaOf = old })
	_, out, _ := e.run("--json")
	cal := decode(t, out)["sources"].([]any)[2].(map[string]any)
	if cal["coverage_gap"] != true || !strings.HasPrefix(cal["note"].(string), "today is not fully covered") || cal["last_sync_at"] != nil {
		t.Fatalf("calendar = %v", cal)
	}
}

// Where mail is not read, the overview says so for mail and leaves the mail steps out.
func TestOverviewWhereMailIsNotRead(t *testing.T) {
	pinMailSupported(t, false)
	e := newEnv(t)
	for _, build := range []func(){func() {}, func() { teamsArchive(t, e) }} {
		build()
		_, out, _ := e.run("--json")
		m := decode(t, out)
		mail := m["sources"].([]any)[1].(map[string]any)
		if mail["source"] != "mail" || mail["state"] != "unsupported" || !strings.Contains(mail["note"].(string), "Windows") {
			t.Fatalf("mail = %v", mail)
		}
		for _, n := range m["next"].([]any) {
			if strings.Contains(n.(map[string]any)["command"].(string), "mail") {
				t.Fatalf("next names a mail command: %v", m["next"])
			}
		}
	}
}

// A word that is no command is an unknown command, not a stray argument of the overview.
func TestUnknownCommandIsNamed(t *testing.T) {
	e := newEnv(t)
	code, _, errOut := e.run("--json", "nope")
	if code != 2 || errorOf(t, errOut)["message"] != `unknown command "nope"` {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

// An archive the overview cannot read, or a query that fails, is an error, not an empty overview.
func TestOverviewReportsArchiveFailures(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	e.garbageArchive()
	if code, _, errOut := e.run("--json"); code == 0 || errorOf(t, errOut)["code"] != "db_error" {
		t.Fatalf("garbage archive: exit %d, stderr %q", code, errOut)
	}

	failing := func(name string, setup func(e *env)) {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			teamsArchive(t, e)
			setup(e)
			if code, _, errOut := e.run("--json"); code == 0 || errorOf(t, errOut)["code"] != "db_error" {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
		})
	}
	failing("chats", func(e *env) { e.exec("drop table messages"); e.exec("create table messages (x)") })
	failing("mail", func(e *env) { e.exec("drop table mail_messages"); e.exec("create table mail_messages (x)") })
	failing("calendar sources", func(e *env) {
		old := calendarSourcesOf
		calendarSourcesOf = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
			return store.CalendarSources{}, errors.New("sources failed")
		}
		t.Cleanup(func() { calendarSourcesOf = old })
	})
	failing("calendar agenda", func(e *env) {
		oldS, oldA := calendarSourcesOf, calendarAgendaOf
		calendarSourcesOf = func(*store.Store, context.Context, store.CalendarSourcesFilter) (store.CalendarSources, error) {
			return store.CalendarSources{Rows: []store.CalendarSource{{WindowStart: noteT0, WindowEnd: noteT0.Add(time.Hour)}}}, nil
		}
		calendarAgendaOf = func(*store.Store, context.Context, store.CalendarFilter) (store.CalendarAgenda, error) {
			return store.CalendarAgenda{}, errors.New("agenda failed")
		}
		t.Cleanup(func() { calendarSourcesOf, calendarAgendaOf = oldS, oldA })
	})
}

// overviewHolds names what each kind of source holds; a source with no counts holds "-".
func TestOverviewHolds(t *testing.T) {
	n, gap := 2, false
	if got := overviewHolds(overviewSource{Events: &n, CoverageGap: &gap}); got != "2 events from - to -; today covered" {
		t.Fatalf("events: %q", got)
	}
	gap = true
	if got := overviewHolds(overviewSource{Events: &n, CoverageGap: &gap}); got != "2 events from - to -; today not fully covered" {
		t.Fatalf("events with a gap: %q", got)
	}
	if got := overviewHolds(overviewSource{Events: &n}); got != "2 events from - to -" {
		t.Fatalf("events without coverage: %q", got)
	}
	if got := overviewHolds(overviewSource{}); got != "-" {
		t.Fatalf("nothing: %q", got)
	}
}

// An archive with no Teams messages says how to fill it.
func TestOverviewEmptyArchive(t *testing.T) {
	pinMailSupported(t, true)
	e := newEnv(t)
	e.emptyArchive()
	_, out, _ := e.run("--json")
	chats := decode(t, out)["sources"].([]any)[0].(map[string]any)
	if chats["state"] != "empty" || chats["messages"] != float64(0) || !strings.HasPrefix(chats["note"].(string), "no Teams messages archived yet") {
		t.Fatalf("chats = %v", chats)
	}
}
