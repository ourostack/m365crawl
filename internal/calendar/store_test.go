package calendar

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func window(t testing.TB, src Source, start, end, fresh string) Window {
	return Window{Source: src, Start: mustTime(t, start), End: mustTime(t, end), SyncedAt: mustTime(t, fresh), CacheFreshAt: mustTime(t, fresh)}
}

func apply(t testing.TB, db *sql.DB, w Window, at string, events ...Event) {
	t.Helper()
	if err := ApplySnapshot(ctx, db, w, events, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
}

func timed(t testing.TB, src Source, id, subject, start string) Event {
	s := mustTime(t, start)
	return Event{Source: src, SourceID: id, Subject: subject, Organizer: "ada@example.com", Start: s, End: s.Add(time.Hour), LastModified: tp(t, "2026-10-01T00:00:00Z")}
}

func count(t testing.TB, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func agenda(t testing.TB, db *sql.DB, from, to string) ([]AgendaItem, bool) {
	t.Helper()
	items, gap, err := Agenda(ctx, db, mustTime(t, from), mustTime(t, to))
	if err != nil {
		t.Fatal(err)
	}
	return items, gap
}

const (
	octStart = "2026-10-01T00:00:00Z"
	octEnd   = "2026-11-01T00:00:00Z"
)

func TestApplySnapshotRemovesUnseenInWindow(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	a := timed(t, SourceTeams, "a", "A", "2026-10-05T16:00:00Z")
	b := timed(t, SourceTeams, "b", "B", "2026-10-06T16:00:00Z")
	day := Event{Source: SourceTeams, SourceID: "d", Subject: "Day", AllDay: true, StartDate: "2026-10-07", EndDate: "2026-10-08"}
	apply(t, db, w, "2026-10-02T01:00:00Z", a, b, day)
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at IS NULL`); n != 3 {
		t.Fatalf("live rows = %d", n)
	}
	apply(t, db, w, "2026-10-02T02:00:00Z", a)
	items, _ := agenda(t, db, octStart, octEnd)
	if len(items) != 1 || items[0].Subject != "A" {
		t.Fatalf("agenda = %+v", items)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events`); n != 3 {
		t.Fatalf("rows deleted instead of marked: %d", n)
	}
	// removed_at is sticky: a later snapshot does not rewrite it.
	apply(t, db, w, "2026-10-02T03:00:00Z", a)
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at = '2026-10-02T02:00:00.000Z'`); n != 2 {
		t.Fatalf("removed_at not sticky: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_sources WHERE source='teams' AND synced_at='2026-10-02T00:00:00.000Z'`); n != 1 {
		t.Fatal("window not recorded")
	}
}

func TestApplySnapshotKeepsOutsideWindow(t *testing.T) {
	db := openDB(t)
	wide := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	early := timed(t, SourceTeams, "early", "Early", "2026-10-02T09:00:00Z")
	late := timed(t, SourceTeams, "late", "Late", "2026-10-20T09:00:00Z")
	edge := timed(t, SourceTeams, "edge", "Edge", "2026-10-10T00:00:00Z")
	dayIn := Event{Source: SourceTeams, SourceID: "din", AllDay: true, StartDate: "2026-10-10", EndDate: "2026-10-11"}
	dayOut := Event{Source: SourceTeams, SourceID: "dout", AllDay: true, StartDate: "2026-10-20", EndDate: "2026-10-21"}
	apply(t, db, wide, "2026-10-02T01:00:00Z", early, late, edge, dayIn, dayOut)
	// A narrower snapshot [10-03, 10-10) sees nothing: early and late are outside and survive, and
	// an event starting exactly at the exclusive end survives too; the all-day inside is removed.
	narrow := window(t, SourceTeams, "2026-10-03T00:00:00Z", "2026-10-10T00:00:00Z", "2026-10-03T00:00:00Z")
	apply(t, db, narrow, "2026-10-03T01:00:00Z")
	removed := func(id string) bool {
		return count(t, db, `SELECT count(*) FROM calendar_source_events WHERE source_id=? AND removed_at IS NOT NULL`, id) == 1
	}
	for _, id := range []string{"early", "late", "edge", "dout"} {
		if removed(id) {
			t.Fatalf("%s outside the window was removed", id)
		}
	}
	if removed("din") {
		t.Fatal("din starts on 10-10, the exclusive end date, and must survive")
	}
	// A window ending mid-day keeps that last date inside the window.
	mid := window(t, SourceTeams, "2026-10-03T00:00:00Z", "2026-10-10T12:00:00Z", "2026-10-04T00:00:00Z")
	apply(t, db, mid, "2026-10-04T01:00:00Z")
	if !removed("din") || !removed("edge") {
		t.Fatal("rows inside the window must be removed")
	}
	// Other sources are never touched.
	other := timed(t, SourceOutlook, "o", "O", "2026-10-05T09:00:00Z")
	apply(t, db, window(t, SourceOutlook, octStart, octEnd, "2026-10-04T00:00:00Z"), "2026-10-04T02:00:00Z", other)
	apply(t, db, narrow, "2026-10-05T01:00:00Z")
	if removed("o") {
		t.Fatal("a teams snapshot removed an outlook row")
	}
}

