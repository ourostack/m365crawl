package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/outlookdesktop"
)

const (
	linkTenant = "00000000-0000-4000-8000-000000000001"
	linkUser   = "00000000-0000-4000-8000-0000000000a1"
	linkTeams  = linkTenant + "/" + linkUser
)

// addSecondProfile puts the second fixture store beside Main in the profiles directory.
func addSecondProfile(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "Second"), 0o750); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(outlookFixture, "profile-two", "HxStore.hxd")) //nolint:gosec // a committed fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Second", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

func activeLinks(t *testing.T, db string) string {
	t.Helper()
	var s string
	if err := openRaw(t, db).QueryRow(`select coalesce(group_concat(account_id||'>'||principal_id||':'||method, ','),'') from (select * from calendar_account_links where unlinked_at is null and source='outlook' order by account_id)`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func usageFix(t *testing.T, err error) (string, string) {
	t.Helper()
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeUsage {
		t.Fatalf("want a usage error, got %v", err)
	}
	return c.Message, c.Fix
}

// The link names a Teams account the run has just synced, applies to the only profile, is a no-op
// when repeated, and ends with "none". The sources are committed whatever the link does.
func TestSyncOutlookLink(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := outlookOpts(db, root)
	o.OutlookLink = linkTeams // a first sync creates the account it names
	run(t, o)
	if got := activeLinks(t, db); got != "outlook/Main>"+linkTeams+":config" {
		t.Fatalf("links %q", got)
	}
	var at1 string
	raw := openRaw(t, db)
	if err := raw.QueryRow(`select linked_at from calendar_account_links`).Scan(&at1); err != nil {
		t.Fatal(err)
	}
	run(t, o)
	var at2 string
	if err := raw.QueryRow(`select linked_at from calendar_account_links`).Scan(&at2); err != nil || at1 != at2 {
		t.Fatalf("a repeated link rewrote the row: %q %q %v", at1, at2, err)
	}
	o.OutlookLink = OutlookLinkNone
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("links after none: %q", got)
	}
	run(t, o) // ending a link that is already gone is not an error
	o.OutlookLink = linkTeams
	run(t, o)
	if got := activeLinks(t, db); got == "" {
		t.Fatal("a link after an unlink did not come back")
	}
}

func TestSyncOutlookLinkUnknownTeamsAccount(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	o := outlookOpts(db, outlookRoot(t, "HxStore.hxd"))
	o.OutlookLink = "00000000-0000-4000-8000-0000000000ff/00000000-0000-4000-8000-0000000000ff"
	rep, _, err := Run(context.Background(), o)
	msg, _ := usageFix(t, err)
	if !strings.Contains(msg, "unknown principal") {
		t.Fatal(msg)
	}
	if rep.Status != StatusOK || sourceKeyed(t, rep, "outlook|Main").Status != StatusOK {
		t.Fatalf("the sources are committed whatever the link does: %s %+v", rep.Status, rep.Sources)
	}
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("a refused link left %q", got)
	}
}

func TestSyncOutlookLinkTwoProfiles(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	addSecondProfile(t, root)
	o := outlookOpts(db, root)
	o.OutlookLink = linkTeams

	_, _, err := Run(context.Background(), o)
	msg, fix := usageFix(t, err)
	if !strings.Contains(msg, "more than one Outlook profile") || !strings.Contains(fix, "--outlook-profile NAME, one of: Main, Second") {
		t.Fatalf("%q %q", msg, fix)
	}
	o.OutlookLinkProfile = "Nope"
	_, _, err = Run(context.Background(), o)
	if msg, fix = usageFix(t, err); !strings.Contains(msg, "no Outlook profile named Nope") || !strings.Contains(fix, "Main, Second") {
		t.Fatalf("%q %q", msg, fix)
	}
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("a refused link left %q", got)
	}

	o.OutlookLinkProfile = "Main"
	run(t, o)
	// The second profile stays its own principal, and cannot take the same Teams account.
	o.OutlookLinkProfile = "Second"
	_, _, err = Run(context.Background(), o)
	if msg, _ = usageFix(t, err); !strings.Contains(msg, "already has the outlook account outlook/Main") {
		t.Fatal(msg)
	}
	if got := activeLinks(t, db); got != "outlook/Main>"+linkTeams+":config" {
		t.Fatalf("links %q", got)
	}

	// "none" with no profile named ends every link.
	o.OutlookLinkProfile, o.OutlookLink = "", OutlookLinkNone
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("links %q", got)
	}
}

func TestSyncOutlookLinkNeedsAProfile(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	o := outlookOpts(db, outlookRoot(t, "HxStore.hxd"))
	run(t, o) // a Teams account exists
	d := outlookDiscover
	t.Cleanup(func() { outlookDiscover = d })
	outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return nil, nil, nil, nil
	}
	o.OutlookLink = linkTeams
	_, _, err := Run(context.Background(), o)
	if msg, fix := usageFix(t, err); !strings.Contains(msg, "no Outlook profile found") || fix != "Add --outlook-profile NAME." {
		t.Fatalf("%q %q", msg, fix)
	}
	outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return nil, nil, nil, errs.Internal(errors.New("disk"))
	}
	if _, _, err = Run(context.Background(), o); err == nil {
		t.Fatal("a failed discovery lost the link failure")
	}
}

