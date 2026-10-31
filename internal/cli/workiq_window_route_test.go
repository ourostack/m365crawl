package cli

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// seedWindow records a finished mail window of the route account directly, as a window run leaves
// it: an attempt, its gaps and counts, all-folder coverage when proven, and deferred members.
func seedWindow(t *testing.T, e *env, since, until string, proven bool, statements ...string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db) // brings the archive to the current schema
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	stmts := []string{`insert into workiq_attempts(family,account_id,started_at,finished_at,kind,status,window_start,window_end,counts_json)
values('mail','` + mailAccount + `','2026-10-01T00:00:00Z','2026-10-01T01:00:00Z','mail_window','ok_with_omissions','` + since + `','` + until + `','{"window_items_open":1234}')`}
	if proven {
		stmts = append(stmts, `insert into workiq_window_coverage(attempt_id,account_id,folder_id,since,until,snapshot_at)
values((select max(id) from workiq_attempts),'`+mailAccount+`','','`+since+`','`+until+`','2026-10-01T00:30:00Z')`)
	} else {
		stmts = append(stmts, `insert into workiq_gaps(attempt_id,code,n) values((select max(id) from workiq_attempts),'workiq_window_unproven',1)`)
	}
	for _, q := range append(stmts, statements...) {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
}

func TestOlderMailRouteOffersAWindowOfTheRangeTheCacheLacks(t *testing.T) {
	e := routeEnv(t)
	read := func(args ...string) []map[string]any {
		t.Helper()
		return routesOf(t, mailJSON(t, e, append([]string{"mail", "list"}, args...)...))
	}
	routes := read("--since", "2026-01-01T00:00:00Z")
	if len(routes) != 1 {
		t.Fatalf("want one route, got %v", routes)
	}
	cmds := routeCommands(t, routes[0])
	// The window ends an hour past the latest cache start among the folders the read reaches (Sent Items).
	if !strings.Contains(cmds[2], "workiq sync --source mail --since 2026-01-01T00:00:00Z --until 2026-09-07T09:00:00Z") ||
		!strings.Contains(routeStepDoes(t, routes[0], "workiq sync"), "prove it complete") {
		t.Fatalf("window step = %q", cmds[2])
	}
	// A read that ends earlier asks for its own range and an hour past its end, rounded out to whole
	// seconds: Sent Items count a window only up to a minute before its end.
	routes = read("--since", "2026-01-01T00:00:00.5Z", "--until", "2026-03-01T00:00:00.5Z")
	if cmds := routeCommands(t, routes[0]); !strings.Contains(cmds[2], "--since 2026-01-01T00:00:00Z --until 2026-03-01T01:00:01Z") {
		t.Fatalf("window bounds = %q", cmds[2])
	}

	// A listing-only window leaves the range unproven: the route offers proof with its cost.
	seedWindow(t, e, "2026-01-01T00:00:00Z", "2026-09-07T09:00:00Z", false)
	routes = read("--since", "2026-01-01T00:00:00Z")
	if len(routes) != 1 || !strings.Contains(routes[0]["fix"].(string), "completeness is not proven") {
		t.Fatalf("unproven route = %v", routes)
	}
	if does := routeStepDoes(t, routes[0], "--coverage proof"); !strings.Contains(does, "about 1234 items in about 124 pages") || !strings.Contains(does, "4 minutes") {
		t.Fatalf("proof step = %q", does)
	}

	// Two proven windows whose union holds the range end the route; a gap between them does not.
	seedWindow(t, e, "2026-01-01T00:00:00Z", "2026-05-01T00:00:00Z", true)
	seedWindow(t, e, "2026-05-01T00:00:01Z", "2026-09-07T09:00:00Z", true)
	if routes := read("--since", "2026-01-01T00:00:00Z"); len(routes) != 1 || !strings.Contains(routeCommands(t, routes[0])[2], "--coverage proof") {
		t.Fatalf("a one-second hole between windows was treated as proven: %v", routes)
	}
	seedWindow(t, e, "2026-04-30T00:00:00Z", "2026-05-02T00:00:00Z", true)
	if routes := read("--since", "2026-01-01T00:00:00Z"); routes != nil {
		t.Fatalf("proven windows did not end the route: %v", routes)
	}
	if routes := read("--since", "2025-12-31T23:59:59Z"); len(routes) != 1 {
		t.Fatal("a range starting before every window lost its route")
	}
}