func TestApplySnapshotReappearClearsRemoved(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	a := timed(t, SourceOutlook, "a", "A", "2026-10-05T16:00:00Z")
	apply(t, db, w, "2026-10-02T01:00:00Z", a)
	apply(t, db, w, "2026-10-02T02:00:00Z")
	if items, _ := agenda(t, db, octStart, octEnd); len(items) != 0 {
		t.Fatal("removed event visible")
	}
	a.Subject = "A again"
	apply(t, db, w, "2026-10-02T03:00:00Z", a)
	items, _ := agenda(t, db, octStart, octEnd)
	if len(items) != 1 || items[0].Subject != "A again" {
		t.Fatalf("agenda = %+v", items)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE removed_at IS NOT NULL`); n != 0 {
		t.Fatal("removed_at not cleared")
	}
}

func TestCompositeMatchPersists(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	e := timed(t, SourceTeams, "t1", "Sync", "2026-10-05T16:00:00Z")
	apply(t, db, w, "2026-10-02T01:00:00Z", e)
	first, _ := agenda(t, db, octStart, octEnd)
	e.Subject = "Sync (renamed)"
	apply(t, db, w, "2026-10-02T02:00:00Z", e)
	second, _ := agenda(t, db, octStart, octEnd)
	if len(first) != 1 || len(second) != 1 || first[0].Key != second[0].Key || second[0].Subject != "Sync (renamed)" {
		t.Fatalf("key flipped: %+v -> %+v", first, second)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='composite'`); n != 1 {
		t.Fatalf("matches = %d", n)
	}
}

func TestCompositeUpgradeEitherOrder(t *testing.T) {
	for _, teamsFirst := range []bool{true, false} {
		db := openDB(t)
		tw := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
		ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
		te := timed(t, SourceTeams, "t1", "Weekly Sync", "2026-10-05T16:00:00Z")
		oe := timed(t, SourceOutlook, "o1", "weekly  sync", "2026-10-05T16:00:00Z")
		oe.GlobalID = "uid-1"
		oe.Location = "Room 9"
		if teamsFirst {
			apply(t, db, tw, "2026-10-02T01:00:00Z", te)
			apply(t, db, ow, "2026-10-02T02:00:00Z", oe)
		} else {
			apply(t, db, ow, "2026-10-02T01:00:00Z", oe)
			apply(t, db, tw, "2026-10-02T02:00:00Z", te)
		}
		items, _ := agenda(t, db, octStart, octEnd)
		if len(items) != 1 {
			t.Fatalf("teamsFirst=%v: want one merged event, got %+v", teamsFirst, items)
		}
		it := items[0]
		if it.Key != "uid-1|" || it.Source != SourceOutlook || it.GlobalID != "uid-1" || it.Location != "Room 9" {
			t.Fatalf("teamsFirst=%v: %+v", teamsFirst, it)
		}
		if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE event_key='uid-1|'`); n != 2 {
			t.Fatalf("both sources must sit under the global key, got %d", n)
		}
		if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='upgraded'`); n != 1 {
			t.Fatalf("upgraded matches = %d", n)
		}
		// A later sync keeps the upgraded keys.
		apply(t, db, tw, "2026-10-02T03:00:00Z", te)
		apply(t, db, ow, "2026-10-02T03:00:00Z", oe)
		if items, _ := agenda(t, db, octStart, octEnd); len(items) != 1 || items[0].Key != "uid-1|" {
			t.Fatalf("teamsFirst=%v: resync: %+v", teamsFirst, items)
		}
	}
}

