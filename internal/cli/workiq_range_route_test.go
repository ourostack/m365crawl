package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// stepCommands are the commands of a coverage entry's next steps.
func stepCommands(t *testing.T, e map[string]any) []string {
	t.Helper()
	steps, _ := e["next"].([]any)
	var out []string
	for _, s := range steps {
		out = append(out, s.(map[string]any)["command"].(string))
	}
	return out
}

func hasCommand(cmds []string, part string) bool {
	for _, c := range cmds {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

func endsWholeAccount(cmds []string) bool {
	for _, c := range cmds {
		if strings.HasSuffix(c, "workiq sync --source mail") {
			return true
		}
	}
	return false
}

// A range the archive does not cover routes to a WorkIQ mail window of that range, as a read
// before the cache does, not to a whole-account sync a large mailbox cannot fit (#313).
func TestRangeNotCoveredRoutesToAWindowOfTheRange(t *testing.T) {
	e, outlook := covEnv(t)
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	list := func(args ...string) map[string]any {
		t.Helper()
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook, "mail", "list"}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return decode(t, out)
	}
	// A hole inside the archived Sent range: the window is the read's own range and an hour past it.
	since, until := mailT0.AddDate(0, 0, -11), mailT0.AddDate(0, 0, -10).Add(-time.Hour)
	window := "workiq sync --source mail --since " + since.Format(time.RFC3339) + " --until " + until.Add(time.Hour).Format(time.RFC3339)
	m := list("--folder", "sent", "--since", since.Format(time.RFC3339), "--until", until.Format(time.RFC3339))
	routes := routesOf(t, m)
	if len(routes) != 1 || routes[0]["reason"] != "range_not_covered" {
		t.Fatalf("routes %v", routes)
	}
	if cmds := routeCommands(t, routes[0]); !hasCommand(cmds, window) || endsWholeAccount(cmds) {
		t.Fatalf("route steps %v, want %q", cmds, window)
	}
	rc := rangeCoverage(t, m)
	if cmds := stepCommands(t, rc[0]); !hasCommand(cmds, window) || !strings.Contains(rc[0]["fix"].(string), "this range") {
		t.Fatalf("entry %v", rc[0])
	}

	// coverage --day names each day's own window.
	day := mailT0.AddDate(0, 0, -100).Truncate(24 * time.Hour)
	c := covRun(t, e, outlook, "--day", covDay(100), "--source", "mail")
	for _, role := range []string{"sent", "inbox"} {
		x := entryOf(t, c, covDay(100), "mail", role)
		want := "--since " + day.Format(time.RFC3339) + " --until " + day.Add(25*time.Hour).Format(time.RFC3339)
		if cmds := stepCommands(t, x); !hasCommand(cmds, want) || endsWholeAccount(cmds) || !strings.Contains(x["fix"].(string), "this range") {
			t.Fatalf("%s: next %v, want %q; fix %q", role, cmds, want, x["fix"])
		}
	}
	c = covRun(t, e, outlook, "--since", covDay(101), "--until", covDay(99), "--source", "mail:sent")
	first, second := stepCommands(t, entryOf(t, c, covDay(101), "mail", "sent")), stepCommands(t, entryOf(t, c, covDay(100), "mail", "sent"))
	if !hasCommand(first, "--since "+day.AddDate(0, 0, -1).Format(time.RFC3339)) || !hasCommand(second, "--since "+day.Format(time.RFC3339)) {
		t.Fatalf("days share a window: %v %v", first, second)
	}

	// A range with no start cannot be a window: it keeps the whole-account sync.
	m = list("--folder", "sent", "--until", until.Format(time.RFC3339))
	if routes := routesOf(t, m); len(routes) != 1 || routes[0]["reason"] != "range_not_covered" || !endsWholeAccount(routeCommands(t, routes[0])) {
		t.Fatalf("open start routes %v", routes)
	}

	// Once a proven window holds the range, the read is covered and has no route.
	seedWindow(t, e, since.Format(time.RFC3339), until.Add(time.Hour).Format(time.RFC3339), true)
	m = list("--folder", "sent", "--since", since.Format(time.RFC3339), "--until", until.Format(time.RFC3339))
	if routes := routesOf(t, m); routes != nil || rangeCoverage(t, m)[0]["state"] != "covered" {
		t.Fatalf("proven window: routes %v, coverage %v", routes, rangeCoverage(t, m))
	}

	// A route that cannot be looked up fails the read.
	setSeam(t, &workIQMailComplete, func(*store.Store, context.Context, string) (bool, error) { return false, errors.New("broken") })
	if code, _, _ := e.run("--no-sync", "--json", "--outlook-root", outlook, "coverage", "--tz", "UTC", "--day", covDay(100), "--source", "mail"); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

// windowOf is the --since/--until of the window step among cmds, or "".
func windowOf(cmds []string) string {
	for _, c := range cmds {
		if i := strings.Index(c, "workiq sync --source mail --since "); i >= 0 {
			return c[i:]
		}
	}
	return ""
}

// A read that starts before the cache prints one window per account, and its range_coverage
// entries name that same window, ending where the cache starts, not at the read's end (#315).
func TestReadBeforeCacheAndItsRangeCoverageNameOneWindow(t *testing.T) {
	e, outlook := covEnv(t)
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	since := mailT0.AddDate(0, 0, -30)
	for _, args := range [][]string{
		{"mail", "list", "--since", since.Format(time.RFC3339)},
		{"mail", "list", "--since", since.In(time.FixedZone("", -7*3600)).Format(time.RFC3339), "--until", mailT0.AddDate(0, 0, -2).Format(time.RFC3339)},
	} {
		code, out, errOut := e.run(append([]string{"--no-sync", "--json", "--outlook-root", outlook}, args...)...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		m := decode(t, out)
		routes := routesOf(t, m)
		if len(routes) != 1 {
			t.Fatalf("%v: routes %v", args, routes)
		}
		window := windowOf(routeCommands(t, routes[0]))
		// The cache's latest start (Sent Items, 10–11 days ago) bounds the window, plus the hour.
		low := "workiq sync --source mail --since " + since.Format(time.RFC3339) + " --until " + mailT0.AddDate(0, 0, -11).Format("2006-01-02")
		high := "workiq sync --source mail --since " + since.Format(time.RFC3339) + " --until " + mailT0.AddDate(0, 0, -10).Format("2006-01-02")
		if !strings.HasPrefix(window, low) && !strings.HasPrefix(window, high) {
			t.Fatalf("%v: route window %q", args, window)
		}
		for _, x := range rangeCoverage(t, m) {
			if cmds := stepCommands(t, x); len(cmds) > 0 && windowOf(cmds) != window {
				t.Fatalf("%v: %s entry names %q, route %q", args, x["scope"], windowOf(cmds), window)
			}
		}
	}
	// One coverage day: Sent and Inbox name one window, the one a read of that day would print.
	day := mailT0.AddDate(0, 0, -20).Truncate(24 * time.Hour)
	c := covRun(t, e, outlook, "--day", covDay(20), "--source", "mail")
	sent, inbox := windowOf(stepCommands(t, entryOf(t, c, covDay(20), "mail", "sent"))), windowOf(stepCommands(t, entryOf(t, c, covDay(20), "mail", "inbox")))
	want := "workiq sync --source mail --since " + day.Format(time.RFC3339) + " --until " + day.Add(25*time.Hour).Format(time.RFC3339)
	if !strings.HasPrefix(sent, want) || !strings.HasPrefix(inbox, want) {
		t.Fatalf("day 20: sent %q, inbox %q, want %q", sent, inbox, want)
	}
}

// coverage --day names the window a read of that day prints, which reaches every folder the read
// spans: a later-starting folder coverage never judges (Drafts) widens both alike (#315).
func TestCoverageDayNamesTheWindowAReadOfTheDayPrints(t *testing.T) {
	fixClock(t, covNow)
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	commitCovMail(t, st, mailT0.Add(time.Hour), append(covMailMessages(), mailMsg(8, fDrafts, "<d2@x.test>", "", "d2", 2, false))...)
	_ = st.Close()
	outlook := t.TempDir()
	setSeam(t, &workIQMailBindable, func(*store.Store, context.Context, string) (bool, error) { return true, nil })
	day := mailT0.AddDate(0, 0, -10).Truncate(24 * time.Hour)
	c := covRun(t, e, outlook, "--day", covDay(10), "--source", "mail")
	sent := windowOf(stepCommands(t, entryOf(t, c, covDay(10), "mail", "sent")))
	code, out, errOut := e.run("--no-sync", "--json", "--outlook-root", outlook, "mail", "list",
		"--since", day.Format(time.RFC3339), "--until", day.AddDate(0, 0, 1).Format(time.RFC3339))
	if code != 0 {
		t.Fatalf("exit %d %s", code, errOut)
	}
	routes := routesOf(t, decode(t, out))
	if len(routes) != 1 {
		t.Fatalf("routes %v", routes)
	}
	if read := windowOf(routeCommands(t, routes[0])); sent == "" || sent != read {
		t.Fatalf("coverage --day names %q, a read of the day %q", sent, read)
	}
}
