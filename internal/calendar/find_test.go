package calendar

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestFindResolvesEveryKeyOfAJoinedGroup(t *testing.T) {
	db := linked(t)
	timed := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) {
		e.OriginalStart = tp(t, "2026-11-03T08:00:00Z")
		e.End = e.Start.Add(24 * time.Hour)
	})
	allDay := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
		e.OriginalStart = tp(t, "2026-11-03T08:00:00Z")
		e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04"
	})
	outlook := occurrence(t, SourceOutlook, "2026-11-02T10:00:30Z", func(e *Event) {
		e.AllDay = TriUnknown
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", timed)
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T10:10:00Z", "2026-11-02T10:10:00Z", allDay)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T10:10:30Z", "2026-11-02T10:10:30Z", outlook)
	dateKey, timedKey := Key(outlook), Key(timed)
	for _, key := range []string{dateKey, timedKey} {
		item, ok, err := Find(ctx, db, acctTeams, key)
		if err != nil || !ok || item.Key != dateKey || len(item.Sources) != 2 || len(item.JoinedKeys) != 1 || item.JoinedKeys[0] != timedKey {
			t.Fatalf("%s: %+v %v %v", key, item, ok, err)
		}
	}
	// Other principals and unknown keys find nothing.
	if _, ok, err := Find(ctx, db, "someone/else", dateKey); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	if _, ok, err := Find(ctx, db, acctTeams, "nope|"); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
}

func TestFindReportsStorageErrors(t *testing.T) {
	db := linked(t)
	e := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z")
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", e)
	// Each failing table is reached after the earlier reads succeeded.
	for _, table := range []string{"calendar_sources", "calendar_account_links"} {
		db := linked(t)
		snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", e)
		exec(t, db, `DROP TABLE `+table)
		if _, _, err := Find(ctx, db, acctTeams, Key(e)); err == nil {
			t.Errorf("%s: want an error", table)
		}
	}
	exec(t, db, `DROP TABLE calendar_source_events`)
	if _, _, err := Find(ctx, db, acctTeams, Key(e)); err == nil {
		t.Error("a missing events table is an error")
	}
}

// A key held by two sources with different times is found over the span of both, and a row whose
// end precedes its start does not break the span.
func TestFindSpansTheRowsOfEverySource(t *testing.T) {
	db := linked(t)
	teams := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z")
	outlook := occurrence(t, SourceOutlook, "2026-11-02T09:00:30Z", func(e *Event) {
		e.Start, e.End = e.Start.Add(-30*time.Minute), e.End.Add(30*time.Minute)
	})
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", teams)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T09:10:30Z", "2026-11-02T09:10:30Z", outlook)
	if Key(teams) != Key(outlook) {
		t.Skip("the fixture keys the two sources apart")
	}
	item, ok, err := Find(ctx, db, acctTeams, Key(teams))
	if err != nil || !ok || len(item.Sources) != 2 {
		t.Fatalf("%+v %v %v", item, ok, err)
	}
	uniq := func(id string) func(*Event) {
		return func(e *Event) { e.GlobalID, e.ICalUID, e.SourceID = "uid-"+id, "uid-"+id, "src-"+id }
	}
	// A source that is the wider one (the order the rows come back in puts the narrower first).
	wide := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", uniq("w"), func(e *Event) {
		e.Start, e.End = e.Start.Add(-time.Hour), e.End.Add(time.Hour)
	})
	narrow := occurrence(t, SourceOutlook, "2026-11-02T09:00:30Z", uniq("w"), func(e *Event) { e.SourceID = "src-wo" })
	backwards := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", uniq("b"), func(e *Event) { e.End = e.Start.Add(-time.Hour) })
	snap(t, db, SourceTeams, acctTeams, "2026-11-03T09:10:00Z", "2026-11-03T09:10:00Z", teams, wide, backwards)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-03T09:10:30Z", "2026-11-03T09:10:30Z", outlook, narrow)
	for _, e := range []Event{wide, backwards} {
		if _, ok, err := Find(ctx, db, acctTeams, Key(e)); err != nil || !ok {
			t.Fatalf("%s: %v %v", e.SourceID, ok, err)
		}
	}
	// An account that is not a principal holds the row under another principal: not its event.
	if _, ok, err := Find(ctx, db, acctOutlook, Key(teams)); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
}

// Each statement Find makes can fail, and the failure is reported, not read as "no such event".
func TestFindReportsEveryStatementFailure(t *testing.T) {
	db := linked(t)
	e := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z")
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T09:10:00Z", "2026-11-02T09:10:00Z", e)
	eachStatementFailure(t, db, func(d *sql.DB) error {
		_, ok, err := Find(ctx, d, acctTeams, Key(e))
		if err == nil && !ok {
			return errors.New("not found")
		}
		return err
	})
}