func TestCompositeAmbiguousNotUpgraded(t *testing.T) {
	// Two Teams events share organizer, subject and start, so they match one composite hash.
	db := openDB(t)
	tw := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	t1 := timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")
	t2 := timed(t, SourceTeams, "t2", "Standup", "2026-10-05T16:00:00Z")
	apply(t, db, tw, "2026-10-02T01:00:00Z", t1, t2)
	if n := count(t, db, `SELECT count(DISTINCT event_key) FROM calendar_source_events`); n != 2 {
		t.Fatalf("twins collapsed into %d keys", n)
	}
	o := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
	o.GlobalID = "uid-9"
	apply(t, db, ow, "2026-10-02T02:00:00Z", o)
	if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='ambiguous' AND source='outlook'`); n != 1 {
		t.Fatalf("ambiguous matches = %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='upgraded'`); n != 0 {
		t.Fatal("ambiguous rows were upgraded")
	}
	items, _ := agenda(t, db, octStart, octEnd)
	if len(items) != 3 {
		t.Fatalf("want three separate events, got %d", len(items))
	}
	// The reverse order: the Outlook row first, then the twins. Same answer.
	db2 := openDB(t)
	apply(t, db2, ow, "2026-10-02T01:00:00Z", o)
	apply(t, db2, tw, "2026-10-02T02:00:00Z", t1, t2)
	if items2, _ := agenda(t, db2, octStart, octEnd); len(items2) != 3 {
		t.Fatalf("want three separate events, got %d: %+v", len(items2), items2)
	}
	for _, key := range []string{"composite|", "#t1", "#t2"} {
		if n := count(t, db2, `SELECT count(*) FROM calendar_source_events WHERE source='teams' AND event_key LIKE ?`, "%"+key+"%"); n == 0 {
			t.Fatalf("twin key part %q missing", key)
		}
	}
}

func TestCompositeAmbiguousFromComposite(t *testing.T) {
	// Two Outlook rows with global ids match one Teams composite event: it joins neither.
	db := openDB(t)
	tw := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	o1 := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
	o1.GlobalID = "uid-1"
	o2 := o1
	o2.SourceID, o2.GlobalID = "o2", "uid-2"
	apply(t, db, ow, "2026-10-02T01:00:00Z", o1, o2)
	apply(t, db, tw, "2026-10-02T02:00:00Z", timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z"))
	if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method='ambiguous' AND source='teams'`); n != 1 {
		t.Fatalf("ambiguous matches = %d", n)
	}
	if items, _ := agenda(t, db, octStart, octEnd); len(items) != 3 {
		t.Fatalf("got %d events", len(items))
	}
}

func TestApplySnapshotRejectsForeignSource(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	err := ApplySnapshot(ctx, db, w, []Event{timed(t, SourceOutlook, "o", "O", "2026-10-05T16:00:00Z")}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "outlook") {
		t.Fatalf("err = %v", err)
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_sources`); n != 0 {
		t.Fatal("rejected snapshot recorded a window")
	}
}

func TestAgendaAllDayAcrossZones(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceOutlook, "2026-09-01T00:00:00Z", "2026-12-01T00:00:00Z", "2026-10-02T00:00:00Z")
	day := Event{Source: SourceOutlook, SourceID: "d", Subject: "Offsite", AllDay: true, StartDate: "2026-10-05", EndDate: "2026-10-06"}
	multi := Event{Source: SourceOutlook, SourceID: "m", Subject: "Trip", AllDay: true, StartDate: "2026-10-05", EndDate: "2026-10-08"}
	bare := Event{Source: SourceOutlook, SourceID: "b", Subject: "Bare", AllDay: true, StartDate: "2026-10-05"}
	apply(t, db, w, "2026-10-02T01:00:00Z", day, multi, bare)
	west, east := time.FixedZone("UTC-7", -7*3600), time.FixedZone("UTC+9", 9*3600)
	for _, loc := range []*time.Location{west, east, time.UTC} {
		from := time.Date(2026, 10, 5, 0, 0, 0, 0, loc)
		items, _, err := Agenda(ctx, db, from, from.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 3 {
			t.Fatalf("%s: want 3 events on 10-05, got %d", loc, len(items))
		}
		prev := time.Date(2026, 10, 4, 0, 0, 0, 0, loc)
		if items, _, _ := Agenda(ctx, db, prev, prev.AddDate(0, 0, 1)); len(items) != 0 {
			t.Fatalf("%s: all-day event shifted onto 10-04: %+v", loc, items)
		}
		next := time.Date(2026, 10, 6, 0, 0, 0, 0, loc)
		items, _, _ = Agenda(ctx, db, next, next.AddDate(0, 0, 1))
		if len(items) != 1 || items[0].Subject != "Trip" {
			t.Fatalf("%s: 10-06 should hold only the multi-day trip: %+v", loc, items)
		}
		if items, _, _ := Agenda(ctx, db, time.Date(2026, 10, 8, 0, 0, 0, 0, loc), time.Date(2026, 10, 9, 0, 0, 0, 0, loc)); len(items) != 0 {
			t.Fatalf("%s: exclusive end date included", loc)
		}
	}
	// A range ending after midnight local picks up the next date.
	from := time.Date(2026, 10, 4, 12, 0, 0, 0, west)
	if items, _, _ := Agenda(ctx, db, from, from.Add(24*time.Hour)); len(items) != 3 {
		t.Fatalf("got %d", len(items))
	}
}

