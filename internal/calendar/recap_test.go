package calendar

import (
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func recapOf(t testing.TB, call string) Recap {
	return Recap{
		AccountID: acct, CallID: call, ICalUID: "uid-1", LinkMethod: LinkICalUID, HasCatchUp: true,
		Headline: "Fixture headline", ShortSummary: "Short fixture summary", Outline: "1. Intro",
		SpeakersJSON: `[{"name":"Alex Fixture"}]`, TopicsJSON: `["fixtures"]`,
		RecordingURL: "https://media.example.test/rec", RecordingStartAt: tp(t, "2026-10-05T16:00:00Z"), RecordingEndAt: tp(t, "2026-10-05T17:00:00Z"),
		DurationSeconds: 3600, ExpiresAt: tp(t, "2026-11-05T17:00:00Z"),
		MeetingStartAt: tp(t, "2026-10-05T16:00:00Z"), MeetingEndAt: tp(t, "2026-10-05T17:00:00Z"), AttendeesCount: 4, OrganizerID: "org-1",
	}
}

func item(call, kind, origin string, ordinal int, title string) RecapItem {
	return RecapItem{AccountID: acct, CallID: call, Kind: kind, Origin: origin, Ordinal: ordinal, Title: title, Text: title + " text", OwnerName: "Blake Fixture"}
}

func TestApplyRecapsThinnerKeepsSummary(t *testing.T) {
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{recapOf(t, "c1")}}, "2026-10-06T01:00:00Z")
	thinner := Recap{AccountID: acct, CallID: "c1", ICalUID: "uid-1", HasCatchUp: true, IsMissed: false}
	c := batch(t, db, Batch{Window: tw(t), Recaps: []Recap{thinner}}, "2026-10-06T02:00:00Z")
	if c.Recaps.Unchanged != 1 {
		t.Fatalf("%+v", c)
	}
	var headline, rec string
	var dur, att int
	if err := db.QueryRow(`SELECT headline, recording_url, duration_seconds, attendees_count FROM calendar_recaps`).Scan(&headline, &rec, &dur, &att); err != nil ||
		headline != "Fixture headline" || rec == "" || dur != 3600 || att != 4 {
		t.Fatalf("%v %s %s %d %d", err, headline, rec, dur, att)
	}
	// A non-empty field replaces only itself.
	upd := Recap{AccountID: acct, CallID: "c1", Headline: "New headline", IsMissed: true}
	c = batch(t, db, Batch{Window: tw(t), Recaps: []Recap{upd}}, "2026-10-06T03:00:00Z")
	var short, speakers string
	var missed bool
	if err := db.QueryRow(`SELECT headline, short_summary, speakers_json, recording_url, is_missed, attendees_count FROM calendar_recaps`).Scan(&headline, &short, &speakers, &rec, &missed, &att); err != nil {
		t.Fatal(err)
	}
	if c.Recaps.Changed != 1 || headline != "New headline" || short == "" || speakers == "" || rec == "" || !missed || att != 4 {
		t.Fatalf("%+v %s %q %s %s %v %d", c, headline, short, speakers, rec, missed, att)
	}
}