func TestSyncOutlookLinkDefaultRootFailure(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	o := outlookOpts(db, outlookRoot(t, "HxStore.hxd"))
	run(t, o)
	old := outlookDefaultRoot
	outlookDefaultRoot = func() (string, error) { return "", errs.Internal(errors.New("no home")) }
	t.Cleanup(func() { outlookDefaultRoot = old })
	o.OutlookRoot, o.OutlookLink = "", linkTeams
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("no error")
	}
}

// A Teams account filter leaves Outlook out of the run, and the link with it.
func TestSyncOutlookLinkNotAppliedWithoutOutlook(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	o := outlookOpts(db, outlookRoot(t, "HxStore.hxd"))
	o.OutlookEnabled = false
	o.OutlookLink = linkTeams
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("links %q", got)
	}
}

// A link whose profile directory is gone can still be ended, by name or without one, and an
// unknown name is still a usage error.
func TestSyncOutlookLinkNoneForAVanishedProfile(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	addSecondProfile(t, root)
	o := outlookOpts(db, root)
	o.OutlookLink, o.OutlookLinkProfile = linkTeams, "Main"
	run(t, o)
	if err := os.RemoveAll(filepath.Join(root, "Main")); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}

	o.OutlookLink, o.OutlookLinkProfile = OutlookLinkNone, "Ghost"
	_, _, err := Run(context.Background(), o)
	if msg, fix := usageFix(t, err); !strings.Contains(msg, "no Outlook profile named Ghost") || !strings.Contains(fix, "Main, Second") {
		t.Fatalf("%q %q", msg, fix)
	}
	if activeLinks(t, db) == "" {
		t.Fatal("a refused unlink ended the link")
	}
	o.OutlookLinkProfile = "Main" // known to the archive, not to the disk
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("the link of a vanished profile stayed: %q", got)
	}

	// Without a name, every link goes, even when no profile directory is left at all.
	o.OutlookLink, o.OutlookLinkProfile = linkTeams, "Second"
	run(t, o)
	o.OutlookRoot = filepath.Join(root, "gone")
	o.OutlookLink, o.OutlookLinkProfile = OutlookLinkNone, ""
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("a missing Outlook root is the source's failure")
	}
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("the link stayed with the root gone: %q", got)
	}
	// Nothing linked and nothing on disk is nothing to end.
	if _, _, err := Run(context.Background(), o); err == nil || strings.Contains(err.Error(), "usage") {
		t.Fatalf("%v", err)
	}
}

func TestSyncOutlookLinkNoneSeesABrokenArchive(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	o := outlookOpts(db, outlookRoot(t, "HxStore.hxd"))
	run(t, o)
	if _, err := openRaw(t, db).Exec(`alter table calendar_account_links rename column unlinked_at to ended_at`); err != nil {
		t.Fatal(err)
	}
	o.OutlookLink = OutlookLinkNone
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("no error")
	}
}

// An archive built when the mapper kept ids in upper case is re-derived by the next mapper without
// a second copy of any event: the old keys are replaced, not left live beside the new ones.
func TestSyncOutlookRederiveReplacesUpperCaseKeys(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := outlookOpts(db, root)
	run(t, o)
	events := count(t, db, `select count(*) from calendar_source_events where source='outlook'`)
	if events == 0 {
		t.Fatal("no Outlook rows")
	}
	raw := openRaw(t, db)
	// What the earlier mapper stored: every id in upper case, and an event Outlook no longer holds.
	for _, q := range []string{
		`update calendar_source_events set event_key=upper(event_key), source_id=upper(source_id), global_id=upper(global_id), ical_uid=upper(ical_uid), series_key=upper(series_key) where source='outlook'`,
		`update calendar_matches set event_key=upper(event_key), source_id=upper(source_id) where source='outlook'`,
		`insert into calendar_source_events(source, account_id, event_key, composite_key, source_id, subject, start_at, end_at, first_seen_at, seen_at) values('outlook','outlook/Main','dropped|','','dropped','Dropped','2020-01-01T00:00:00.000Z','2020-01-01T01:00:00.000Z','2020-01-01T00:00:00.000Z','2020-01-01T00:00:00.000Z')`,
		`update meta set value='old' where key like 'outlook_derivation:%'`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	old := outlookMapperVersion
	outlookMapperVersion = old + 1
	t.Cleanup(func() { outlookMapperVersion = old })
	run(t, o)
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook'`); n != events+1 {
		t.Fatalf("%d Outlook rows, want the store's %d plus the dropped one", n, events)
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and event_key<>lower(event_key)`); n != 0 {
		t.Fatalf("%d upper-case keys are left", n)
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and event_key='dropped|' and removed_at is null`); n != 1 {
		t.Fatal("an event the store no longer holds was dropped")
	}
	if n := count(t, db, `select count(*) from calendar_matches where source='outlook' and event_key<>lower(event_key)`); n != 0 {
		t.Fatalf("%d upper-case match rows are left", n)
	}
}
