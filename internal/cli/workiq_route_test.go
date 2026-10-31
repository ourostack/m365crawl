package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// routeEnv is the seeded mailbox of outlook/Main, whose cached Inbox starts on 2026-09-05, Sent
// Items on 2026-09-07 and Projects on 2026-08-01, with a WorkIQ executable that must never start during a read.
func routeEnv(t *testing.T) *env {
	t.Helper()
	e := mailEnv(t)
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	marker := filepath.Join(dir, "started")
	bin := filepath.Join(dir, "workiq")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700); err != nil { //nolint:gosec // G306: private synthetic test executable.
		t.Fatal(err)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", bin)
	t.Cleanup(func() {
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("an ordinary read started WorkIQ")
		}
	})
	return e
}

func routesOf(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	raw, ok := doc["workiq_routes"]
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, r := range raw.([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

// routeFolders is a mail route's folders as "name kind start" entries, in order.
func routeFolders(route map[string]any) string {
	raw, _ := route["folders"].([]any)
	var out []string
	for _, f := range raw {
		m := f.(map[string]any)
		out = append(out, m["folder"].(string)+" "+m["kind"].(string)+" "+m["cache_covers_since"].(string))
	}
	return strings.Join(out, "; ")
}

func routeCommands(t *testing.T, route map[string]any) []string {
	t.Helper()
	var out []string
	for _, step := range route["next"].([]any) {
		s := step.(map[string]any)
		if s["does"] == "" {
			t.Fatal("a route step does not say what it does")
		}
		out = append(out, s["command"].(string))
	}
	return out
}

func setWorkIQ(t *testing.T, e *env, on bool) {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkIQEnabled(context.Background(), on); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOlderMailReadRoutesTheAgentToWorkIQ(t *testing.T) {
	e := routeEnv(t)
	doc := mailJSON(t, e, "mail", "list", "--since", "2026-01-01")
	if len(doc["items"].([]any)) != 6 {
		t.Fatal("the route replaced the cached result")
	}
	routes := routesOf(t, doc)
	if len(routes) != 1 {
		t.Fatalf("want one route, got %v", doc["workiq_routes"])
	}
	r := routes[0]
	if r["source"] != "mail" || r["account"] != mailAccount || r["reason"] != "range_before_cache" ||
		r["workiq"] != "disabled" || r["cache_covers_since"] != nil ||
		routeFolders(r) != "Inbox inbox 2026-09-05T08:00:00Z; Sent Items sent 2026-09-07T08:00:00Z; Projects other 2026-08-01T08:00:00Z" {
		t.Fatalf("route = %v", r)
	}
	if fix, _ := r["fix"].(string); !strings.Contains(fix, "Projects from 2026-08-01") || !strings.Contains(fix, "WorkIQ") {
		t.Fatalf("fix = %q", fix)
	}
	cmds := routeCommands(t, r)
	if len(cmds) != 4 {
		t.Fatalf("commands = %q", cmds)
	}
	for i, want := range []string{"--account outlook/Main workiq status --source mail", "workiq enable",
		"--account outlook/Main workiq sync --source mail", "mail list --since 2026-01-01"} {
		if !strings.HasPrefix(cmds[i], "m365crawl ") || !strings.Contains(cmds[i], want) {
			t.Fatalf("step %d = %q, want %q", i, cmds[i], want)
		}
	}
	if !strings.Contains(cmds[0], "--db") || strings.Contains(cmds[1], "--account") {
		t.Fatalf("commands lost the archive or scoped enable: %q", cmds)
	}
	for _, args := range [][]string{
		{"mail", "list"},
		{"mail", "list", "--since", "2026-09-08"},
		{"mail", "list", "--since", "2026-09-07T08:00:00Z"},
		{"mail", "list", "--folder", "projects", "--since", "2026-09-01"},
		{"--account", "outlook/Other", "mail", "list", "--since", "2026-01-01"},
	} {
		if routes := routesOf(t, mailJSON(t, e, args...)); routes != nil {
			t.Fatalf("%v: a covered range or unknown account got a route: %v", args, routes)
		}
	}
	if routes := routesOf(t, mailJSON(t, e, "--account", mailAccount, "mail", "list", "--since", "2026-01-01")); len(routes) != 1 || routes[0]["account"] != mailAccount {
		t.Fatalf("an explicit account lost its route: %v", routes)
	}
	// Each folder is judged by its own range: the older Projects folder does not hide that the
	// Inbox and Sent Items start after this read's start.
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-09-01")); len(routes) != 1 ||
		routeFolders(routes[0]) != "Inbox inbox 2026-09-05T08:00:00Z; Sent Items sent 2026-09-07T08:00:00Z" {
		t.Fatalf("an older folder hid a newer folder's start: %v", routes)
	}
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--folder", "inbox", "--since", "2026-09-01")); len(routes) != 1 ||
		routeFolders(routes[0]) != "Inbox inbox 2026-09-05T08:00:00Z" {
		t.Fatalf("a folder's own range was not used: %v", routes)
	}
}

func TestOlderMailRouteFollowsWorkIQState(t *testing.T) {
	e := routeEnv(t)
	setWorkIQ(t, e, true)
	r := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01"))[0]
	cmds := routeCommands(t, r)
	if r["workiq"] != "enabled" || len(cmds) != 3 || strings.Contains(strings.Join(cmds, "\n"), "workiq enable") {
		t.Fatalf("enabled route = %v", r)
	}
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent"))
	r = routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01"))[0]
	fix, _ := r["fix"].(string)
	if r["workiq"] != "not_installed" || !strings.Contains(fix, "install WorkIQ") || !strings.Contains(fix, "sign in") ||
		!strings.Contains(fix, "never") || len(routeCommands(t, r)) != 3 {
		t.Fatalf("not-installed route = %v", r)
	}
}

func TestOlderMailRouteEndsAfterACompleteWorkIQSync(t *testing.T) {
	e := routeEnv(t)
	old := workIQMailComplete
	t.Cleanup(func() { workIQMailComplete = old })
	var asked []string
	workIQMailComplete = func(_ *store.Store, _ context.Context, account string) (bool, error) {
		asked = append(asked, account)
		return true, nil
	}
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01")); routes != nil {
		t.Fatalf("an account already synced through WorkIQ was routed again: %v", routes)
	}
	// The route and the read's range_coverage both ask; neither asks about another account.
	if len(asked) == 0 || slices.ContainsFunc(asked, func(a string) bool { return a != mailAccount }) {
		t.Fatalf("asked = %v", asked)
	}
	workIQMailComplete = func(*store.Store, context.Context, string) (bool, error) { return false, errors.New("broken") }
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01")
	workIQMailComplete = old
	e.exec(`insert into meta(key,value) values('workiq_enabled','x')`)
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01")
}

func TestOlderMailThreadAndSearchRouteToWorkIQ(t *testing.T) {
	e := routeEnv(t)
	if routes := routesOf(t, mailJSON(t, e, "mail", "thread", "outlook/Main:101")); routes != nil {
		t.Fatalf("a thread whose replies all have their parents got a route: %v", routes)
	}
	doc := mailJSON(t, e, "search", "--source", "mail", "--since", "2026-01-01")
	routes := routesOf(t, doc)
	if len(routes) != 1 || routes[0]["source"] != "mail" || routes[0]["reason"] != "range_before_cache" ||
		!strings.Contains(routeCommands(t, routes[0])[3], "search --source mail --since 2026-01-01") {
		t.Fatalf("search route = %v", routes)
	}
	if routes := routesOf(t, mailJSON(t, e, "search", "--source", "mail", "--folder", "sent", "--since", "2026-09-01")); len(routes) != 1 ||
		routeFolders(routes[0]) != "Sent Items sent 2026-09-07T08:00:00Z" {
		t.Fatalf("search did not judge only the folder it read: %v", routes)
	}
	for _, args := range [][]string{
		{"search", "--source", "mail", "--since", "2026-09-08"},
		{"search", "--source", "mail", "--folder", "Projects", "--since", "2026-09-01"},
		{"search", "plan", "--source", "mail"},
		{"--account", "outlook/Other", "search", "--source", "mail", "--since", "2026-01-01"},
	} {
		if routes := routesOf(t, mailJSON(t, e, args...)); routes != nil {
			t.Fatalf("%v: unexpected route %v", args, routes)
		}
	}
	old := mailCoverageOf
	t.Cleanup(func() { mailCoverageOf = old })
	mailCoverageOf = func(*store.Store, context.Context) ([]store.CoverageRow, error) { return nil, errors.New("broken") }
	mailFails(t, e, "db_error", 1, "search", "--source", "mail", "--since", "2026-01-01")

	e2 := textEnv(t)
	seedMail(t, e2, seedMessages()[1:]) // the plan that the first reply answers is not archived
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent"))
	routes = routesOf(t, mailJSON(t, e2, "mail", "thread", "outlook/Main:102"))
	if len(routes) != 1 || routes[0]["reason"] != "parent_not_archived" || routes[0]["account"] != mailAccount || routes[0]["folders"] != nil ||
		!strings.Contains(routeCommands(t, routes[0])[3], "mail thread outlook/Main:102") {
		t.Fatalf("thread route = %v", routes)
	}
}

func TestOlderChatReadRoutesOnlyToCoverage(t *testing.T) {
	e := newEnv(t)
	e.sync()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "absent"))
	for _, args := range [][]string{
		{"messages", "--since", "2000-01-01"},
		{"search", "--source", "chats", "--since", "2000-01-01"},
	} {
		code, out, stderr := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatal(args, code, stderr)
		}
		routes := routesOf(t, decode(t, out))
		if len(routes) != 1 {
			t.Fatalf("%v: routes = %v", args, routes)
		}
		r := routes[0]
		cmds := routeCommands(t, r)
		joined := strings.Join(cmds, "\n")
		if r["source"] != "chats" || r["reason"] != "range_before_cache" || r["workiq"] != "not_installed" ||
			!strings.Contains(joined, "workiq coverage --source chats --since 2000-01-01T00:00:00") ||
			strings.Contains(joined, "workiq sync") || strings.Contains(joined, " messages") {
			t.Fatalf("%v: chat route = %v", args, r)
		}
		if fix, _ := r["fix"].(string); !strings.Contains(fix, "counts") {
			t.Fatalf("chat route hides that chats stay counts-only: %q", fix)
		}
	}
	for _, args := range [][]string{{"messages"}, {"search", "--source", "chats", "--since", "1d"}} {
		code, out, stderr := e.run(append([]string{"--json", "--max-age", "0"}, args...)...)
		if code != 0 {
			t.Fatal(args, code, stderr)
		}
		if routes := routesOf(t, decode(t, out)); routes != nil {
			t.Fatalf("%v: covered chat range got a route: %v", args, routes)
		}
	}
	// The archive's first Teams read was just now, so the last day is before it: messages'
	// range_coverage routes each account to the counts-only steps, although archived messages
	// reach further back.
	code, out, stderr := e.run("--json", "--max-age", "0", "messages", "--since", "1d")
	if code != 0 {
		t.Fatal(code, stderr)
	}
	if routes := routesOf(t, decode(t, out)); len(routes) != 2 || routes[0]["reason"] != "range_not_covered" || routes[0]["account"] == nil ||
		!strings.Contains(strings.Join(routeCommands(t, routes[0]), "\n"), "workiq coverage --source chats") {
		t.Fatalf("range not covered routes = %v", routes)
	}
	code, out, _ = e.run("--json", "--max-age", "0", "--account", tenantA+"/"+userA, "messages", "--since", "2000-01-01")
	routes := routesOf(t, decode(t, out))
	if code != 0 || len(routes) != 1 || routes[0]["account"] != tenantA+"/"+userA ||
		!strings.Contains(strings.Join(routeCommands(t, routes[0]), "\n"), "--account "+tenantA+"/"+userA+" workiq coverage") {
		t.Fatalf("explicit Teams account route = %v", routes)
	}
}