func TestAgendaTimedOverlap(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	in := timed(t, SourceTeams, "in", "In", "2026-10-05T10:00:00Z")
	straddle := timed(t, SourceTeams, "s", "Straddle", "2026-10-05T09:30:00Z")
	endsAtFrom := timed(t, SourceTeams, "e", "EndsAtFrom", "2026-10-05T09:00:00Z")
	startsAtTo := timed(t, SourceTeams, "x", "StartsAtTo", "2026-10-05T12:00:00Z")
	instant := timed(t, SourceTeams, "i", "Instant", "2026-10-05T10:00:00Z")
	instant.End = instant.Start
	atFrom := timed(t, SourceTeams, "f", "AtFrom", "2026-10-05T10:00:00Z")
	atFrom.End = atFrom.Start
	atFrom.SourceID, atFrom.Subject = "f2", "AtFrom"
	apply(t, db, w, "2026-10-02T01:00:00Z", in, straddle, endsAtFrom, startsAtTo, instant)
	items, _ := agenda(t, db, "2026-10-05T10:00:00Z", "2026-10-05T12:00:00Z")
	var got []string
	for _, it := range items {
		got = append(got, it.Subject)
	}
	// Sorted by start; ties by subject.
	want := []string{"Straddle", "In", "Instant"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if items, _ := agenda(t, db, "2026-10-05T12:00:00Z", "2026-10-05T12:00:00Z"); len(items) != 0 {
		t.Fatal("empty range returned events")
	}
}

func TestAgendaSortsAllDayFirstThenKey(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	day := Event{Source: SourceTeams, SourceID: "d", Subject: "Z day", AllDay: true, StartDate: "2026-10-05"}
	early := timed(t, SourceTeams, "e", "Early", "2026-10-05T00:00:00Z")
	same1 := timed(t, SourceTeams, "s1", "Same", "2026-10-05T01:00:00Z")
	same1.Organizer = "b"
	same2 := timed(t, SourceTeams, "s2", "Same", "2026-10-05T01:00:00Z")
	same2.Organizer = "a"
	apply(t, db, w, "2026-10-02T01:00:00Z", same1, early, day, same2)
	items, _ := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z")
	if len(items) != 4 || items[0].Subject != "Z day" || items[1].Subject != "Early" {
		t.Fatalf("order: %+v", items)
	}
	if items[2].Key > items[3].Key {
		t.Fatal("equal start and subject must order by key")
	}
}

func TestAgendaCoverageGap(t *testing.T) {
	db := openDB(t)
	_, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-06T00:00:00Z")
	if !gap {
		t.Fatal("no sources at all must be a gap")
	}
	apply(t, db, window(t, SourceTeams, "2026-10-01T00:00:00Z", "2026-10-10T00:00:00Z", "2026-10-02T00:00:00Z"), "2026-10-02T01:00:00Z")
	if _, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-10T00:00:00Z"); gap {
		t.Fatal("range inside one window is not a gap")
	}
	if _, gap := agenda(t, db, "2026-09-30T00:00:00Z", "2026-10-02T00:00:00Z"); !gap {
		t.Fatal("range starting before the window is a gap")
	}
	if _, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-11T00:00:00Z"); !gap {
		t.Fatal("range ending after the window is a gap")
	}
	// A second source that abuts the first closes the hole; one that leaves a hole does not.
	apply(t, db, window(t, SourceOutlook, "2026-10-10T00:00:00Z", "2026-10-20T00:00:00Z", "2026-10-02T00:00:00Z"), "2026-10-02T01:00:00Z")
	if _, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-15T00:00:00Z"); gap {
		t.Fatal("abutting windows cover the range")
	}
	apply(t, db, window(t, SourceTeams, "2026-10-01T00:00:00Z", "2026-10-08T00:00:00Z", "2026-10-03T00:00:00Z"), "2026-10-03T01:00:00Z")
	if _, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-15T00:00:00Z"); !gap {
		t.Fatal("hole between windows must be a gap")
	}
	// A window nested inside another does not shrink coverage.
	apply(t, db, window(t, SourceTeams, "2026-10-01T00:00:00Z", "2026-10-20T00:00:00Z", "2026-10-04T00:00:00Z"), "2026-10-04T01:00:00Z")
	apply(t, db, window(t, SourceOutlook, "2026-10-12T00:00:00Z", "2026-10-13T00:00:00Z", "2026-10-04T00:00:00Z"), "2026-10-04T01:00:00Z")
	if _, gap := agenda(t, db, "2026-10-05T00:00:00Z", "2026-10-15T00:00:00Z"); gap {
		t.Fatal("covered range reported as gap")
	}
}

