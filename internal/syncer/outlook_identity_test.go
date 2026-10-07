package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

const ownAddress = "Fixture.Owner@Example.com"

// putOutlookAccount adds an account object that carries address to the profile's store, after the
// fixture's blocks.
func putOutlookAccount(t *testing.T, root string, addresses ...string) {
	t.Helper()
	path := filepath.Join(root, "Main", "HxStore.hxd")
	b, err := os.ReadFile(path) //nolint:gosec // a test temp dir
	if err != nil {
		t.Fatal(err)
	}
	var accounts []*hxbuild.Object
	for _, a := range addresses {
		accounts = append(accounts, hxbuild.NewAccount(hxbuild.AccountSpec{Address: a}))
	}
	b = append(b, hxbuild.EncodeBlock(hxbuild.BlockTypeData, hxbuild.FramedPayload(hxbuild.Head(15), accounts...))...)
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

// putTeamsProfile writes the record the fixture's Teams account would keep of itself.
func putTeamsProfile(t *testing.T, db, address string) {
	t.Helper()
	if _, err := openRaw(t, db).Exec(`insert into records(source, tenant_id, user_id, database, store, key_json, value_json, content_hash, first_seen_at, updated_at)
	  values('fixture', ?, ?, 'Teams:profiles:react-web-client:'||?||':'||?||':en-us', 'profiles', '"8:orgid:'||?||'"', json_object('userPrincipalName', ?), 'h', '2031-01-01T00:00:00.000Z', '2031-01-01T00:00:00.000Z')`,
		linkTenant, linkUser, linkTenant, linkUser, linkUser, address); err != nil {
		t.Fatal(err)
	}
}

func identity(t *testing.T, db string) string {
	t.Helper()
	return queryStr(t, db, `select coalesce((select value from meta where key='outlook_identity:outlook/Main'), 'unread')`)
}

// The profile's own address is kept from the read; with a Teams account that has the same address
// the next sync links them by address; the link survives a sync that reads nothing.
func TestSyncOutlookLinksByAddress(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	putOutlookAccount(t, root, ownAddress, "another.account@example.net")
	o := outlookOpts(db, root)
	run(t, o)
	if got := identity(t, db); got != "another.account@example.net\nfixture.owner@example.com" {
		t.Fatalf("identity %q", got)
	}
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("linked without a Teams profile: %q", got)
	}
	putTeamsProfile(t, db, "FIXTURE.OWNER@example.com")
	if r, _ := run(t, o); sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("%+v", r.Sources)
	}
	if got := activeLinks(t, db); got != "outlook/Main>"+linkTeams+":address" {
		t.Fatalf("links %q", got)
	}
}

func TestSyncOutlookNoMatchNoLink(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	putOutlookAccount(t, root, "someone.else@example.com")
	o := outlookOpts(db, root)
	run(t, o)
	putTeamsProfile(t, db, ownAddress)
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("an address that differs linked: %q", got)
	}
}

// An explicit link or none wins over the address and stays.
func TestSyncOutlookExplicitWinsOverAddress(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	putOutlookAccount(t, root, ownAddress)
	o := outlookOpts(db, root)
	o.OutlookLink = OutlookLinkNone
	run(t, o) // the first sync creates the Teams account, and none is recorded
	putTeamsProfile(t, db, ownAddress)
	o.OutlookLink = ""
	run(t, o)
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("none was replaced: %q", got)
	}
	o.OutlookLink = linkTeams
	run(t, o)
	if got := activeLinks(t, db); got != "outlook/Main>"+linkTeams+":config" {
		t.Fatalf("links %q", got)
	}
	o.OutlookLink = ""
	run(t, o)
	if got := activeLinks(t, db); got != "outlook/Main>"+linkTeams+":config" {
		t.Fatalf("an explicit link was replaced: %q", got)
	}
}

// An archive written before the address was read has no address row: the first sync reads the
// unchanged store again.
func TestSyncOutlookRereadsForTheAddress(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	putOutlookAccount(t, root, ownAddress)
	o := outlookOpts(db, root)
	run(t, o)
	if _, err := openRaw(t, db).Exec(`delete from meta where key like 'outlook_identity:%'`); err != nil {
		t.Fatal(err)
	}
	if r, _ := run(t, o); sourceKeyed(t, r, "outlook|Main").Status != StatusOK || identity(t, db) == "unread" {
		t.Fatalf("%+v %s", r.Sources, identity(t, db))
	}
	if r, _ := run(t, o); sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("%+v", r.Sources)
	}
}

func implicitOpts(db, root string) Options {
	o := outlookOpts(db, root)
	o.OutlookImplicit = true
	return o
}

