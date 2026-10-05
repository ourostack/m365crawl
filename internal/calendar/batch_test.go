package calendar

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

const acct = "tenant-1/user-1"

func tw(t testing.TB) Window {
	w := window(t, SourceTeams, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z", "2026-10-06T00:00:00Z")
	w.AccountID = acct
	return w
}

// batch applies b in its own transaction with Teams options.
func batch(t testing.TB, db *sql.DB, b Batch, at string) BatchCounts {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := ApplyBatch(ctx, tx, b, ApplyOptions{SkipMatches: true}, mustTime(t, at))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return c
}

func listed(t testing.TB, db *sql.DB, q AgendaQuery) AgendaResult {
	t.Helper()
	if q.From.IsZero() {
		q.From, q.To = mustTime(t, octStart), mustTime(t, octEnd)
	}
	res, err := Agenda(ctx, db, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestApplyKeepsDetailAcrossBatches(t *testing.T) {
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Events: []Event{rich(t, t1)}}, "2026-10-06T01:00:00Z")
	c := batch(t, db, Batch{Window: tw(t), Events: []Event{thin(t, t3)}}, "2026-10-06T02:00:00Z")
	if c.Events.Changed != 1 {
		t.Fatalf("%+v", c)
	}
	got := listed(t, db, AgendaQuery{AccountID: acct}).Items
	if len(got) != 1 || got[0].AttendeesJSON != attendeesTwo || got[0].OnlineMeetingURL == "" {
		t.Fatalf("%+v", got)
	}
	if got[0].Key != "uid-1|" {
		t.Fatalf("key = %q", got[0].Key)
	}
	// The same copy again is unchanged, and no match rows exist (SkipMatches).
	if c := batch(t, db, Batch{Window: tw(t), Events: []Event{thin(t, t3)}}, "2026-10-06T03:00:00Z"); c.Events.Unchanged != 1 {
		t.Fatalf("%+v", c)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_matches`); n != 0 {
		t.Fatalf("matches = %d", n)
	}
	var first, seen string
	if err := db.QueryRow(`SELECT first_seen_at, seen_at FROM calendar_source_events`).Scan(&first, &seen); err != nil ||
		first != "2026-10-06T01:00:00.000Z" || seen != "2026-10-06T03:00:00.000Z" {
		t.Fatalf("%s %s %v", first, seen, err)
	}
}

func TestApplyGoneKeepsDetailThenReappears(t *testing.T) {
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Events: []Event{rich(t, t1)}}, "2026-10-06T01:00:00Z")
	c := batch(t, db, Batch{Window: tw(t), GoneSourceIDs: []string{"ev1", "unknown"}}, "2026-10-06T02:00:00Z")
	if c.Gone != 1 {
		t.Fatalf("%+v", c)
	}
	if len(listed(t, db, AgendaQuery{}).Items) != 0 {
		t.Fatal("a gone event is listed")
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at='2026-10-06T02:00:00.000Z' AND attendees_json<>''`); n != 1 {
		t.Fatal("gone row lost its data or removed_at is missing")
	}
	// Gone is sticky: a second gone does not rewrite it.
	if c := batch(t, db, Batch{Window: tw(t), GoneSourceIDs: []string{"ev1"}}, "2026-10-06T03:00:00Z"); c.Gone != 0 {
		t.Fatalf("%+v", c)
	}
	// Seen again with a thin copy: live, and the detail is still there.
	c = batch(t, db, Batch{Window: tw(t), Events: []Event{thin(t, t3)}}, "2026-10-06T04:00:00Z")
	items := listed(t, db, AgendaQuery{}).Items
	if c.Events.Changed != 1 || len(items) != 1 || items[0].AttendeesJSON != attendeesTwo {
		t.Fatalf("%+v %+v", c, items)
	}
	// An event of the batch is live even when listed gone.
	batch(t, db, Batch{Window: tw(t), Events: []Event{thin(t, t3)}, GoneSourceIDs: []string{"ev1"}}, "2026-10-06T05:00:00Z")
	if len(listed(t, db, AgendaQuery{}).Items) != 1 {
		t.Fatal("present beats gone")
	}
}

func TestGoneSkipsMasters(t *testing.T) {
	db := openDB(t)
	m := thin(t, t1)
	m.EventType = EventMaster
	batch(t, db, Batch{Window: tw(t), Events: []Event{m}}, "2026-10-06T01:00:00Z")
	if c := batch(t, db, Batch{Window: tw(t), GoneSourceIDs: []string{"ev1"}}, "2026-10-06T02:00:00Z"); c.Gone != 0 {
		t.Fatalf("%+v", c)
	}
	// And inferring unseen rows in a window skips masters too.
	tx, _ := db.BeginTx(ctx, nil)
	if err := ApplySnapshotTx(ctx, tx, tw(t), nil, mustTime(t, "2026-10-06T03:00:00Z")); err != nil {
		t.Fatal(err)
	}
	_ = tx.Commit()
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at IS NOT NULL`); n != 0 {
		t.Fatal("master marked removed")
	}
	res := listed(t, db, AgendaQuery{IncludeMasters: true})
	if len(res.Items) != 1 {
		t.Fatalf("masters must be listable on request: %+v", res.Items)
	}
}

func TestApplyBatchInsideCallerTransaction(t *testing.T) {
	db := openDB(t)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), Events: []Event{rich(t, t1)}, CoveredDays: []string{"2026-10-05"}}, ApplyOptions{SkipMatches: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"calendar_source_events", "calendar_sources", "calendar_covered_days"} {
		if n := count(t, db, `SELECT count(*) FROM `+table); n != 0 {
			t.Fatalf("%s kept %d rows after the caller rolled back", table, n)
		}
	}
}

func TestApplyPartitionsAccounts(t *testing.T) {
	db := openDB(t)
	other := tw(t)
	other.AccountID = "tenant-2/user-2"
	a, b := thin(t, t1), thin(t, t1)
	b.AccountID = other.AccountID
	b.Subject = "Other Account Sync"
	batch(t, db, Batch{Window: tw(t), Events: []Event{a}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: other, Events: []Event{b}}, "2026-10-06T01:00:00Z")
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events`); n != 2 {
		t.Fatalf("same key in two accounts must be two rows: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_sources`); n != 2 {
		t.Fatalf("sources = %d", n)
	}
	one := listed(t, db, AgendaQuery{AccountID: acct}).Items
	if len(one) != 1 || one[0].Subject != "Fixture Sync" {
		t.Fatalf("%+v", one)
	}
	all := listed(t, db, AgendaQuery{}).Items
	if len(all) != 2 {
		t.Fatalf("all accounts: %+v", all)
	}
	// Gone and window removal touch one account only.
	batch(t, db, Batch{Window: tw(t), GoneSourceIDs: []string{"ev1"}}, "2026-10-06T02:00:00Z")
	if got := listed(t, db, AgendaQuery{}).Items; len(got) != 1 || got[0].AccountID != other.AccountID {
		t.Fatalf("%+v", got)
	}
	// Events from another account are refused.
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), Events: []Event{b}}, ApplyOptions{}, time.Now()); err == nil {
		t.Fatal("want account mismatch")
	}
}

