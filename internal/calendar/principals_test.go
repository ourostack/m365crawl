package calendar

import (
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// snap applies one source's snapshot for one account at the given time (all of November 2026).
func snap(t testing.TB, db *sql.DB, src Source, account, at, cacheFresh string, events ...Event) BatchCounts {
	t.Helper()
	w := Window{
		Source: src, AccountID: account, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2026-12-01T00:00:00Z"),
		SyncedAt: mustTime(t, at), CacheFreshAt: mustTime(t, cacheFresh),
	}
	counts, err := ApplySnapshot(ctx, db, w, events, mustTime(t, at))
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// linked is a database with a Teams account, an Outlook account linked to it, and no events.
func linked(t testing.TB) *sql.DB {
	t.Helper()
	db := openDB(t)
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	link(t, db, SourceOutlook, acctOutlook, acctTeams)
	return db
}

func link(t testing.TB, db *sql.DB, src Source, account, principal string) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := LinkAccount(ctx, tx, src, account, principal, "config", mustTime(t, "2026-11-02T08:30:00Z")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func linkErr(t testing.TB, db *sql.DB, src Source, account, principal string) error {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	return LinkAccount(ctx, tx, src, account, principal, "config", mustTime(t, "2026-11-02T09:00:00Z"))
}

func wantUsage(t testing.TB, err error, what string) {
	t.Helper()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeUsage || coded.Exit != errs.ExitUsage || coded.Fix == "" {
		t.Fatalf("%s: want a usage-class error with a fix, got %v", what, err)
	}
}

func TestLinkAccountRules(t *testing.T) {
	db := openDB(t)
	// Nothing synced: sync Teams first.
	err := linkErr(t, db, SourceOutlook, acctOutlook, acctTeams)
	wantUsage(t, err, "no teams account")
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Fix != "Sync Teams first, then link the account." {
		t.Fatalf("fix %v", err)
	}
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	snap(t, db, SourceTeams, "tenant-2/user-2", "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	snap(t, db, SourceOutlook, "outlook-2", "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")

	// Only a non-Teams source is linked.
	wantUsage(t, linkErr(t, db, SourceTeams, "tenant-2/user-2", acctTeams), "teams account")
	wantUsage(t, linkErr(t, db, "", acctOutlook, acctTeams), "no source")
	wantUsage(t, linkErr(t, db, SourceOutlook, "", acctTeams), "no account")
	// Only to a Teams account that exists: not a stranger, and not another linked account (no chains).
	wantUsage(t, linkErr(t, db, SourceOutlook, acctOutlook, "stranger"), "unknown principal")
	wantUsage(t, linkErr(t, db, SourceOutlook, acctOutlook, "outlook-2"), "outlook account as principal")

	link(t, db, SourceOutlook, acctOutlook, acctTeams)
	// One per source per principal.
	wantUsage(t, linkErr(t, db, SourceOutlook, "outlook-2", acctTeams), "second outlook account")
	// The same account again is an update, and another principal for the second account is fine.
	link(t, db, SourceOutlook, acctOutlook, acctTeams)
	link(t, db, SourceOutlook, "outlook-2", "tenant-2/user-2")
	// Moving an account to another principal needs the target to be free of that source.
	wantUsage(t, linkErr(t, db, SourceOutlook, acctOutlook, "tenant-2/user-2"), "target already has an outlook account")

	// Unlinking keeps the row, frees the slot, and linking again clears it.
	tx, _ := db.BeginTx(ctx, nil)
	if err := UnlinkAccount(ctx, tx, SourceOutlook, acctOutlook, mustTime(t, "2026-11-03T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	wantUsage(t, UnlinkAccount(ctx, tx, SourceOutlook, acctOutlook, mustTime(t, "2026-11-03T00:00:00Z")), "unlink twice")
	wantUsage(t, UnlinkAccount(ctx, tx, SourceOutlook, "never-linked", mustTime(t, "2026-11-03T00:00:00Z")), "unlink a stranger")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_account_links`); n != 2 {
		t.Fatalf("an unlink must keep the row: %d links", n)
	}
	var unlinkedAt sql.NullString
	if err := db.QueryRow(`SELECT unlinked_at FROM calendar_account_links WHERE account_id=?`, acctOutlook).Scan(&unlinkedAt); err != nil || unlinkedAt.String != "2026-11-03T00:00:00.000Z" {
		t.Fatalf("unlinked_at %v %v", unlinkedAt, err)
	}
	p, err := LoadPrincipals(ctx, db)
	if err != nil || p.Of(acctOutlook) != acctOutlook {
		t.Fatalf("an unlinked account is its own principal: %v %v", p.Of(acctOutlook), err)
	}
	link(t, db, SourceOutlook, "outlook-3", acctTeams) // the freed slot
	// A second unlinked-then-relinked account still obeys the unique index.
	wantUsage(t, linkErr(t, db, SourceOutlook, acctOutlook, acctTeams), "relink to a taken principal")
	if n := count(t, db, `SELECT count(*) FROM calendar_account_links WHERE unlinked_at IS NULL`); n != 2 {
		t.Fatalf("%d active links", n)
	}
}

func TestLinkAccountReportsStorageErrors(t *testing.T) {
	db := linked(t)
	exec(t, db, `DROP TABLE calendar_account_links`)
	if err := linkErr(t, db, SourceOutlook, "x", acctTeams); err == nil {
		t.Fatal("want a storage error")
	}
	db = linked(t)
	exec(t, db, `DROP TABLE calendar_sources`)
	if err := linkErr(t, db, SourceOutlook, "x", acctTeams); err == nil {
		t.Fatal("want a storage error")
	}
	db = linked(t)
	tx, _ := db.BeginTx(ctx, nil)
	exec2(t, tx, `DROP TABLE calendar_account_links`)
	if err := LinkAccount(ctx, tx, SourceOutlook, "x", acctTeams, "config", time.Now()); err == nil {
		t.Fatal("want a storage error from the one-per-source check")
	}
	if err := UnlinkAccount(ctx, tx, SourceOutlook, "x", time.Now()); err == nil {
		t.Fatal("want a storage error from the unlink")
	}
	_ = tx.Rollback()
	// The insert itself failing: the table exists for the checks but refuses the write.
	db = linked(t)
	exec(t, db, `CREATE TRIGGER no_links BEFORE INSERT ON calendar_account_links BEGIN SELECT RAISE(ABORT, 'no'); END`)
	if err := linkErr(t, db, SourceOutlook, "outlook-9", acctTeams); err == nil {
		t.Fatal("want a storage error from the insert")
	}
}

func exec2(t testing.TB, tx *sql.Tx, q string) {
	t.Helper()
	if _, err := tx.ExecContext(ctx, q); err != nil {
		t.Fatal(err)
	}
}

func TestPrincipalsOf(t *testing.T) {
	var zero Principals
	if zero.Of("a") != "a" || !reflect.DeepEqual(zero.Accounts("a"), []string{"a"}) || zero.Resolve("") != nil || !reflect.DeepEqual(zero.Resolve("a"), []string{"a"}) {
		t.Fatal("the zero value links nothing")
	}
	db := linked(t)
	snap(t, db, SourceOutlook, "outlook-0", "2026-11-02T08:00:00Z", "2026-11-02T08:00:00Z")
	tx, _ := db.BeginTx(ctx, nil)
	// A second source's account for the same principal, to see the sort.
	if _, err := tx.ExecContext(ctx, `INSERT INTO calendar_account_links (source, account_id, principal_id, method, linked_at) VALUES ('other','a-first',?,'config','2026-11-02T00:00:00.000Z')`, acctTeams); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPrincipals(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if p.Of(acctOutlook) != acctTeams || p.Of(acctTeams) != acctTeams || p.Of("outlook-0") != "outlook-0" {
		t.Fatalf("Of: %q %q %q", p.Of(acctOutlook), p.Of(acctTeams), p.Of("outlook-0"))
	}
	want := []string{acctTeams, "a-first", acctOutlook}
	if got := p.Accounts(acctTeams); !reflect.DeepEqual(got, want) {
		t.Fatalf("Accounts: %v", got)
	}
	// Resolve is the same for the principal and for any of its accounts, and nil for all.
	if !reflect.DeepEqual(p.Resolve(acctOutlook), want) || !reflect.DeepEqual(p.Resolve(acctTeams), want) || p.Resolve("") != nil {
		t.Fatalf("Resolve: %v", p.Resolve(acctOutlook))
	}
	if !reflect.DeepEqual(p.Resolve("outlook-0"), []string{"outlook-0"}) {
		t.Fatalf("an unlinked account resolves to itself: %v", p.Resolve("outlook-0"))
	}
	// A teams row in the link table (which LinkAccount never writes) is ignored: a Teams account is
	// always its own principal.
	exec(t, db, `INSERT INTO calendar_account_links (source, account_id, principal_id, method, linked_at) VALUES ('teams','tenant-9/user-9','tenant-1/user-1','config','2026-11-02T00:00:00.000Z')`)
	p, _ = LoadPrincipals(ctx, db)
	if p.Of("tenant-9/user-9") != "tenant-9/user-9" {
		t.Fatal("a teams account is its own principal")
	}
	// Reads and scans report errors.
	if _, err := LoadPrincipals(ctx, openClosed(t)); err == nil {
		t.Fatal("want an error from a closed database")
	}
	failScan(t, func() {
		if _, err := LoadPrincipals(ctx, db); err == nil {
			t.Fatal("want a scan error")
		}
	})
}

func openClosed(t testing.TB) *sql.DB {
	db := openDB(t)
	_ = db.Close()
	return db
}

func failScan(t testing.TB, f func()) {
	orig := scanRow
	scanRow = func(*sql.Rows, ...any) error { return errors.New("scan failed") }
	defer func() { scanRow = orig }()
	f()
}

func TestSchemaHasAccountLinkTable(t *testing.T) {
	db := openDB(t)
	for _, idx := range []string{"calendar_account_links_principal", "calendar_account_links_one_per_source"} {
		if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx); n != 1 {
			t.Errorf("index %s missing", idx)
		}
	}
	var unique int
	if err := db.QueryRow(`SELECT "unique" FROM pragma_index_list('calendar_account_links') WHERE name='calendar_account_links_one_per_source'`).Scan(&unique); err != nil || unique != 1 {
		t.Fatalf("the one-per-source index must be unique: %d %v", unique, err)
	}
}
