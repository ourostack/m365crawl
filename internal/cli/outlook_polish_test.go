package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/store"
)

// A root that is not a directory is one coded error with one exit code on every command that takes
// the flag, the implicit sync of a read included.
func TestOutlookRootMissingIsOneCodedError(t *testing.T) {
	e := textEnv(t)
	e.sync()
	missing := filepath.Join(t.TempDir(), "gone")
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ root, want string }{
		{missing, "does not exist"},
		{file, "is not a directory"},
		{filepath.Join(file, "below"), "ENOTDIR-or-missing"}, // Unix says the path cannot be read, Windows that it does not exist
	} {
		for _, cmd := range [][]string{{"sync"}, {"calendar"}, {"calendar", "event", "ev_x"}, {"calendar", "actions"}, {"conversations"}} {
			code, out, errOut := e.run(append([]string{"--max-age", "0", "--outlook-root", c.root}, cmd...)...)
			body := usageBody(t, errOut)
			if code != 3 || out != "" || body["code"] != "outlook_root_missing" || !messageHas(body["message"].(string), c.want) || !strings.Contains(body["message"].(string), "--outlook-root") || body["fix"] == "" {
				t.Fatalf("%v with %s: exit %d out %q err %s", cmd, c.root, code, out, errOut)
			}
		}
	}
	// 'none' is not this error.
	if code, _, errOut := e.run("--outlook-root", "none", "sync"); code != 0 {
		t.Fatalf("none: %d %s", code, errOut)
	}
	// A root that exists and holds no profile keeps its own code.
	empty := t.TempDir()
	if code, _, errOut := e.run("--outlook-root", empty, "sync"); code == 0 || !strings.Contains(errOut, "no_outlook_profiles") {
		t.Fatalf("empty root: %d %s", code, errOut)
	}
}

// TEAMSCRAWL_OUTLOOK=1 with a default directory that is missing is the same error, worded for the default.
func TestOutlookDefaultRootMissingIsTheSameError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "Outlook")
	old := outlookDefaultRoot
	t.Cleanup(func() { outlookDefaultRoot = old })
	outlookDefaultRoot = func() (string, error) { return missing, nil }
	t.Setenv("TEAMSCRAWL_OUTLOOK", "1")
	t.Setenv("TEAMSCRAWL_OUTLOOK_ROOT", "")
	t.Setenv("TEAMSCRAWL_OUTLOOK_ACCOUNT", "")
	db := filepath.Join(t.TempDir(), "x.db")
	for _, cmd := range [][]string{{"sync"}, {"calendar"}} {
		var out, errb strings.Builder
		code := Main(append([]string{"--db", db}, cmd...), &out, &errb)
		body := usageBody(t, errb.String())
		if code != 3 || body["code"] != "outlook_root_missing" || !strings.Contains(body["message"].(string), "default Outlook directory") || !strings.Contains(body["fix"].(string), "TEAMSCRAWL_OUTLOOK") {
			t.Fatalf("%v: exit %d %s", cmd, code, errb.String())
		}
	}
	// A Teams root turns the default off, so a missing default is not looked at.
	e := textEnv(t)
	if code, _, errOut := e.run("sync"); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	// A default root that cannot be worked out is an internal fault, as it is for sync.
	outlookDefaultRoot = func() (string, error) { return "", errors.New("no home") }
	var out, errb strings.Builder
	if code := Main([]string{"--db", db, "calendar"}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "internal") {
		t.Fatalf("exit %d %s", code, errb.String())
	}
}

// With the Outlook source off, a result that holds an Outlook event says so and how old the data is.
func TestOutlookOffNotice(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	const from, to = "2023-11-20", "2023-11-26"
	e.exec(`UPDATE meta SET value=json_set(value,'$.at',strftime('%Y-%m-%dT%H:%M:%fZ','now','-5 days','-1 hour')) WHERE key='outlook_read:outlook/Main'`)
	on := agendaVia(t, run, "--from", from, "--to", to)
	if on["notices"] != nil {
		t.Fatalf("the source is on: %v", on["notices"])
	}
	off := func(args ...string) map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: %d %s", args, code, errOut)
		}
		return decode(t, out)
	}
	agenda := off("calendar", "--limit", "200", "--from", from, "--to", to)
	notices := asStrings(agenda["notices"])
	if len(notices) != 1 || !strings.Contains(notices[0], "Outlook source is off for this run") || !strings.Contains(notices[0], "last read outlook/Main 5 days ago") {
		t.Fatalf("notices %v", notices)
	}
	var outlookOnly map[string]any
	for _, it := range items(t, agenda) {
		if sourcesOf(it) == "outlook" {
			outlookOnly = it
		}
	}
	if outlookOnly == nil {
		t.Fatal("the fixture window has no Outlook-only event")
	}
	ev := off("calendar", "event", outlookOnly["event_id"].(string))
	if got := asStrings(ev["notices"]); len(got) != 1 || !strings.Contains(got[0], "outlook/Main") {
		t.Fatalf("event notices %v", got)
	}
	// Absent: a result with no Outlook event, a run with the source on, and the Teams-only account.
	teams := off("calendar", "--from", from, "--to", to, "--account", teamsAccount)
	if teams["notices"] != nil {
		t.Fatalf("a Teams-only result: %v", teams["notices"])
	}
	if code, out, _ := run("--max-age", "0", "calendar", "event", outlookOnly["event_id"].(string)); code != 0 || decode(t, out)["notices"] != nil {
		t.Fatalf("source on: %d %s", code, out)
	}
	// The text output prints it as one dim line.
	code, out, errOut := e.run("--format", "text", "--max-age", "0", "calendar", "--limit", "200", "--from", from, "--to", to)
	if code != 0 || strings.Count(out, "notice: the Outlook source is off") != 1 {
		t.Fatalf("text: %d %s %s", code, out, errOut)
	}
	// With no read time kept the notice still says the source is off.
	e.exec(`DELETE FROM meta WHERE key LIKE 'outlook_read:%'`)
	bare := off("calendar", "--limit", "200", "--from", from, "--to", to)
	if got := asStrings(bare["notices"]); len(got) != 1 || strings.Contains(got[0], "last read") {
		t.Fatalf("no read time: %v", got)
	}
}