func TestApplyPartitionsMatchesByAccount(t *testing.T) {
	// Cross-source joins happen inside one account: the same event in two accounts stays two.
	db := openDB(t)
	for _, account := range []string{"a1", "a2"} {
		w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
		w.AccountID = account
		e := timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")
		e.AccountID = account
		apply(t, db, w, "2026-10-02T01:00:00Z", e)
		ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
		ow.AccountID = account
		o := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
		o.AccountID, o.GlobalID = account, "uid-"+account
		apply(t, db, ow, "2026-10-02T02:00:00Z", o)
	}
	if got := listed(t, db, AgendaQuery{}).Items; len(got) != 2 {
		t.Fatalf("each account joins its own pair: %+v", got)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='upgraded' OR event_key LIKE 'uid-a%'`); n < 2 {
		t.Fatalf("matches = %d", n)
	}
}

func TestApplySkipMatchesWritesNoMatchRows(t *testing.T) {
	db := openDB(t)
	batch(t, db, Batch{Window: tw(t), Events: []Event{rich(t, t1), func() Event { e := thin(t, t1); e.SourceID, e.GlobalID = "ev2", "uid-2"; return e }()}}, "2026-10-06T01:00:00Z")
	if n := count(t, db, `SELECT count(*) FROM calendar_matches`); n != 0 {
		t.Fatalf("matches = %d", n)
	}
	// Without SkipMatches the same event gets a match row.
	other := openDB(t)
	apply(t, other, window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z"), "2026-10-02T01:00:00Z", timed(t, SourceTeams, "t", "T", "2026-10-05T16:00:00Z"))
	if n := count(t, other, `SELECT count(*) FROM calendar_matches`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
}

func TestCoveredDaysCumulativeAndGap(t *testing.T) {
	db := openDB(t)
	w := tw(t)
	batch(t, db, Batch{Window: w, CoveredDays: []string{"2026-10-05", "2026-10-07"}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: w, CoveredDays: []string{"2026-10-05"}}, "2026-10-09T01:00:00Z")
	if n := count(t, db, `SELECT count(*) FROM calendar_covered_days`); n != 2 {
		t.Fatalf("days are never removed: %d", n)
	}
	var first, last string
	_ = db.QueryRow(`SELECT first_verified_at, last_verified_at FROM calendar_covered_days WHERE day='2026-10-05'`).Scan(&first, &last)
	if first != "2026-10-06T01:00:00.000Z" || last != "2026-10-09T01:00:00.000Z" {
		t.Fatalf("%s %s", first, last)
	}
	q := func(from, to string) AgendaResult {
		return listed(t, db, AgendaQuery{AccountID: acct, From: mustTime(t, from), To: mustTime(t, to)})
	}
	if r := q("2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z"); r.Gap {
		t.Fatal("a covered day is not a gap")
	}
	// The window (10-05 to 10-06) is ignored for a source that has covered days.
	if r := q("2026-10-05T00:00:00Z", "2026-10-08T00:00:00Z"); !r.Gap {
		t.Fatal("10-06 was never loaded: gap")
	}
	if r := q("2026-10-07T12:00:00Z", "2026-10-07T13:00:00Z"); r.Gap {
		t.Fatal("a mid-day range inside a covered day is covered")
	}
}

func TestCoverageAsOf(t *testing.T) {
	db := openDB(t)
	w := tw(t)
	batch(t, db, Batch{Window: w, CoveredDays: []string{"2026-10-05"}}, "2026-10-06T01:00:00Z")
	batch(t, db, Batch{Window: w, CoveredDays: []string{"2026-10-07"}}, "2026-10-20T01:00:00Z")
	r := listed(t, db, AgendaQuery{AccountID: acct, From: mustTime(t, "2026-10-05T00:00:00Z"), To: mustTime(t, "2026-10-06T00:00:00Z")})
	if !r.AsOf.Equal(mustTime(t, "2026-10-06T01:00:00Z")) {
		t.Fatalf("as of %v", r.AsOf)
	}
	// Two covered days: the oldest verification is reported.
	batch(t, db, Batch{Window: w, CoveredDays: []string{"2026-10-06"}}, "2026-10-21T01:00:00Z")
	r = listed(t, db, AgendaQuery{AccountID: acct, From: mustTime(t, "2026-10-05T00:00:00Z"), To: mustTime(t, "2026-10-08T00:00:00Z")})
	if r.Gap || !r.AsOf.Equal(mustTime(t, "2026-10-06T01:00:00Z")) {
		t.Fatalf("%v %v", r.Gap, r.AsOf)
	}
	// A contiguous window source reports its sync time.
	db2 := openDB(t)
	apply(t, db2, window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z"), "2026-10-02T01:00:00Z")
	r = listed(t, db2, AgendaQuery{})
	if r.Gap || !r.AsOf.Equal(mustTime(t, "2026-10-02T00:00:00Z")) {
		t.Fatalf("%v %v", r.Gap, r.AsOf)
	}
	// Nothing covers an empty archive.
	if r := listed(t, openDB(t), AgendaQuery{}); !r.Gap || !r.AsOf.IsZero() {
		t.Fatalf("%+v", r)
	}
	if r := listed(t, db, AgendaQuery{From: mustTime(t, octStart), To: mustTime(t, octStart)}); r.Gap || len(r.Items) != 0 {
		t.Fatal("an empty range asks for nothing")
	}
}

func TestAgendaHidesCancelledDeclinedMasters(t *testing.T) {
	db := openDB(t)
	mk := func(id, subject string, edit func(*Event)) Event {
		e := thin(t, t1)
		e.SourceID, e.GlobalID, e.Subject, e.Organizer, e.Location = id, "uid-"+id, subject, "Pat Organizer", "Hall"
		edit(&e)
		return e
	}
	events := []Event{
		mk("a", "Plain", func(*Event) {}),
		mk("b", "Cancelled", func(e *Event) { e.Cancelled = true }),
		mk("c", "Declined", func(e *Event) { e.Response = "declined" }),
		mk("d", "Master", func(e *Event) { e.EventType = EventMaster }),
		mk("e", "Searchable Zebra", func(e *Event) { e.Location = "Zebra Room" }),
	}
	batch(t, db, Batch{Window: tw(t), Events: events}, "2026-10-06T01:00:00Z")
	subjects := func(q AgendaQuery) string {
		var s []string
		for _, it := range listed(t, db, q).Items {
			s = append(s, it.Subject)
		}
		return strings.Join(s, ",")
	}
	if got := subjects(AgendaQuery{}); got != "Plain,Searchable Zebra" {
		t.Fatalf("default: %s", got)
	}
	if got := subjects(AgendaQuery{IncludeCancelled: true}); !strings.Contains(got, "Cancelled") || strings.Contains(got, "Declined") {
		t.Fatalf("cancelled: %s", got)
	}
	if got := subjects(AgendaQuery{IncludeDeclined: true}); !strings.Contains(got, "Declined") || strings.Contains(got, "Cancelled") {
		t.Fatalf("declined: %s", got)
	}
	if got := subjects(AgendaQuery{IncludeMasters: true}); !strings.Contains(got, "Master") {
		t.Fatalf("masters: %s", got)
	}
	for _, needle := range []string{"zebra", "PAT ORG", "searchable"} {
		if got := subjects(AgendaQuery{Query: needle}); !strings.Contains(got, "Searchable Zebra") {
			t.Fatalf("query %q: %s", needle, got)
		}
	}
	if got := subjects(AgendaQuery{Query: "no such text"}); got != "" {
		t.Fatalf("query miss: %s", got)
	}
}

func TestAgendaOrderIndependentWithAccounts(t *testing.T) {
	mk := func(account string) []Event {
		e := thin(t, t1)
		e.AccountID, e.Subject = account, "Sync "+account
		return []Event{e}
	}
	build := func(order []string) []AgendaItem {
		db := openDB(t)
		for _, a := range order {
			w := tw(t)
			w.AccountID = a
			batch(t, db, Batch{Window: w, Events: mk(a)}, "2026-10-06T01:00:00Z")
		}
		return listed(t, db, AgendaQuery{}).Items
	}
	x, y := build([]string{"a1", "a2", "a3"}), build([]string{"a3", "a1", "a2"})
	if len(x) != 3 || !reflect.DeepEqual(x, y) {
		t.Fatalf("%+v\n%+v", x, y)
	}
}

func TestApplyBatchRejectsBadRecapRows(t *testing.T) {
	db := openDB(t)
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	bad := []Batch{
		{Window: tw(t), Recaps: []Recap{{AccountID: "other", CallID: "c"}}},
		{Window: tw(t), Recaps: []Recap{{AccountID: acct}}},
		{Window: tw(t), RecapItems: []RecapItem{{AccountID: "other", CallID: "c"}}},
		{Window: tw(t), Events: []Event{{Source: SourceOutlook, AccountID: acct}}},
	}
	for i, b := range bad {
		if _, err := ApplyBatch(ctx, tx, b, ApplyOptions{}, time.Now()); err == nil {
			t.Errorf("batch %d accepted", i)
		}
	}
}

func TestAgendaSameEventInTwoAccountsOrdersByAccount(t *testing.T) {
	db := openDB(t)
	for _, a := range []string{"a2", "a1"} {
		w := tw(t)
		w.AccountID = a
		e := thin(t, t1)
		e.AccountID = a
		batch(t, db, Batch{Window: w, Events: []Event{e}}, "2026-10-06T01:00:00Z")
	}
	items := listed(t, db, AgendaQuery{}).Items
	if len(items) != 2 || items[0].AccountID != "a1" || items[1].AccountID != "a2" {
		t.Fatalf("%+v", items)
	}
}

func TestBatchReadErrors(t *testing.T) {
	q := AgendaQuery{From: mustTime(t, octStart), To: mustTime(t, octEnd)}
	db := openDB(t)
	exec(t, db, `DROP TABLE calendar_covered_days`)
	if _, err := Agenda(ctx, db, q); err == nil {
		t.Fatal("covered days query: want error")
	}
	db = openDB(t)
	batch(t, db, Batch{Window: tw(t), CoveredDays: []string{"2026-10-05"}}, "2026-10-06T01:00:00Z")
	exec(t, db, `UPDATE calendar_covered_days SET last_verified_at='soon'`)
	if _, err := Agenda(ctx, db, q); err == nil {
		t.Fatal("covered days scan: want error")
	}
	// A stored row that fails to scan, and a gone update whose result cannot be read.
	db = openDB(t)
	batch(t, db, Batch{Window: tw(t), Events: []Event{thin(t, t1)}}, "2026-10-06T01:00:00Z")
	orig := scanRow
	t.Cleanup(func() { scanRow = orig })
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	tx, _ := db.BeginTx(ctx, nil)
	defer func() { _ = tx.Rollback() }()
	if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), Events: []Event{thin(t, t3)}}, ApplyOptions{SkipMatches: true}, time.Now()); err == nil {
		t.Fatal("stored row scan: want error")
	}
	if _, err := ApplyBatch(ctx, tx, Batch{Window: tw(t), GoneSourceIDs: []string{"ev1"}}, ApplyOptions{}, time.Now()); err == nil {
		t.Fatal("gone scan: want error")
	}
}

func TestCaptureReminderAloneKeepsDetailClock(t *testing.T) {
	old := Capture(nil, thin(t, t1))
	in := thin(t, t2)
	five := 5
	in.ReminderMinutes = &five
	got := Capture(&old, in)
	if got.ReminderMinutes == nil || *got.ReminderMinutes != 5 || got.DetailAsOf != nil {
		t.Fatalf("%+v", got)
	}
}

func TestLinkRecapsEventEarlierThanRecap(t *testing.T) {
	db := openDB(t)
	seedLinkable(t, db, evAt(t, "e1", "uid-a", "2026-10-05T15:59:50Z", "2026-10-05T16:59:50Z"))
	batch(t, db, Batch{Window: tw(t), Recaps: []Recap{unlinked(t, "c1")}}, "2026-10-06T02:00:00Z")
	if uid, _ := linkState(t, db, "c1"); uid != "uid-a" {
		t.Fatalf("uid %q", uid)
	}
}