func TestApplyRecapsStoresDoNotEraseEachOther(t *testing.T) {
	db := openDB(t)
	catchUp := Recap{AccountID: acct, CallID: "c1", ICalUID: "uid-1", LinkMethod: LinkICalUID, HasCatchUp: true,
		Headline: "From catch-up", SpeakersJSON: `["a"]`, RecordingURL: "https://media.example.test/r"}
	recap := Recap{AccountID: acct, CallID: "c1", RecapID: "recap-1", HasRecap: true, ShortSummary: "From recap", MeetingStartAt: tp(t, t1), MeetingEndAt: tp(t, t2)}
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{catchUp}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{recap}}, "2026-10-06T02:00:00Z")
	var uid, id, short, speakers, url string
	var hc, hr bool
	if err := db.QueryRow(`SELECT ical_uid, recap_id, short_summary, speakers_json, recording_url, has_catchup, has_recap FROM calendar_recaps`).
		Scan(&uid, &id, &short, &speakers, &url, &hc, &hr); err != nil {
		t.Fatal(err)
	}
	if uid != "uid-1" || id != "recap-1" || short != "From recap" || speakers == "" || url == "" || !hc || !hr {
		t.Fatalf("%s %s %s %s %s %v %v", uid, id, short, speakers, url, hc, hr)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_recaps`); n != 1 {
		t.Fatalf("one row per call: %d", n)
	}
}

func TestApplyRecapItemsSupersedeNotDelete(t *testing.T) {
	db := openDB(t)
	a, b := item("c1", ItemActionItem, OriginCatchUp, 0, "Send notes"), item("c1", ItemActionItem, OriginCatchUp, 1, "Book room")
	m := item("c1", ItemMention, OriginCatchUp, 0, "Mention one")
	other := item("c1", ItemActionItem, OriginRecap, 0, "Recap-origin item")
	c := batch(t, db, Batch{Window: tw(t), RecapItems: []RecapItem{a, b, m, other}}, "2026-10-06T01:00:00Z")
	if c.RecapItems.New != 4 {
		t.Fatalf("%+v", c)
	}
	// The next read of the catch-up action items holds only b: a is superseded, not deleted; the
	// mention set and the recap-origin set are untouched.
	c = batch(t, db, Batch{Window: tw(t), RecapItems: []RecapItem{b}}, "2026-10-06T02:00:00Z")
	if c.RecapItems.Changed != 1 || c.RecapItems.Unchanged != 1 {
		t.Fatalf("%+v", c)
	}
	live := func() int {
		return count(t, db, `SELECT count(*) FROM calendar_recap_items WHERE superseded_at IS NULL`)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_recap_items`); n != 4 {
		t.Fatalf("rows deleted: %d", n)
	}
	if live() != 3 || count(t, db, `SELECT count(*) FROM calendar_recap_items WHERE title='Send notes' AND superseded_at='2026-10-06T02:00:00.000Z'`) != 1 {
		t.Fatalf("live = %d", live())
	}
	// An empty incoming set changes nothing.
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{recapOf(t, "c1")}}, "2026-10-06T03:00:00Z")
	if live() != 3 {
		t.Fatal("an empty set superseded items")
	}
	// The item comes back: it is live again with its original first_seen_at, and a changed field updates.
	a.OwnerName = "Casey Fixture"
	c = batch(t, db, Batch{Window: tw(t), RecapItems: []RecapItem{a, b}}, "2026-10-06T04:00:00Z")
	var first, owner string
	_ = db.QueryRow(`SELECT first_seen_at, owner_name FROM calendar_recap_items WHERE title='Send notes'`).Scan(&first, &owner)
	if c.RecapItems.Changed != 1 || live() != 4 || first != "2026-10-06T01:00:00.000Z" || owner != "Casey Fixture" {
		t.Fatalf("%+v live=%d %s %s", c, live(), first, owner)
	}
	// ItemKey is derived when empty, stable, and ignores case and spacing.
	if k := ItemKey("action_item", "catchup", 0, "Send  Notes", "SEND NOTES TEXT"); k != ItemKey("action_item", "catchup", 0, "send notes", "send notes text") || len(k) != 16 {
		t.Fatalf("key %q", k)
	}
	if count(t, db, `SELECT count(*) FROM calendar_recap_items WHERE item_key=?`, ItemKey("action_item", "catchup", 1, "Book room", "Book room text")) != 1 {
		t.Fatal("derived key not stored")
	}
}

func TestEmptyDataArrayCreatesNoRecap(t *testing.T) {
	// The core stores only what it is given; an empty batch of recaps creates no recap row, no
	// placeholder and no item.
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{}, RecapItems: []RecapItem{}}, "2026-10-06T01:00:00Z")
	if count(t, db, `SELECT count(*) FROM calendar_recaps`)+count(t, db, `SELECT count(*) FROM calendar_recap_items`) != 0 {
		t.Fatal("rows created")
	}
}

func TestRecapExpiredRecordGoneKeepsRow(t *testing.T) {
	// Recap rows are never deleted or hidden because a source stopped reporting them.
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{recapOf(t, "c1")}, RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "m")}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: tw(t), GoneSourceIDs: []string{"c1"}}, "2026-10-06T02:00:00Z")
	if count(t, db, `SELECT count(*) FROM calendar_recaps`) != 1 || count(t, db, `SELECT count(*) FROM calendar_recap_items WHERE superseded_at IS NULL`) != 1 {
		t.Fatal("recap rows changed")
	}
}

