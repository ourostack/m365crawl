package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

// staleMailEnv is the mail archive after a read of a store Outlook wrote at mailT0+1h, then an
// unchanged check two hours later: Outlook has not refreshed its cache since the read.
func staleMailEnv(t *testing.T) *env {
	t.Helper()
	e := mailEnv(t)
	t.Setenv("M365CRAWL_OUTLOOK_ROOT", mailStoreRoot(t))
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	read := mailT0.Add(time.Hour)
	for _, r := range []store.Run{
		{Source: "outlook|Main|mail", Status: "ok", Fingerprint: "fp", StartedAt: read, FinishedAt: read},
		{Source: "outlook|Main|mail", Status: "unchanged", Fingerprint: "fp", StartedAt: read.Add(2 * time.Hour), FinishedAt: read.Add(2 * time.Hour)},
	} {
		if err := st.RecordRun(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func TestStaleOutlookCacheShowsOnMailReadsPastIt(t *testing.T) {
	e := staleMailEnv(t)
	doc := mailJSON(t, e, "mail", "list")
	if doc["cache_fresh_at"] != "2026-09-10T09:00:00Z" {
		t.Fatalf("cache_fresh_at = %v", doc["cache_fresh_at"])
	}
	health, _ := doc["source_health"].([]any)
	if len(health) != 1 {
		t.Fatalf("source_health = %v", health)
	}
	h := health[0].(map[string]any)
	if h["source"] != "outlook_mail" || h["state"] != "stale" || h["last_error_code"] != "outlook_cache_stale" || h["account_id"] != mailAccount ||
		h["doctor_check"] != "mail_readable" || h["cache_fresh_at"] != "2026-09-10T09:00:00Z" || h["last_ok_at"] != "2026-09-10T11:00:00Z" {
		t.Fatalf("stale entry = %v", h)
	}
	want := "Outlook mail for outlook/Main: Outlook has not updated its local cache since 2026-09-10T09:00:00Z (checked 2026-09-10T11:00:00Z, outlook_cache_stale); mail after that is not in it. Open Outlook and let it sync, then run `m365crawl sync`."
	if notices := doc["notices"].([]any); len(notices) != 1 || notices[0] != want {
		t.Fatalf("notices = %v", notices)
	}
	for _, args := range [][]string{
		{"mail", "list", "--since", "2026-09-10T08:00:00Z", "--until", "2026-09-10T10:00:00Z"},
		{"mail", "folders"}, {"search", "plan", "--source", "mail"},
	} {
		if health, _ := mailJSON(t, e, args...)["source_health"].([]any); len(health) != 1 {
			t.Errorf("%v hid the stale cache: %v", args, health)
		}
	}
	// A read that ends before the cache's write time asks for nothing the stale cache can lack.
	for _, args := range [][]string{
		{"mail", "list", "--until", "2026-09-10T09:00:00Z"},
		{"--account", "outlook/Other", "mail", "list"},
		{"messages", "--fields", "id"},
	} {
		if doc := mailJSON(t, e, args...); doc["source_health"] != nil && len(doc["source_health"].([]any)) != 0 || doc["notices"] != nil {
			t.Errorf("%v reported a stale cache it does not read: %v %v", args, doc["source_health"], doc["notices"])
		}
	}
	if code, out, _ := e.mail("--format", "text", "mail", "list"); code != 0 || !strings.Contains(out, "notice: Outlook mail for outlook/Main: Outlook has not updated its local cache") {
		t.Fatalf("text: %d %s", code, out)
	}
	// A failing read is reported as failing, not also as stale.
	e.exec(`insert into sync_runs(started_at, finished_at, source, status, error_code, fingerprint) values('2026-09-10T11:05:00.000Z','2026-09-10T11:05:00.000Z','outlook|Main|mail','failed','outlook_blocks_damaged','')`)
	health, _ = mailJSON(t, e, "mail", "list")["source_health"].([]any)
	if len(health) != 1 || health[0].(map[string]any)["state"] != "failing" {
		t.Fatalf("failing and stale = %v", health)
	}
	// An unchanged check of a copy no successful read qualified does not clear the failure, and
	// the failing source is not also reported stale.
	e.exec(`insert into sync_runs(started_at, finished_at, source, status, error_code, fingerprint) values('2026-09-10T11:10:00.000Z','2026-09-10T11:10:00.000Z','outlook|Main|mail','unchanged','','unqualified')`)
	health, _ = mailJSON(t, e, "mail", "list")["source_health"].([]any)
	if len(health) != 1 || health[0].(map[string]any)["state"] != "failing" {
		t.Fatalf("unqualified unchanged = %v", health)
	}
}

func TestStaleOutlookCacheReadErrorStopsTheRead(t *testing.T) {
	e := staleMailEnv(t)
	old := outlookCacheHealth
	t.Cleanup(func() { outlookCacheHealth = old })
	outlookCacheHealth = func(*store.Store, context.Context) ([]store.SourceHealth, error) {
		return nil, errors.New("disk on fire")
	}
	if code, out, errOut := e.mail("--json", "mail", "list"); code != 1 || out != "" || errorOf(t, errOut)["code"] != "db_error" {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
	outlookCacheHealth = old
	oldFresh := mailCacheFresh
	t.Cleanup(func() { mailCacheFresh = oldFresh })
	mailCacheFresh = func(*store.Store, context.Context, string) (time.Time, error) {
		return time.Time{}, errors.New("disk on fire")
	}
	if code, out, errOut := e.mail("--json", "mail", "folders"); code != 1 || out != "" || errorOf(t, errOut)["code"] != "db_error" {
		t.Fatalf("folders: exit=%d out=%q err=%q", code, out, errOut)
	}
}

func TestMailCacheFreshOnStatusAndOverview(t *testing.T) {
	pinMailSupported(t, true)
	e := staleMailEnv(t)
	_, out, _ := e.run("--json", "--no-sync", "status")
	if m := mailBlockOf(t, decode(t, out)); m["cache_fresh_at"] != "2026-09-10T09:00:00Z" {
		t.Fatalf("status mail = %v", m)
	}
	_, out, _ = e.run("--json")
	for _, s := range decode(t, out)["sources"].([]any) {
		if src := s.(map[string]any); src["source"] == "mail" && src["cache_fresh_at"] != "2026-09-10T09:00:00Z" {
			t.Fatalf("overview mail = %v", src)
		}
	}
}

func TestDoctorWarnsWhenOutlookStoppedWritingItsCache(t *testing.T) {
	forceMailSupported(t)
	rt, st, _ := mailRuntime(t, mailStoreRoot(t), true)
	w, err := store.Open(context.Background(), rt.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	read, fresh := rt.now().Add(-90*time.Second), rt.now().Add(-3*time.Hour)
	if err := w.SetMailRead(context.Background(), "outlook/Main", read); err != nil {
		t.Fatal(err)
	}
	if _, err := w.CommitMail(context.Background(), store.MailBatch{Account: "outlook/Main", ReadAt: read, FreshAt: fresh}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"outlook|Main|mail", "outlook|Main"} {
		if err := w.RecordRun(context.Background(), store.Run{Source: key, Status: "unchanged", Fingerprint: "fp", StartedAt: read, FinishedAt: read}); err != nil {
			t.Fatal(err)
		}
	}
	c := rt.mailReadableCheck(st)
	if !c.OK || !c.Warn || !strings.HasPrefix(c.Detail, "profile Main: mail read 1m ago, but Outlook has not updated its local cache since ") || c.Fix != outlookStaleFix {
		t.Fatalf("mail_readable = %+v", c)
	}
	old := outlookCacheHealth
	t.Cleanup(func() { outlookCacheHealth = old })
	outlookCacheHealth = func(*store.Store, context.Context) ([]store.SourceHealth, error) {
		return nil, errors.New("disk on fire")
	}
	if c := rt.mailReadableCheck(st); !c.Warn || !strings.Contains(c.Detail, "cannot read the mail state: disk on fire") {
		t.Fatalf("mail_readable error = %+v", c)
	}
	if c := rt.outlookStoreCheck(st); !c.Warn || !strings.Contains(c.Detail, "cannot read the Outlook state: disk on fire") {
		t.Fatalf("outlook_store error = %+v", c)
	}
}

func TestOutlookStoreCheckNamesAStaleCalendarCache(t *testing.T) {
	root := mailStoreRoot(t)
	profiles, _, _, err := outlookdesktop.Discover(root)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles %v %v", profiles, err)
	}
	now := time.Date(2026, 10, 29, 21, 0, 0, 0, time.UTC)
	fresh, checked := now.Add(-7*time.Hour), now.Add(-time.Minute)
	row := store.CalendarSource{Outlook: &store.OutlookSource{LastReadAt: checked, Status: "unchanged"}}
	stale := &store.SourceHealth{Source: "outlook_calendar", State: store.SourceStale, CacheFreshAt: &fresh, LastOKAt: &checked}
	d, fix := outlookProfileState(profiles[0], row, stale, now)
	if !strings.HasSuffix(d, "last read 1m ago, unchanged, but Outlook has not updated its local cache since 2026-10-29T14:00:00Z (checked 2026-10-29T20:59:00Z, outlook_cache_stale); an event created or changed after that is not in it") || fix != outlookStaleFix {
		t.Fatalf("detail %q fix %q", d, fix)
	}
	if d, fix := outlookProfileState(profiles[0], row, nil, now); strings.Contains(d, "local cache") || fix != "" {
		t.Fatalf("fresh detail %q fix %q", d, fix)
	}
}

func TestOWADoctorNamesAStaleCache(t *testing.T) {
	root := createdOWARoot(t)
	copyTree(t, filepath.Join("..", "..", "testdata", "outlook-owa-fixture", "mail"), filepath.Join(root, "Default"))
	e := textEnv(t)
	e.emptyArchive()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	rt := &runtime{ctx: context.Background(), outlookOn: true, outlookRoot: root, now: func() time.Time { return now }}
	read, fresh := now.Add(-time.Minute), now.Add(-5*time.Hour)
	if err := st.SetMailRead(rt.ctx, "outlook/Default", read); err != nil {
		t.Fatal(err)
	}
	stamp := func(t time.Time) string { return t.Format("2006-01-02T15:04:05.000Z") }
	// The mail's write time comes from the OWA mail commit itself: the calendar row is from another
	// read, so the calendar fallback cannot supply it.
	if _, err := st.CommitOWAMail(rt.ctx, store.OWAMailBatch{Account: "outlook/Default", ReadAt: read, FreshAt: fresh}); err != nil {
		t.Fatal(err)
	}
	e.exec(`insert into calendar_sources(source, account_id, window_start, window_end, synced_at, cache_fresh_at) values('outlook','outlook/Default','` +
		stamp(now.Add(-24*time.Hour)) + `','` + stamp(now.Add(24*time.Hour)) + `','` + stamp(read.Add(-time.Minute)) + `','` + stamp(fresh) + `')`)
	for _, key := range []string{"outlook|Default|mail", "outlook|Default"} {
		if err := st.RecordRun(rt.ctx, store.Run{Source: key, Status: "unchanged", Fingerprint: "fp", StartedAt: read, FinishedAt: read}); err != nil {
			t.Fatal(err)
		}
	}
	c := rt.outlookStoreCheck(st)
	if !c.Warn || c.Fix != outlookStaleFix || strings.Count(c.Detail, "Outlook has not updated its local cache since 2026-10-09T07:00:00Z") != 2 ||
		!strings.Contains(c.Detail, "mail after that") || !strings.Contains(c.Detail, "an event created or changed after that") {
		t.Fatalf("stale OWA = %+v", c)
	}
	old := outlookCacheHealth
	t.Cleanup(func() { outlookCacheHealth = old })
	outlookCacheHealth = func(*store.Store, context.Context) ([]store.SourceHealth, error) {
		return nil, errors.New("disk on fire")
	}
	if c := rt.outlookStoreCheck(st); !c.Warn || c.Fix != "Run `m365crawl sync`." || strings.Count(c.Detail, "cannot read the Outlook cache state") != 2 {
		t.Fatalf("OWA cache state error = %+v", c)
	}
}