func TestOlderMailRouteForANamedFolderWeighsDeferredTwins(t *testing.T) {
	e := routeEnv(t)
	seedWindow(t, e, "2026-01-01T00:00:00Z", "2026-09-07T09:00:00Z", true,
		`insert into workiq_frontiers(family,account_id,resource,scope,qualification) values('mail','`+mailAccount+`','/me/mailFolders/g-sent/messages/delta','window:x','workiq-v1')`,
		`insert into workiq_folders(account_id,graph_id,folder_key,well_known,inventory_attempt) values('`+mailAccount+`','g-sent',900,'sent',0)`,
		`insert into workiq_window_members(frontier_id,graph_id,account_id,folder_id,received_at,in_window,state,observed_at,match_rowid)
values((select max(id) from workiq_frontiers),'twin','`+mailAccount+`','g-sent','2026-09-06T00:00:00Z',1,'deferred','2026-10-01T00:00:00Z',
(select rowid from mail_messages where folder_key=(select folder_key from mail_folders where kind='inbox') limit 1))`)
	// Every folder read: the deferred copy is listed through its match.
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--since", "2026-01-01T00:00:00Z")); routes != nil {
		t.Fatalf("an unfiltered read kept its route: %v", routes)
	}
	// The Sent Items read may miss a twin whose match is in the inbox: route to whole-account sync.
	routes := routesOf(t, mailJSON(t, e, "mail", "list", "--folder", "sent", "--since", "2026-01-01T00:00:00Z"))
	if len(routes) != 1 || !strings.Contains(routes[0]["fix"].(string), "1 server messages in it match mail cached in other folders") {
		t.Fatalf("twin route = %v", routes)
	}
	if cmds := routeCommands(t, routes[0]); strings.Contains(cmds[len(cmds)-2], "--since") || !strings.Contains(cmds[len(cmds)-2], "workiq sync --source mail") {
		t.Fatalf("a twin route offered the window again: %q", cmds)
	}
	// The inbox read holds the match itself.
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--folder", "inbox", "--since", "2026-01-01T00:00:00Z")); routes != nil {
		t.Fatalf("a read of the match's own folder kept its route: %v", routes)
	}
	// Once a whole-account sync binds the twin's Graph ID, the route ends without another window.
	seedWindow(t, e, "2025-01-01T00:00:00Z", "2025-01-02T00:00:00Z", true,
		`insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail','`+mailAccount+`','graph','twin',1)`)
	if routes := routesOf(t, mailJSON(t, e, "mail", "list", "--folder", "sent", "--since", "2026-01-01T00:00:00Z")); routes != nil {
		t.Fatalf("a resolved twin kept the route: %v", routes)
	}
}

func TestOlderMailWindowRouteFailureFailsTheRead(t *testing.T) {
	e := routeEnv(t)
	old := workIQMailWindowRoute
	t.Cleanup(func() { workIQMailWindowRoute = old })
	workIQMailWindowRoute = func(*store.Store, context.Context, string, time.Time, time.Time, []uint32, []string) (store.WorkIQWindowRoute, error) {
		return store.WorkIQWindowRoute{}, errors.New("broken")
	}
	mailFails(t, e, "db_error", 1, "mail", "list", "--since", "2026-01-01T00:00:00Z")
}

// Spans compare by instant: a window recorded in UTC covers a day bounded in another zone.
func TestSameSpansComparesInstantsAcrossZones(t *testing.T) {
	zone := time.FixedZone("PDT", -7*3600)
	a, b := time.Date(2026, 9, 1, 7, 0, 0, 0, time.UTC), time.Date(2026, 9, 2, 7, 0, 0, 0, time.UTC)
	if !sameSpans([]store.TimeSpan{{From: a, To: b}}, []store.TimeSpan{{From: a.In(zone), To: b.In(zone)}}) {
		t.Fatal("the same instants in two zones were judged different spans")
	}
	if sameSpans([]store.TimeSpan{{From: a, To: b}}, []store.TimeSpan{{From: a, To: b.Add(time.Second)}}) ||
		sameSpans([]store.TimeSpan{{From: a, To: b}}, nil) {
		t.Fatal("different spans were judged the same")
	}
}
