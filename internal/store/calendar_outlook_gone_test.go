package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookcal"
)

// goneEvent is a synthetic Outlook event with its own id, starting at start.
func goneEvent(id string, start time.Time, kind string) calendar.Event {
	e := outlookEvent(calendar.TriFalse, base)
	e.SourceID, e.GlobalID, e.ICalUID, e.SeriesKey = id, id, id, id
	e.EventType = kind
	e.Start, e.End = start, start.Add(30*time.Minute)
	return e
}

func commitGone(t *testing.T, s *Store, at time.Time, infer bool, events ...calendar.Event) CalendarResult {
	t.Helper()
	b := OutlookBatch{Account: "outlook/Main", Events: events, FreshAt: at, At: at, Zone: time.UTC, Stamp: OutlookStamp(outlookcal.MapperVersion, time.UTC), InferGone: infer}
	res, err := s.CommitOutlook(context.Background(), b, outlookRun)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func removedAt(t *testing.T, s *Store, id string) (removed bool, subject string) {
	t.Helper()
	var r *string
	if err := s.db.QueryRow(`select removed_at, subject from calendar_source_events where source='outlook' and source_id=?`, id).Scan(&r, &subject); err != nil {
		t.Fatal(err)
	}
	return r != nil, subject
}

// An event the store held and no longer holds is deleted: Outlook leaves no tombstone and drops the
// event's objects when it compacts (docs/outlook-store.md, "Deleted events"). One miss changes nothing
// visible; the second consecutive trusted miss, from a newer copy, marks it gone, keeping its data and
// counting it once. It comes back live when the store holds it again.
func TestOutlookEventAbsentFromTwoReadsIsGone(t *testing.T) {
	s := newStore(t)
	soon := base.Add(48 * time.Hour)
	keep, drop := goneEvent("KEEP", soon, calendar.EventSingle), goneEvent("DROP", soon.Add(time.Hour), calendar.EventSingle)
	commitGone(t, s, base, true, keep, drop)
	if res := commitGone(t, s, base.Add(time.Hour), true, keep); res.Counts.Gone != 0 {
		t.Fatalf("one miss marked %d events gone", res.Counts.Gone)
	}
	if gone, _ := removedAt(t, s, "DROP"); gone {
		t.Fatal("one miss marked the event gone")
	}
	res := commitGone(t, s, base.Add(2*time.Hour), true, keep)
	if res.Counts.Gone != 1 {
		t.Fatalf("gone count %d, want 1", res.Counts.Gone)
	}
	if gone, subj := removedAt(t, s, "DROP"); !gone || subj != "Fixture" {
		t.Fatalf("removed=%v subject=%q: the event must be marked gone and keep its data", gone, subj)
	}
	if gone, _ := removedAt(t, s, "KEEP"); gone {
		t.Fatal("an event the store still holds was marked gone")
	}
	if res = commitGone(t, s, base.Add(3*time.Hour), true, keep); res.Counts.Gone != 0 {
		t.Fatalf("an event already gone was counted again: %d", res.Counts.Gone)
	}
	commitGone(t, s, base.Add(4*time.Hour), true, keep, drop)
	if gone, _ := removedAt(t, s, "DROP"); gone {
		t.Fatal("an event the store holds again stayed gone")
	}
}

// A torn copy that hides a live event for one read must never mark it: miss then seen, miss then a damaged
// read then miss, and two misses from the same copy all leave the event live.
func TestOutlookOneMissIsNeverEnough(t *testing.T) {
	keep, drop := goneEvent("KEEP", base.Add(48*time.Hour), calendar.EventSingle), goneEvent("DROP", base.Add(49*time.Hour), calendar.EventSingle)
	live := func(t *testing.T, s *Store) {
		t.Helper()
		if gone, _ := removedAt(t, s, "DROP"); gone {
			t.Fatal("the event was marked gone")
		}
	}
	t.Run("miss then seen then miss", func(t *testing.T) {
		s := newStore(t)
		commitGone(t, s, base, true, keep, drop)
		commitGone(t, s, base.Add(time.Hour), true, keep)
		commitGone(t, s, base.Add(2*time.Hour), true, keep, drop)
		if res := commitGone(t, s, base.Add(3*time.Hour), true, keep); res.Counts.Gone != 0 {
			t.Fatalf("gone %d", res.Counts.Gone)
		}
		live(t, s)
	})
	t.Run("miss then damaged read then miss", func(t *testing.T) {
		s := newStore(t)
		commitGone(t, s, base, true, keep, drop)
		commitGone(t, s, base.Add(time.Hour), true, keep)
		commitGone(t, s, base.Add(2*time.Hour), false, keep)
		if res := commitGone(t, s, base.Add(3*time.Hour), true, keep); res.Counts.Gone != 0 {
			t.Fatalf("gone %d", res.Counts.Gone)
		}
		live(t, s)
	})
	t.Run("two misses from one copy", func(t *testing.T) {
		s := newStore(t)
		commitGone(t, s, base, true, keep, drop)
		at := base.Add(time.Hour)
		for range 2 {
			b := OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{keep}, FreshAt: at, At: at, Zone: time.UTC, Stamp: OutlookStamp(outlookcal.MapperVersion, time.UTC), InferGone: true}
			if _, err := s.CommitOutlook(context.Background(), b, outlookRun); err != nil {
				t.Fatal(err)
			}
		}
		live(t, s)
	})
	t.Run("a changed stamp keeps the memory", func(t *testing.T) {
		s := newStore(t)
		commitGone(t, s, base, true, keep, drop)
		commitGone(t, s, base.Add(time.Hour), true, keep)
		b := OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{keep}, FreshAt: base.Add(2 * time.Hour), At: base.Add(2 * time.Hour), Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC), InferGone: true}
		res, err := s.CommitOutlook(context.Background(), b, outlookRun)
		if err != nil || res.Counts.Gone != 1 {
			t.Fatalf("gone %d, err %v", res.Counts.Gone, err)
		}
	})
}