func TestOlderMailRouteText(t *testing.T) {
	e := routeEnv(t)
	code, out, stderr := e.mail("--format", "text", "mail", "list", "--since", "2026-01-01")
	if code != 0 {
		t.Fatal(code, stderr)
	}
	if !strings.Contains(out, "route: ") || !strings.Contains(out, "next: m365crawl ") ||
		!strings.Contains(out, "workiq sync --source mail") {
		t.Fatalf("text output lost the route:\n%s", out)
	}
}

func TestWorkIQRouteSkipsFoldersWithoutANativeRange(t *testing.T) {
	rt := newRuntime(context.Background(), &Globals{}, nil, nil)
	routes, err := rt.sinceRoutes(nil, []store.CoverageRow{{Account: mailAccount}}, time.Now())
	if err != nil || routes != nil {
		t.Fatal("a server-only folder without a native range produced a route", routes, err)
	}
}

func TestWorkIQRouteFailuresFailTheRead(t *testing.T) {
	e := routeEnv(t)
	old := workIQMailComplete
	t.Cleanup(func() { workIQMailComplete = old })
	workIQMailComplete = func(*store.Store, context.Context, string) (bool, error) { return false, errors.New("broken") }
	e2 := textEnv(t)
	seedMail(t, e2, seedMessages()[1:])
	for _, tc := range []struct {
		e    *env
		args []string
	}{
		{e, []string{"search", "--source", "mail", "--since", "2026-01-01"}},
		{e2, []string{"mail", "thread", "outlook/Main:102"}},
	} {
		mailFails(t, tc.e, "db_error", 1, tc.args...)
	}
	workIQMailComplete = old
	chats := newEnv(t)
	chats.sync()
	t.Setenv("HOME", t.TempDir())
	chats.exec(`insert into meta(key,value) values('workiq_enabled','x')`)
	for _, args := range [][]string{{"messages", "--since", "2000-01-01"}, {"search", "--source", "chats", "--since", "2000-01-01"}} {
		code, _, stderr := chats.run(append([]string{"--json", "--max-age", "0"}, args...)...)
		if code != 1 || errorOf(t, stderr)["code"] != "db_error" {
			t.Fatal(args, "an unreadable WorkIQ setting did not fail the chat read", code, stderr)
		}
	}
}

// An account WorkIQ cannot bind, such as a further Windows mailbox without a recorded identity,
// gets no route: its sync would only refuse.
func TestOlderMailRouteSkipsAnAccountWorkIQCannotBind(t *testing.T) {
	e := routeEnv(t)
	old := workIQMailBindable
	t.Cleanup(func() { workIQMailBindable = old })
	workIQMailBindable = func(*store.Store, context.Context, string) (bool, error) { return false, nil }
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01")); routes != nil {
		t.Fatalf("an unbindable account got a route: %v", routes)
	}
	workIQMailBindable = func(*store.Store, context.Context, string) (bool, error) { return false, errors.New("broken") }
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01")
}