func seedLinkable(t testing.TB, db *sql.DB, events ...Event) {
	t.Helper()
	batch(t, db, Batch{Window: tw(t), Events: events}, "2026-10-06T01:00:00Z")
}

func evAt(t testing.TB, id, uid, start, end string) Event {
	e := thin(t, t1)
	e.SourceID, e.GlobalID, e.ICalUID = id, uid, uid
	e.Start, e.End = mustTime(t, start), mustTime(t, end)
	return e
}

func linkState(t testing.TB, db *sql.DB, call string) (uid, method string) {
	t.Helper()
	if err := db.QueryRow(`SELECT ical_uid, link_method FROM calendar_recaps WHERE call_id=?`, call).Scan(&uid, &method); err != nil {
		t.Fatal(err)
	}
	return
}

func unlinked(t testing.TB, call string) Recap {
	r := recapOf(t, call)
	r.ICalUID, r.LinkMethod = "", ""
	return r
}

func TestLinkRecapsByICalUID(t *testing.T) {
	// A recap that arrives with an iCalUID joins events directly and is never relinked by time.
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-time", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z"))
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{recapOf(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, m := linkState(t, db, "c1"); uid != "uid-1" || m != LinkICalUID {
		t.Fatalf("%s %s", uid, m)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_recaps r JOIN calendar_source_events e ON e.ical_uid=r.ical_uid`); n != 0 {
		t.Fatalf("join = %d (uid-1 has no event here)", n)
	}
}

func TestLinkRecapsByTimeUnique(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:00:20Z", "2026-10-05T17:00:30Z"),
		evAt(t, "e2", "uid-b", "2026-10-05T18:00:00Z", "2026-10-05T19:00:00Z"))
	c := batch(t, db, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, m := linkState(t, db, "c1"); uid != "uid-a" || m != LinkTime || c.Linked != 1 {
		t.Fatalf("%s %s %+v", uid, m, c)
	}
	// Never relinked: a later event that matches better does not move it.
	batch(t, db, Batch{Window: tw(t), Events: []Event{evAt(t, "e3", "uid-c", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")}}, "2026-10-06T03:00:00Z")
	if uid, _ := linkState(t, db, "c1"); uid != "uid-a" {
		t.Fatal("relinked")
	}
}

func TestLinkRecapsAmbiguousStaysUnlinked(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:00:10Z", "2026-10-05T17:00:00Z"),
		evAt(t, "e2", "uid-b", "2026-10-05T16:00:00Z", "2026-10-05T17:00:10Z"))
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1"), unlinked(t, "none")}}, "2026-10-06T02:00:00Z")
	if uid, m := linkState(t, db, "c1"); uid != "" || m != "" {
		t.Fatalf("two candidates must not link: %s %s", uid, m)
	}
	// Outside the tolerance, a gone event, a master, an all-day event and an event without a uid never match.
	db2 := openDB(t)
	gone := evAt(t, "g", "uid-g", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")
	master := evAt(t, "m", "uid-m", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")
	master.EventType = EventMaster
	allDay := evAt(t, "d", "uid-d", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")
	allDay.AllDay, allDay.StartDate = TriTrue, "2026-10-05"
	far := evAt(t, "f", "uid-f", "2026-10-05T16:05:00Z", "2026-10-05T17:00:00Z")
	seedLinkable(t, db2, gone, master, allDay, far)
	batch(t, db2, Batch{Window: tw(t), GoneSourceIDs: []string{"g"}}, "2026-10-06T01:30:00Z")
	batch(t, db2, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, _ := linkState(t, db2, "c1"); uid != "" {
		t.Fatalf("linked to %s", uid)
	}
	// A single candidate without a uid cannot link either; the recap links when the event appears.
	db3 := openDB(t)
	noUID := evAt(t, "n", "", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")
	seedLinkable(t, db3, noUID)
	batch(t, db3, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, _ := linkState(t, db3, "c1"); uid != "" {
		t.Fatal("linked to an event with no uid")
	}
	// The same event held by two sources counts once.
	db4 := openDB(t)
	a := evAt(t, "a", "uid-x", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z")
	o := a
	o.Source, o.SourceID = SourceOutlook, "o"
	ow := tw(t)
	ow.Source = SourceOutlook
	batch(t, db4, Batch{Window: tw(t), Events: []Event{a}}, "2026-10-06T01:00:00Z")
	batch(t, db4, Batch{Window: ow, Events: []Event{o}}, "2026-10-06T01:00:00Z")
	batch(t, db4, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, _ := linkState(t, db4, "c1"); uid != "uid-x" {
		t.Fatalf("one event in two sources must link: %q", uid)
	}
}

func TestLinkRecapsSurvivesRekey(t *testing.T) {
	// The link is the shared ical_uid, so changing the event's key (here: re-adding it under a
	// different source id and key) does not break it.
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z"))
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	exec(t, db, `UPDATE calendar_source_events SET event_key='some-other-key'`)
	if n := count(t, db, `SELECT count(*) FROM calendar_recaps r JOIN calendar_source_events e ON e.ical_uid=r.ical_uid AND e.account_id=r.account_id`); n != 1 {
		t.Fatalf("join = %d", n)
	}
}

func TestLinkRecapsErrors(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z"))
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T00:30:00Z")
	// recapOf links by time in the same batch above when the event exists; reset it for the error cases.
	exec(t, db, `UPDATE calendar_recaps SET ical_uid='', link_method=''`)
	run := func(db *sql.DB) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		_, err = LinkRecaps(ctx, tx, acct, time.Minute, time.Now())
		return err
	}
	if err := run(db); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE calendar_recaps SET ical_uid='', link_method=''`)
	exec(t, db, `CREATE TRIGGER boom BEFORE UPDATE ON calendar_recaps BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if err := run(db); err == nil {
		t.Fatal("update: want error")
	}
	exec(t, db, `DROP TRIGGER boom`)
	exec(t, db, `ALTER TABLE calendar_source_events RENAME COLUMN removed_at TO gone`)
	if err := run(db); err == nil {
		t.Fatal("events query: want error")
	}
	exec(t, db, `DROP TABLE calendar_recaps`)
	if err := run(db); err == nil {
		t.Fatal("recaps query: want error")
	}
	// Scan and rows errors.
	db2 := openDB(t)
	batch(t, db2, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T00:30:00Z")
	unlink := func() { exec(t, db2, `UPDATE calendar_recaps SET ical_uid='', link_method=''`) }
	origScan, origErr := scanRow, rowsErr
	t.Cleanup(func() { scanRow, rowsErr = origScan, origErr })
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	if err := run(db2); err == nil {
		t.Fatal("scan: want error")
	}
	scanRow = origScan
	rowsErr = func(*sql.Rows) error { return errBoom }
	if err := run(db2); err == nil {
		t.Fatal("rows: want error")
	}
	// The events scan: second rows loop.
	rowsErr = origErr
	seedLinkable(t, db2, evAt(t, "e1", "uid-a", "2026-10-05T16:00:00Z", "2026-10-05T17:00:00Z"))
	unlink()
	n := 0
	scanRow = func(r *sql.Rows, d ...any) error {
		n++
		if n == 2 {
			return errBoom
		}
		return origScan(r, d...)
	}
	if err := run(db2); err == nil {
		t.Fatal("events scan: want error")
	}
	scanRow = origScan
	n = 0
	rowsErr = func(r *sql.Rows) error {
		n++
		if n == 2 {
			return errBoom
		}
		return origErr(r)
	}
	if err := run(db2); err == nil {
		t.Fatal("events rows: want error")
	}
}

func TestRecapStoreErrors(t *testing.T) {
	good := func() Batch {
		return Batch{Window: tw(t), Recaps: []Recap{recapOf(t, "c1")}, RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "m")}}
	}
	raise := func(event, table string) string {
		return `CREATE TRIGGER boom BEFORE ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'boom'); END`
	}
	tests := []struct {
		name  string
		setup func(db *sql.DB)
		b     Batch
	}{
		{"recap load", func(db *sql.DB) { exec(t, db, `DROP TABLE calendar_recaps`) }, good()},
		{"recap insert", func(db *sql.DB) { exec(t, db, raise("INSERT", "calendar_recaps")) }, good()},
		{"item load", func(db *sql.DB) { exec(t, db, `DROP TABLE calendar_recap_items`) }, good()},
		{"item insert", func(db *sql.DB) { exec(t, db, raise("INSERT", "calendar_recap_items")) }, good()},
		{"covered days", func(db *sql.DB) { exec(t, db, `DROP TABLE calendar_covered_days`) }, Batch{Window: tw(t), CoveredDays: []string{"2026-10-05"}}},
		{"gone", func(db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }, Batch{Window: tw(t), GoneSourceIDs: []string{"x"}}},
		{"stored row", func(db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }, Batch{Window: tw(t), Events: []Event{thin(t, t1)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			tt.setup(db)
			tx, _ := db.BeginTx(ctx, nil)
			defer func() { _ = tx.Rollback() }()
			if _, err := ApplyBatch(ctx, tx, tt.b, ApplyOptions{SkipMatches: true}, time.Now()); err == nil {
				t.Fatal("want error")
			}
		})
	}
	t.Run("supersede and update", func(t *testing.T) {
		for _, event := range []string{"UPDATE"} {
			db := openDB(t)
			batch(t, db, Batch{Window: tw(t), RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "a"), item("c1", ItemMention, OriginCatchUp, 1, "b")}}, "2026-10-06T01:00:00Z")
			exec(t, db, raise(event, "calendar_recap_items"))
			tx, _ := db.BeginTx(ctx, nil)
			_, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "a")}}, ApplyOptions{}, time.Now())
			_ = tx.Rollback()
			if err == nil {
				t.Fatal("supersede: want error")
			}
		}
	})
	t.Run("item scan", func(t *testing.T) {
		db := openDB(t)
		batch(t, db, Batch{Window: tw(t), RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "a")}}, "2026-10-06T01:00:00Z")
		orig, origRows := scanRow, rowsErr
		defer func() { scanRow, rowsErr = orig, origRows }()
		tx, _ := db.BeginTx(ctx, nil)
		defer func() { _ = tx.Rollback() }()
		scanRow = func(*sql.Rows, ...any) error { return errBoom }
		if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "a")}}, ApplyOptions{}, time.Now()); err == nil {
			t.Fatal("scan: want error")
		}
		scanRow = orig
		rowsErr = func(*sql.Rows) error { return errBoom }
		if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), RecapItems: []RecapItem{item("c1", ItemMention, OriginCatchUp, 0, "a")}}, ApplyOptions{}, time.Now()); err == nil {
			t.Fatal("rows: want error")
		}
	})
}

func TestCaptureRecapMergesPerField(t *testing.T) {
	ms, me := at(t, "2026-10-05T16:00:00Z"), at(t, "2026-10-05T17:00:00Z")
	exp := at(t, "2026-11-05T00:00:00Z")
	old := Recap{
		AccountID: "a", CallID: "c1", RecordingURL: "https://rec.example.test/1", DurationSeconds: 3600,
		RecordingStartAt: ms, RecordingEndAt: me, MeetingStartAt: ms, MeetingEndAt: me, IsMissed: true,
		HasConfRoomConnected: true, AttendanceStatus: "attended", AttendeesCount: 4, OrganizerID: "org-1",
		Headline: "Head", ShortSummary: "Short", SpeakersJSON: `["s"]`, TopicsJSON: `["t"]`,
	}
	got := CaptureRecap(&old, Recap{AccountID: "a", CallID: "c1", ExpiresAt: exp, AttendeesCount: 7})
	want := old
	want.ExpiresAt, want.AttendeesCount = exp, 7
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("poorer copy erased data:\n got %+v\nwant %+v", got, want)
	}
	// Speakers and topics fill independently.
	got = CaptureRecap(&Recap{AccountID: "a", CallID: "c1", SpeakersJSON: `["s"]`}, Recap{AccountID: "a", CallID: "c1", TopicsJSON: `["t"]`})
	if got.SpeakersJSON != `["s"]` || got.TopicsJSON != `["t"]` {
		t.Fatalf("speakers/topics: %+v", got)
	}
	// A non-empty value replaces; the sticky booleans stay true when a copy says false.
	got = CaptureRecap(&old, Recap{AccountID: "a", CallID: "c1", Headline: "New", MeetingStartAt: me, DurationSeconds: 5})
	// (the duration belongs to the stored URL, so a URL-less copy does not move it)
	if got.Headline != "New" || !got.MeetingStartAt.Equal(*me) || got.DurationSeconds != 3600 || !got.IsMissed || !got.HasConfRoomConnected {
		t.Fatalf("replace/sticky: %+v", got)
	}
}

func TestCaptureRecapRecordingTravelsAsOneUnit(t *testing.T) {
	old := CaptureRecap(nil, recapOf(t, "c1"))
	in := Recap{AccountID: acct, CallID: "c1", RecordingURL: "https://media.example.test/other"}
	got := CaptureRecap(&old, in)
	if got.RecordingURL != in.RecordingURL || got.RecordingStartAt != nil || got.RecordingEndAt != nil || got.DurationSeconds != 0 {
		t.Fatalf("old times or duration stayed beside the new URL: %+v", got)
	}
	// Without a URL the copy's recording times and duration are not taken over a stored URL.
	late := Recap{AccountID: acct, CallID: "c1", DurationSeconds: 5, RecordingStartAt: tp(t, "2026-10-05T18:00:00Z")}
	kept := CaptureRecap(&old, late)
	if kept.RecordingURL != old.RecordingURL || kept.DurationSeconds != 3600 || !kept.RecordingStartAt.Equal(*old.RecordingStartAt) {
		t.Fatalf("a URL-less copy moved the recording: %+v", kept)
	}
	// With no stored URL they fill, and the sticky booleans stay sticky.
	bare := CaptureRecap(nil, Recap{AccountID: acct, CallID: "c1", IsMissed: true})
	filled := CaptureRecap(&bare, late)
	later := CaptureRecap(&filled, Recap{AccountID: acct, CallID: "c1", RecordingEndAt: tp(t, "2026-10-05T19:00:00Z")})
	if later.RecordingEndAt == nil || later.RecordingStartAt == nil || later.DurationSeconds != 5 {
		t.Fatalf("an end time alone must fill without erasing: %+v", later)
	}
	if filled.DurationSeconds != 5 || filled.RecordingStartAt == nil || !filled.IsMissed {
		t.Fatalf("fill: %+v", filled)
	}
}

// TestCaptureRecapRecordingNeverMixesCopies runs every arrival order of copies with and without a
// URL and checks that the stored URL, times and duration always come from one copy: the last copy
// in that order that supplied a URL.
func TestCaptureRecapRecordingNeverMixesCopies(t *testing.T) {
	copies := []Recap{
		{AccountID: acct, CallID: "c", RecordingURL: "https://rec.example.test/1", RecordingStartAt: tp(t, "2026-10-05T16:00:00Z"), RecordingEndAt: tp(t, "2026-10-05T17:00:00Z"), DurationSeconds: 3600},
		{AccountID: acct, CallID: "c", RecordingURL: "https://rec.example.test/2"}, // a URL with no times
		{AccountID: acct, CallID: "c", RecordingStartAt: tp(t, "2026-10-05T18:00:00Z"), DurationSeconds: 5},
		{AccountID: acct, CallID: "c", RecordingURL: "https://rec.example.test/3", RecordingEndAt: tp(t, "2026-10-05T19:00:00Z"), DurationSeconds: 30},
	}
	for _, order := range permutations(len(copies)) {
		var stored *Recap
		var supplier *Recap
		for _, i := range order {
			next := CaptureRecap(stored, copies[i])
			stored = &next
			if copies[i].RecordingURL != "" {
				supplier = &copies[i]
			}
		}
		if stored.RecordingURL != supplier.RecordingURL || !reflect.DeepEqual(stored.RecordingStartAt, supplier.RecordingStartAt) ||
			!reflect.DeepEqual(stored.RecordingEndAt, supplier.RecordingEndAt) || stored.DurationSeconds != supplier.DurationSeconds {
			t.Fatalf("order %v mixed copies: %+v, supplier %+v", order, stored, supplier)
		}
	}
}