// The rule is not applied to a read that may have missed events (damaged blocks), nor to events that left
// the store's rolling window, nor to series masters, which stay in the store as long as the series lives.
func TestOutlookGoneDetectionLeavesWhatItCannotJudge(t *testing.T) {
	s := newStore(t)
	at := base.Add(24 * time.Hour)
	events := []calendar.Event{
		goneEvent("FUTURE", at.Add(48*time.Hour), calendar.EventSingle),
		goneEvent("EDGE", at.Add(-OutlookGoneHorizon+3*time.Hour), calendar.EventOccurrence),
		goneEvent("OLD", at.Add(-OutlookGoneHorizon-time.Hour), calendar.EventSingle),
		goneEvent("MASTER", at.Add(48*time.Hour), calendar.EventMaster),
		goneEvent("KEEP", at.Add(72*time.Hour), calendar.EventSingle),
	}
	commitGone(t, s, at, true, events...)

	commitGone(t, s, at.Add(time.Hour), false, events[4]) // not trusted: a damaged read
	commitGone(t, s, at.Add(90*time.Minute), true, events[4])
	for _, id := range []string{"FUTURE", "EDGE", "OLD", "MASTER"} {
		if gone, _ := removedAt(t, s, id); gone {
			t.Fatalf("%s marked gone by a read that is not trusted", id)
		}
	}
	res := commitGone(t, s, at.Add(2*time.Hour), true, events[4])
	if res.Counts.Gone != 2 {
		t.Fatalf("gone %d, want 2 (the future event and the one inside the horizon)", res.Counts.Gone)
	}
	for id, want := range map[string]bool{"FUTURE": true, "EDGE": true, "OLD": false, "MASTER": false, "KEEP": false} {
		if gone, _ := removedAt(t, s, id); gone != want {
			t.Fatalf("%s gone=%v, want %v", id, gone, want)
		}
	}
}

