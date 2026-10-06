package calendar

import (
	"database/sql"
	"testing"
	"time"
)

// startRecap is a recap with no iCalUID whose meeting started at start; its end is far from any event's.
func startRecap(t testing.TB, call, start string) Recap {
	r := unlinked(t, call)
	r.MeetingStartAt, r.MeetingEndAt = tp(t, start), tp(t, "2026-10-05T23:00:00Z")
	return r
}

func TestLinkRecapsByStartLinksTheOneEventThatStartsNearby(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:03:00Z", "2026-10-05T16:30:00Z"),
		evAt(t, "e2", "uid-b", "2026-10-05T18:00:00Z", "2026-10-05T19:00:00Z"))
	c := batch(t, db, Batch{Window: tw(t), Recaps: []Recap{startRecap(t, "c1", "2026-10-05T16:00:00Z")}}, "2026-10-06T02:00:00Z")
	if uid, m := linkState(t, db, "c1"); uid != "uid-a" || m != LinkStartTime || c.Linked != 0 || c.LinkedByStart != 1 || c.NoStartMatch != 0 || c.AmbiguousStart != 0 {
		t.Fatalf("%s %s %+v", uid, m, c)
	}
	// Never relinked: a later event that starts closer does not move it.
	batch(t, db, Batch{Window: tw(t), Events: []Event{evAt(t, "e3", "uid-c", "2026-10-05T16:00:00Z", "2026-10-05T16:20:00Z")}}, "2026-10-06T03:00:00Z")
	if uid, _ := linkState(t, db, "c1"); uid != "uid-a" {
		t.Fatalf("relinked to %s", uid)
	}
}

func TestLinkRecapsByStartToleranceIsFiveMinutes(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:05:00Z", "2026-10-05T16:30:00Z"))
	c := batch(t, db, Batch{Window: tw(t), Recaps: []Recap{startRecap(t, "in", "2026-10-05T16:00:00Z"), startRecap(t, "out", "2026-10-05T15:59:59Z")}}, "2026-10-06T02:00:00Z")
	if uid, _ := linkState(t, db, "in"); uid != "uid-a" {
		t.Fatalf("exactly five minutes must link: %q", uid)
	}
	if uid, _ := linkState(t, db, "out"); uid != "" || c.LinkedByStart != 1 || c.NoStartMatch != 1 {
		t.Fatalf("a second past five minutes must not: %q %+v", uid, c)
	}
}

func TestLinkRecapsByStartCountsAmbiguousAndNoEvent(t *testing.T) {
	db := openDB(t)
	noUID := evAt(t, "n", "", "2026-10-05T20:00:00Z", "2026-10-05T21:00:00Z")
	a := evAt(t, "a", "uid-x", "2026-10-05T22:00:00Z", "2026-10-05T22:30:00Z")
	o := a
	o.Source, o.SourceID = SourceOutlook, "o"
	ow := tw(t)
	ow.Source = SourceOutlook
	batch(t, db, Batch{Window: tw(t), Events: []Event{
		evAt(t, "e1", "uid-a", "2026-10-05T16:01:00Z", "2026-10-05T16:30:00Z"),
		evAt(t, "e2", "uid-b", "2026-10-05T16:04:00Z", "2026-10-05T16:30:00Z"), noUID, a}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: ow, Events: []Event{o}}, "2026-10-06T01:00:00Z")
	c := batch(t, db, Batch{Window: tw(t), Recaps: []Recap{
		startRecap(t, "two", "2026-10-05T16:02:00Z"),   // uid-a and uid-b both start within five minutes
		startRecap(t, "none", "2026-10-05T12:00:00Z"),  // nothing starts near it
		startRecap(t, "nouid", "2026-10-05T20:01:00Z"), // its only candidate has no uid
		startRecap(t, "twice", "2026-10-05T22:01:00Z"), // one event held by two sources counts once
	}}, "2026-10-06T02:00:00Z")
	if c.LinkedByStart != 1 || c.NoStartMatch != 1 || c.AmbiguousStart != 2 {
		t.Fatalf("%+v", c)
	}
	for call, want := range map[string]string{"two": "", "none": "", "nouid": "", "twice": "uid-x"} {
		if uid, _ := linkState(t, db, call); uid != want {
			t.Errorf("%s linked to %q, want %q", call, uid, want)
		}
	}
	// A recap with no meeting start cannot be matched and is not counted.
	exec(t, db, `UPDATE calendar_recaps SET meeting_start_at=NULL WHERE call_id='none'`)
	if c = batch(t, db, Batch{Window: tw(t)}, "2026-10-06T03:00:00Z"); c.NoStartMatch != 0 || c.AmbiguousStart != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestLinkRecapsByStartErrors(t *testing.T) {
	setup := func() *sql.DB {
		db := openDB(t)
		seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z"))
		batch(t, db, Batch{Window: tw(t), Recaps: []Recap{startRecap(t, "c1", "2026-10-05T16:00:00Z")}}, "2026-10-06T00:30:00Z")
		exec(t, db, `UPDATE calendar_recaps SET ical_uid='', link_method=''`)
		return db
	}
	run := func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		_, err = LinkRecapsByStart(ctx, tx, acct, time.Now())
		return err
	}
	if err := run(setup()); err != nil {
		t.Fatal(err)
	}
	db := setup()
	exec(t, db, `CREATE TRIGGER boom BEFORE UPDATE ON calendar_recaps BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if err := run(db); err == nil {
		t.Fatal("update: want error")
	}
	db = setup()
	exec(t, db, `DROP TABLE calendar_account_links`)
	if err := run(db); err == nil {
		t.Fatal("principals: want error")
	}
	db = setup()
	exec(t, db, `ALTER TABLE calendar_source_events RENAME COLUMN removed_at TO gone`)
	if err := run(db); err == nil {
		t.Fatal("events query: want error")
	}
	db = setup()
	exec(t, db, `DROP TABLE calendar_recaps`)
	if err := run(db); err == nil {
		t.Fatal("recaps query: want error")
	}
	origScan, origErr := scanRow, rowsErr
	t.Cleanup(func() { scanRow, rowsErr = origScan, origErr })
	db = setup()
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	if err := run(db); err == nil {
		t.Fatal("scan: want error")
	}
	scanRow = origScan
	rowsErr = func(*sql.Rows) error { return errBoom }
	if err := run(db); err == nil {
		t.Fatal("rows: want error")
	}
}