// On by default, Outlook is read when it is there and says nothing when it is not.
func TestSyncOutlookImplicit(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	r, _ := run(t, implicitOpts(db, root))
	if sourceKeyed(t, r, "outlook|Main").Status != StatusOK || r.Status != StatusOK {
		t.Fatalf("%s %+v", r.Status, r.Sources)
	}
	for name, dir := range map[string]string{"no directory": filepath.Join(root, "missing"), "no profile": t.TempDir()} {
		r, _, err := Run(context.Background(), implicitOpts(newDB(t), dir))
		if err != nil || r.Status != StatusOK || len(r.Sources) == 0 {
			t.Fatalf("%s: %v %s", name, err, r.Status)
		}
		for _, s := range r.Sources {
			if s.Source == "outlook" {
				t.Fatalf("%s: says %+v", name, s)
			}
		}
	}
	// Not asked for by name, and the same directory fails when it is.
	if _, _, err := Run(context.Background(), outlookOpts(newDB(t), t.TempDir())); err == nil {
		t.Fatal("a directory with no profile must fail an Outlook source that was asked for")
	}
}

// Whatever goes wrong with a default Outlook is reported as unavailable and never fails the sync.
func TestSyncOutlookImplicitNeverFails(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := outlookRoot(t, "HxStore.hxd")
	noAccess := errs.NoFullDiskAccess(root, os.ErrPermission)
	old := outlookDiscover
	t.Cleanup(func() { outlookDiscover = old })

	outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return nil, nil, nil, noAccess
	}
	r, _, err := Run(context.Background(), implicitOpts(newDB(t), root))
	if s := sourceKeyed(t, r, "outlook"); err != nil || r.Status != StatusOK || s.Status != StatusUnavailable || s.Error.Code != errs.CodeNoFullDiskAccess || s.Error.Fix == "" {
		t.Fatalf("%v %s %+v", err, r.Status, s)
	}

	outlookDiscover = func(root string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		p, c, _, _ := old(root)
		return p, c, []outlookdesktop.SkippedProfile{{Name: "Locked", Dir: filepath.Join(root, "Locked"), Reason: "no_full_disk_access", Err: os.ErrPermission}}, nil
	}
	db := newDB(t)
	r, _, err = Run(context.Background(), implicitOpts(db, root))
	if err != nil || r.Status != StatusOK || sourceKeyed(t, r, "outlook|Locked").Status != StatusUnavailable || sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
		t.Fatalf("%v %s %+v", err, r.Status, r.Sources)
	}

	outlookDiscover = old
	broken := filepath.Join(root, "Main", "HxStore.hxd")
	if err := os.WriteFile(broken, []byte("Nostromo-and"), 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
	db = newDB(t)
	r, _, err = Run(context.Background(), implicitOpts(db, root))
	if err != nil || r.Status != StatusOK || sourceKeyed(t, r, "outlook|Main").Status != StatusUnavailable {
		t.Fatalf("%v %s %+v", err, r.Status, r.Sources)
	}
	// Asked for by name it is a failure.
	if _, _, err := Run(context.Background(), outlookOpts(newDB(t), root)); err == nil {
		t.Fatal("a store that cannot be read must fail an Outlook source that was asked for")
	}
}

// With no Teams, a default Outlook runs alone only if there is an Outlook to run.
func TestSyncOutlookImplicitWithoutTeams(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := implicitOpts(newDB(t), root)
	o.Root = t.TempDir()
	if r, _ := run(t, o); len(r.Sources) != 2 || r.Sources[0].Status != StatusOK || r.Sources[1].Status != StatusOK {
		t.Fatalf("%+v", r.Sources)
	}
	o = implicitOpts(newDB(t), t.TempDir())
	o.Root = t.TempDir()
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("no Teams and no Outlook is the Teams error")
	}
	old := outlookDefaultRoot
	outlookDefaultRoot = func() (string, error) { return "", errors.New("no home") }
	t.Cleanup(func() { outlookDefaultRoot = old })
	o = implicitOpts(newDB(t), "")
	o.Root = t.TempDir()
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("no Teams and no default directory is the Teams error")
	}
	// And a run that has Teams, with no way to name the default directory, says nothing.
	o = implicitOpts(newDB(t), "")
	if r, _, err := Run(context.Background(), o); err != nil || r.Status != StatusOK {
		t.Fatalf("%v %s", err, r.Status)
	}
}

// The automatic link cannot fail a sync.
func TestSyncOutlookAutoLinkFailureIsNotAFailure(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	putOutlookAccount(t, root, ownAddress)
	o := outlookOpts(db, root)
	run(t, o)
	putTeamsProfile(t, db, ownAddress)
	if _, err := openRaw(t, db).Exec(`create trigger no_links before insert on calendar_account_links begin select raise(abort, 'no'); end`); err != nil {
		t.Fatal(err)
	}
	if r, _ := run(t, o); r.Status != StatusUnchanged {
		t.Fatalf("%s", r.Status)
	}
	if got := activeLinks(t, db); got != "" {
		t.Fatalf("linked through a trigger that refuses: %q", got)
	}
}