// An all-day event is placed by its stated date, not by the instant of its midnight.
func TestOutlookGoneHorizonUsesTheStatedDateOfAnAllDayEvent(t *testing.T) {
	s := newStore(t)
	at := base.Add(24 * time.Hour)
	inside := goneEvent("INSIDE", time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), calendar.EventSingle)
	outside := goneEvent("OUTSIDE", at.Add(-OutlookGoneHorizon-48*time.Hour).Truncate(24*time.Hour), calendar.EventSingle)
	for _, e := range []*calendar.Event{&inside, &outside} {
		e.AllDay = calendar.TriTrue
		e.End = e.Start.Add(24 * time.Hour)
		e.StartDate, e.EndDate = e.Start.Format(time.DateOnly), e.End.Format(time.DateOnly)
	}
	keep := goneEvent("KEEP", at.Add(72*time.Hour), calendar.EventSingle)
	commitGone(t, s, at, true, inside, outside, keep)
	commitGone(t, s, at.Add(time.Hour), true, keep)
	res := commitGone(t, s, at.Add(2*time.Hour), true, keep)
	if res.Counts.Gone != 1 {
		t.Fatalf("gone %d, want 1", res.Counts.Gone)
	}
	if g, _ := removedAt(t, s, "INSIDE"); !g {
		t.Fatal("the all-day event inside the horizon was kept")
	}
	if g, _ := removedAt(t, s, "OUTSIDE"); g {
		t.Fatal("the all-day event past the horizon was marked gone")
	}
}

// A read that drops a large share of the live events looks like a store that was reset or replaced, not
// like deletions: nothing is marked.
func TestOutlookGoneDetectionWithholdsAMassDisappearance(t *testing.T) {
	s := newStore(t)
	var events []calendar.Event
	for i := range 2 * OutlookGoneWithholdMin {
		events = append(events, goneEvent(fmt.Sprintf("E%03d", i), base.Add(time.Duration(48+i)*time.Hour), calendar.EventSingle))
	}
	commitGone(t, s, base, true, events...)
	res := commitGone(t, s, base.Add(time.Hour), true, events[:1]...)
	if res.Counts.Gone != 0 {
		t.Fatalf("a mass disappearance marked %d events gone", res.Counts.Gone)
	}
	// A handful of deletions in a large calendar is still detected.
	commitGone(t, s, base.Add(2*time.Hour), true, events[:len(events)-1]...)
	res = commitGone(t, s, base.Add(3*time.Hour), true, events[:len(events)-1]...)
	if res.Counts.Gone != 1 {
		t.Fatalf("gone %d, want the one deleted event", res.Counts.Gone)
	}
}

// Every storage step of the gone rule fails the commit whole, and a stored start no one can read is
// left alone: it cannot be placed against the horizon, so it is not judged.
func TestOutlookGoneFailuresRollBack(t *testing.T) {
	keep, drop, odd := goneEvent("KEEP", base.Add(48*time.Hour), calendar.EventSingle), goneEvent("DROP", base.Add(49*time.Hour), calendar.EventSingle), goneEvent("ODD", base.Add(50*time.Hour), calendar.EventSingle)
	sweepCalendar(t, func(t *testing.T, s *Store) {
		commitGone(t, s, base, true, keep, drop, odd)
		if _, err := s.db.Exec(`update calendar_source_events set start_at='not a time' where source_id='ODD'`); err != nil {
			t.Fatal(err)
		}
	}, func(ctx context.Context, s *Store) error {
		b := OutlookBatch{Account: "outlook/Main", Events: []calendar.Event{keep}, FreshAt: base, At: base.Add(time.Hour), Zone: time.UTC, Stamp: OutlookStamp(outlookcal.MapperVersion, time.UTC), InferGone: true}
		_, err := s.CommitOutlook(ctx, b, outlookRun)
		return err
	})
	s := newStore(t)
	commitGone(t, s, base, true, keep, odd)
	if _, err := s.db.Exec(`update calendar_source_events set start_at='not a time' where source_id='ODD'`); err != nil {
		t.Fatal(err)
	}
	if res := commitGone(t, s, base.Add(time.Hour), true, keep); res.Counts.Gone != 0 {
		t.Fatalf("an event whose stored start cannot be read was judged: gone %d", res.Counts.Gone)
	}
}
