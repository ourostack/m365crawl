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