func TestAgeText(t *testing.T) {
	for _, c := range []struct {
		d    string
		want string
	}{{"10s", "just now"}, {"5m", "5 min ago"}, {"5h", "5 h ago"}, {"72h", "3 days ago"}} {
		d, _ := parseMaxAge(c.d)
		if got := ageText(d); got != c.want {
			t.Fatalf("%s: %q", c.d, got)
		}
	}
}

// An action item held on an event Outlook also holds carries the notice too.
func TestOutlookOffNoticeOnActions(t *testing.T) {
	e, run := syncedWithOutlook(t, "Main")
	if code, _, errOut := run("sync", "--outlook-account", teamsAccount); code != 0 {
		t.Fatal(errOut)
	}
	args := []string{"--max-age", "0", "calendar", "actions", "--from", "2023-11-20", "--to", "2023-11-26", "--account", teamsAccount}
	code, out, errOut := e.run(args...)
	m := decode(t, out)
	if code != 0 || m["count"].(float64) == 0 || len(asStrings(m["notices"])) != 1 {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if code, out, _ = run(args...); code != 0 || decode(t, out)["notices"] != nil {
		t.Fatalf("source on: %d %s", code, out)
	}
}

// Each account of an agenda says which of the uncovered days it lacks, once: the account that lacks
// them all says so, and an account that lacks fewer lists its own.
func TestCoverageGapIsAttributedToAnAccount(t *testing.T) {
	_, run := syncedWithOutlook(t, "Main")
	m := agendaVia(t, run, "--from", "2031-03-05", "--days", "7")
	union := asStrings(m["uncovered_days"])
	if m["coverage_gap"] != true || len(union) != 7 {
		t.Fatalf("gap %v days %v", m["coverage_gap"], union)
	}
	var sawAll, sawSome bool
	for _, a := range m["accounts"].([]any) {
		a := a.(map[string]any)
		own := asStrings(a["uncovered_days"])
		switch a["account_id"].(string) {
		case "outlook/Main":
			// The unlinked Outlook profile covers some of the week, so it lacks fewer days than the Teams accounts.
			if a["uncovered_all"] != nil || len(own) == 0 || len(own) >= len(union) {
				t.Fatalf("outlook account: %v", a)
			}
			sawSome = true
		default:
			if a["uncovered_all"] != true || a["uncovered_days"] != nil {
				t.Fatalf("a Teams account that lacks them all: %v", a)
			}
			sawAll = true
		}
	}
	if !sawAll || !sawSome {
		t.Fatalf("accounts %v", m["accounts"])
	}
	// One account in scope: its days are the top-level list, so nothing is repeated.
	one := agendaVia(t, run, "--from", "2031-03-05", "--days", "7", "--account", teamsAccount)
	a := one["accounts"].([]any)[0].(map[string]any)
	if a["uncovered_all"] != nil || a["uncovered_days"] != nil {
		t.Fatalf("repeated: %v", a)
	}
}

func TestSetCoverageCapsAnAccountsOwnDays(t *testing.T) {
	var union, own []string
	for i := range 40 {
		d := "2030-01-" + strconv.Itoa(i+1)
		union = append(union, d)
		if i < 35 {
			own = append(own, d)
		}
	}
	l := &listResult{}
	l.setCoverage(union, []calendar.AccountCoverage{{AccountID: "a", Uncovered: own}, {AccountID: "b", Uncovered: union}}, time.Time{})
	a := l.Accounts[0]
	if len(a.UncoveredDays) != maxUncoveredDays || a.UncoveredDaysTotal != 35 || a.UncoveredAll || !l.Accounts[1].UncoveredAll {
		t.Fatalf("%+v", l.Accounts)
	}
}

// A read time that cannot be loaded leaves the ages out; the notice is still given.
func TestOutlookOffNoticeWithoutReadTimes(t *testing.T) {
	e, _ := syncedWithOutlook(t, "Main")
	old := outlookReadTimes
	t.Cleanup(func() { outlookReadTimes = old })
	outlookReadTimes = func(*store.Store, context.Context) (map[string]time.Time, error) { return nil, errors.New("no meta") }
	code, out, errOut := e.run("--max-age", "0", "calendar", "--limit", "200", "--from", "2023-11-20", "--to", "2023-11-26")
	got := asStrings(decode(t, out)["notices"])
	if code != 0 || len(got) != 1 || strings.Contains(got[0], "last read") {
		t.Fatalf("%d %v %s", code, got, errOut)
	}
}

func messageHas(msg, want string) bool {
	if want == "ENOTDIR-or-missing" {
		return strings.Contains(msg, "cannot be read") || strings.Contains(msg, "does not exist")
	}
	return strings.Contains(msg, want)
}