func TestAgendaSameSnapshotsSameResult(t *testing.T) {
	tw := window(t, SourceTeams, octStart, octEnd, "2026-10-02T05:00:00Z")
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T04:00:00Z")
	te := timed(t, SourceTeams, "t1", "Weekly Sync", "2026-10-05T16:00:00Z")
	te.Location = "Teams link"
	te.LastModified = tp(t, "2026-10-01T00:00:00Z")
	oe := timed(t, SourceOutlook, "o1", "weekly sync", "2026-10-05T16:00:00Z")
	oe.GlobalID, oe.BodyPreview = "uid-1", "agenda"
	oe.LastModified = tp(t, "2026-10-01T00:00:00Z")
	tOnly := timed(t, SourceTeams, "t2", "Teams only", "2026-10-06T16:00:00Z")
	oOnly := timed(t, SourceOutlook, "o2", "Outlook only", "2026-10-06T16:00:00Z")
	day := Event{Source: SourceOutlook, SourceID: "d", GlobalID: "uid-d", Subject: "Day", AllDay: true, StartDate: "2026-10-07"}

	a, b := openDB(t), openDB(t)
	apply(t, a, tw, "2026-10-02T06:00:00Z", te, tOnly)
	apply(t, a, ow, "2026-10-02T07:00:00Z", oe, oOnly, day)
	apply(t, b, ow, "2026-10-02T07:00:00Z", day, oOnly, oe)
	apply(t, b, tw, "2026-10-02T06:00:00Z", tOnly, te)
	ia, ga := agenda(t, a, octStart, octEnd)
	ib, gb := agenda(t, b, octStart, octEnd)
	if ga != gb || len(ia) != 4 || len(ia) != len(ib) {
		t.Fatalf("len a=%d b=%d gap %v %v", len(ia), len(ib), ga, gb)
	}
	for i := range ia {
		if !reflect.DeepEqual(ia[i], ib[i]) {
			t.Fatalf("row %d differs:\n%+v\n%+v", i, ia[i], ib[i])
		}
	}
	for _, it := range ia {
		if it.GlobalID == "uid-1" && (it.Location != "Teams link" || it.BodyPreview != "agenda" || it.Source != SourceTeams) {
			t.Fatalf("merge result %+v", it)
		}
	}

	// Probe scenarios: whichever source syncs first, the agenda is identical.
	t.Run("two outlook ids, one teams without", func(t *testing.T) {
		o1 := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
		o1.GlobalID = "uid-1"
		o2 := o1
		o2.SourceID, o2.GlobalID = "o2", "uid-2"
		items := bothOrders(t, tw, []Event{timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")}, ow, []Event{o1, o2})
		if len(items) != 3 {
			t.Fatalf("want 3 events, got %d", len(items))
		}
	})
	t.Run("two teams twins, one outlook id", func(t *testing.T) {
		o := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
		o.GlobalID = "uid-1"
		items := bothOrders(t, tw, []Event{
			timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z"),
			timed(t, SourceTeams, "t2", "Standup", "2026-10-05T16:00:00Z"),
		}, ow, []Event{o})
		if len(items) != 3 {
			t.Fatalf("want 3 events, got %d", len(items))
		}
	})
	t.Run("one each upgrades", func(t *testing.T) {
		o := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
		o.GlobalID = "uid-1"
		items := bothOrders(t, tw, []Event{timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")}, ow, []Event{o})
		if len(items) != 1 {
			t.Fatalf("want 1 event, got %d", len(items))
		}
	})
	t.Run("outlook with and without id, one teams", func(t *testing.T) {
		o1 := timed(t, SourceOutlook, "o1", "Standup", "2026-10-05T16:00:00Z")
		o1.GlobalID = "uid-1"
		o2 := o1
		o2.SourceID, o2.GlobalID = "o2", ""
		bothOrders(t, tw, []Event{timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")}, ow, []Event{o1, o2})
	})
}

// bothOrders feeds the two snapshots to two fresh databases in opposite order and requires the
// same agenda, keys included.
func bothOrders(t *testing.T, tw Window, teams []Event, ow Window, outlook []Event) []AgendaItem {
	t.Helper()
	a, b := openDB(t), openDB(t)
	apply(t, a, tw, "2026-10-02T06:00:00Z", teams...)
	apply(t, a, ow, "2026-10-02T07:00:00Z", outlook...)
	apply(t, b, ow, "2026-10-02T07:00:00Z", outlook...)
	apply(t, b, tw, "2026-10-02T06:00:00Z", teams...)
	ia, _ := agenda(t, a, octStart, octEnd)
	ib, _ := agenda(t, b, octStart, octEnd)
	if !reflect.DeepEqual(ia, ib) {
		t.Fatalf("order changed the agenda:\n%+v\n%+v", ia, ib)
	}
	return ia
}

func TestTwinKeysIgnoreSnapshotOrder(t *testing.T) {
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	t1 := timed(t, SourceTeams, "t1", "Standup", "2026-10-05T16:00:00Z")
	t2 := timed(t, SourceTeams, "t2", "Standup", "2026-10-05T16:00:00Z")
	a, b := openDB(t), openDB(t)
	apply(t, a, w, "2026-10-02T01:00:00Z", t1, t2)
	apply(t, b, w, "2026-10-02T01:00:00Z", t2, t1)
	ia, _ := agenda(t, a, octStart, octEnd)
	ib, _ := agenda(t, b, octStart, octEnd)
	if len(ia) != 2 || !reflect.DeepEqual(ia, ib) {
		t.Fatalf("a=%+v b=%+v", ia, ib)
	}
	for _, it := range ia {
		if !strings.HasSuffix(it.Key, "#"+it.SourceID) {
			t.Fatalf("every twin carries its source id, got %q", it.Key)
		}
	}
	// A twin appearing in a later sync gets the suffix; the first one keeps the bare hash it was given.
	c := openDB(t)
	apply(t, c, w, "2026-10-02T01:00:00Z", t1)
	apply(t, c, w, "2026-10-02T02:00:00Z", t1, t2)
	ic, _ := agenda(t, c, octStart, octEnd)
	if len(ic) != 2 {
		t.Fatalf("got %+v", ic)
	}
	// A twin the snapshot no longer lists still counts while its row is live, so the newcomer is suffixed.
	d := openDB(t)
	apply(t, d, w, "2026-10-02T01:00:00Z", t1)
	apply(t, d, w, "2026-10-02T02:00:00Z", t2)
	if n := count(t, d, `SELECT count(*) FROM calendar_source_events WHERE event_key LIKE '%#t2'`); n != 1 {
		t.Fatal("newcomer next to a live stored twin must carry its source id")
	}
}

func TestUpgradeBlockedByExistingKey(t *testing.T) {
	tw := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	// Teams already holds the global key under a different subject, so joining would collide.
	tg := timed(t, SourceTeams, "tg", "B", "2026-10-05T16:00:00Z")
	tg.GlobalID = "uid-1"
	tc := timed(t, SourceTeams, "tc", "A", "2026-10-05T16:00:00Z")
	og := timed(t, SourceOutlook, "og", "A", "2026-10-05T16:00:00Z")
	og.GlobalID = "uid-1"
	for _, outlookFirst := range []bool{false, true} {
		db := openDB(t)
		if outlookFirst {
			apply(t, db, ow, "2026-10-02T01:00:00Z", og)
			apply(t, db, tw, "2026-10-02T02:00:00Z", tg, tc)
		} else {
			apply(t, db, tw, "2026-10-02T01:00:00Z", tg, tc)
			apply(t, db, ow, "2026-10-02T02:00:00Z", og)
		}
		if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE source='teams'`); n != 2 {
			t.Fatalf("outlookFirst=%v: teams rows = %d, a row was overwritten", outlookFirst, n)
		}
		if n := count(t, db, `SELECT count(*) FROM calendar_matches WHERE match_method IN ('upgraded','ambiguous')`); n != 0 {
			t.Fatalf("outlookFirst=%v: blocked upgrade was recorded", outlookFirst)
		}
	}
}

func TestEventRoundTrip(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	e := Event{
		Source: SourceTeams, SourceID: "t1", GlobalID: "uid", OriginalStart: tp(t, "2026-10-05T16:00:00Z"),
		Start: mustTime(t, "2026-10-06T16:00:00Z"), End: mustTime(t, "2026-10-06T17:00:00Z"), TimeZone: "Pacific Standard Time",
		Subject: "S", Organizer: "o", AttendeesJSON: `["a"]`, Location: "L", OnlineMeetingURL: "u", TeamsThreadID: "th", SeriesKey: "sk",
		Cancelled: true, Response: "accepted", ShowAs: "busy", BodyPreview: "bp", LastModified: tp(t, "2026-10-01T00:00:00Z"),
	}
	apply(t, db, w, "2026-10-02T01:00:00Z", e)
	items, _ := agenda(t, db, octStart, octEnd)
	if len(items) != 1 || items[0].Start != e.Start.UTC() || items[0].End != e.End.UTC() {
		t.Fatalf("%+v", items)
	}
	got := items[0].Event
	if !got.OriginalStart.Equal(*e.OriginalStart) || !got.LastModified.Equal(*e.LastModified) {
		t.Fatalf("times: %+v", got)
	}
	got.OriginalStart, got.LastModified, e.OriginalStart, e.LastModified = nil, nil, nil, nil
	if got != e.withUTC() {
		t.Fatalf("got  %+v\nwant %+v", got, e)
	}
}

func (e Event) withUTC() Event {
	e.Start, e.End = e.Start.UTC(), e.End.UTC()
	return e
}

func exec(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestApplySnapshotErrors(t *testing.T) {
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	teams := timed(t, SourceTeams, "t1", "Sync", "2026-10-05T16:00:00Z")
	outlook := timed(t, SourceOutlook, "o1", "Sync", "2026-10-05T16:00:00Z")
	outlook.GlobalID = "uid"
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	raise := func(event, table string) string {
		return `CREATE TRIGGER boom BEFORE ` + event + ` ON ` + table + ` BEGIN SELECT RAISE(ABORT, 'boom'); END`
	}
	tests := []struct {
		name   string
		setup  func(t *testing.T, db *sql.DB) // runs before the failing call
		w      Window
		events []Event
	}{
		{"closed db", func(t *testing.T, db *sql.DB) { _ = db.Close() }, w, []Event{teams}},
		{"match lookup", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_matches`) }, w, []Event{teams}},
		{"own count", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }, w, []Event{teams}},
		{"own count global", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }, ow, []Event{outlook}},
		{"other rows composite", func(t *testing.T, db *sql.DB) {
			exec(t, db, `ALTER TABLE calendar_source_events RENAME COLUMN event_key TO ek`)
		}, w, []Event{teams}},
		{"other rows global", func(t *testing.T, db *sql.DB) {
			exec(t, db, `ALTER TABLE calendar_source_events RENAME COLUMN event_key TO ek`)
		}, ow, []Event{outlook}},
		{"match insert", func(t *testing.T, db *sql.DB) { exec(t, db, raise("INSERT", "calendar_matches")) }, w, []Event{teams}},
		{"upsert", func(t *testing.T, db *sql.DB) { exec(t, db, raise("INSERT", "calendar_source_events")) }, w, []Event{teams}},
		{"remove select", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }, w, nil},
		{"remove update", func(t *testing.T, db *sql.DB) {
			apply(t, db, w, "2026-10-02T01:00:00Z", teams)
			exec(t, db, raise("UPDATE", "calendar_source_events"))
		}, w, nil},
		{"window insert", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_sources`) }, w, nil},
		{"rekey events", func(t *testing.T, db *sql.DB) {
			apply(t, db, w, "2026-10-02T01:00:00Z", teams)
			exec(t, db, raise("UPDATE", "calendar_source_events"))
		}, ow, []Event{outlook}},
		{"rekey matches", func(t *testing.T, db *sql.DB) {
			apply(t, db, w, "2026-10-02T01:00:00Z", teams)
			exec(t, db, raise("UPDATE", "calendar_matches"))
		}, ow, []Event{outlook}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			tt.setup(t, db)
			if err := ApplySnapshot(ctx, db, tt.w, tt.events, time.Now()); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestApplySnapshotRollsBack(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	exec(t, db, `CREATE TRIGGER boom BEFORE INSERT ON calendar_sources BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	if err := ApplySnapshot(ctx, db, w, []Event{timed(t, SourceTeams, "t", "T", "2026-10-05T16:00:00Z")}, time.Now()); err == nil {
		t.Fatal("want error")
	}
	if n := count(t, db, `SELECT count(*) FROM calendar_source_events`); n != 0 {
		t.Fatal("failed snapshot left rows behind")
	}
}

func TestScanErrorsInResolve(t *testing.T) {
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	ow := window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z")
	orig := scanRow
	t.Cleanup(func() { scanRow = orig })
	scanRow = func(*sql.Rows, ...any) error { return errBoom }

	// A stored same-hash row of the same source reaches ownCount's scan.
	scanRow = orig
	db := openDB(t)
	apply(t, db, w, "2026-10-02T01:00:00Z", timed(t, SourceTeams, "t0", "Sync", "2026-10-05T16:00:00Z"))
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	if err := ApplySnapshot(ctx, db, w, []Event{timed(t, SourceTeams, "t1", "Sync", "2026-10-05T16:00:00Z")}, time.Now()); err == nil {
		t.Fatal("own count scan: want error")
	}

	// A stored row of the other source reaches otherRows' scan.
	scanRow = orig
	db2 := openDB(t)
	apply(t, db2, ow, "2026-10-02T01:00:00Z", timed(t, SourceOutlook, "o0", "Sync", "2026-10-05T16:00:00Z"))
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	if err := ApplySnapshot(ctx, db2, w, []Event{timed(t, SourceTeams, "t1", "Sync", "2026-10-05T16:00:00Z")}, time.Now()); err == nil {
		t.Fatal("other rows scan: want error")
	}
}

func TestAgendaErrors(t *testing.T) {
	from, to := mustTime(t, octStart), mustTime(t, octEnd)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	good := timed(t, SourceTeams, "t", "T", "2026-10-05T16:00:00Z")
	insertRaw := func(t *testing.T, db *sql.DB, col, val string) {
		apply(t, db, w, "2026-10-02T01:00:00Z", good)
		exec(t, db, `UPDATE calendar_source_events SET `+col+`=?`, val)
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
	}{
		{"closed db", func(t *testing.T, db *sql.DB) { _ = db.Close() }},
		{"events table", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_source_events`) }},
		{"sources table", func(t *testing.T, db *sql.DB) { exec(t, db, `DROP TABLE calendar_sources`) }},
		{"bad start", func(t *testing.T, db *sql.DB) { insertRaw(t, db, "start_at", "soon") }},
		{"bad original", func(t *testing.T, db *sql.DB) { insertRaw(t, db, "original_start", "soon") }},
		{"bad modified", func(t *testing.T, db *sql.DB) { insertRaw(t, db, "last_modified", "soon") }},
		{"bad flag", func(t *testing.T, db *sql.DB) { insertRaw(t, db, "all_day", "maybe") }},
		{"bad window", func(t *testing.T, db *sql.DB) {
			apply(t, db, w, "2026-10-02T01:00:00Z", good)
			exec(t, db, `UPDATE calendar_sources SET window_start='soon'`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openDB(t)
			tt.setup(t, db)
			if _, _, err := Agenda(ctx, db, from, to); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestRowsErrInjected(t *testing.T) {
	db := openDB(t)
	w := window(t, SourceTeams, octStart, octEnd, "2026-10-02T00:00:00Z")
	apply(t, db, w, "2026-10-02T01:00:00Z", timed(t, SourceTeams, "t", "T", "2026-10-05T16:00:00Z"))
	orig, origScan := rowsErr, scanRow
	t.Cleanup(func() { rowsErr = orig })
	rowsErr = func(*sql.Rows) error { return errBoom }
	if _, _, err := Agenda(ctx, db, mustTime(t, octStart), mustTime(t, octEnd)); err == nil {
		t.Fatal("agenda: want error")
	}
	if err := ApplySnapshot(ctx, db, w, nil, time.Now()); err == nil {
		t.Fatal("apply: want error")
	}
	other := timed(t, SourceOutlook, "o", "T", "2026-10-05T16:00:00Z")
	other.GlobalID = "g"
	if err := ApplySnapshot(ctx, db, window(t, SourceOutlook, octStart, octEnd, "2026-10-02T00:00:00Z"), []Event{other}, time.Now()); err == nil {
		t.Fatal("candidates: want error")
	}
	rowsErr = orig
	scanRow = func(*sql.Rows, ...any) error { return errBoom }
	t.Cleanup(func() { scanRow = origScan })
	if err := ApplySnapshot(ctx, db, w, nil, time.Now()); err == nil {
		t.Fatal("scan: want error")
	}
	scanRow = origScan
	// Agenda's source scan hits the hook after the events scan passes.
	n := 0
	rowsErr = func(r *sql.Rows) error {
		n++
		if n == 2 {
			return errBoom
		}
		return orig(r)
	}
	if _, _, err := Agenda(ctx, db, mustTime(t, octStart), mustTime(t, octEnd)); err == nil {
		t.Fatal("sources: want error")
	}
}

var errBoom = errString("boom")

type errString string

func (e errString) Error() string { return string(e) }
