package calendar

import (
	"testing"
	"time"
)

func occurrence(t testing.TB, src Source, lm string, mods ...func(*Event)) Event {
	return twin(t, src, lm, append([]func(*Event){func(e *Event) {
		e.OriginalStart = tp(t, "2026-11-03T00:00:00Z")
		e.Start, e.End = mustTime(t, "2026-11-03T08:00:00Z"), mustTime(t, "2026-11-04T08:00:00Z")
	}}, mods...)...)
}

func TestKeyDateFormWhenAllDayIsKnownOrLooksAllDay(t *testing.T) {
	known := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AllDay, e.StartDate = TriTrue, "2026-11-03" })
	unknown := occurrence(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) { e.AllDay = TriUnknown })
	if Key(known) != "uid-1|2026-11-03" || Key(unknown) != Key(known) {
		t.Fatalf("an unknown flag next to all-day instants keys by date: %q %q", Key(known), Key(unknown))
	}
	// An unknown flag next to a one-hour event is timed, and a known false is timed whatever it looks like.
	short := occurrence(t, SourceOutlook, "2026-11-02T09:00:00Z", func(e *Event) {
		e.AllDay, e.End = TriUnknown, e.Start.Add(time.Hour)
	})
	notAllDay := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AllDay = TriFalse })
	for _, e := range []Event{short, notAllDay} {
		if got := Key(e); got != "uid-1|2026-11-03T00:00:00Z" {
			t.Fatalf("timed key: %q", got)
		}
	}
}

func TestKeyNeverMovesWhenFlagGoesAbsent(t *testing.T) {
	for _, path := range []string{"matches", "skip"} {
		t.Run(path, func(t *testing.T) {
			db := openDB(t)
			stated := occurrence(t, SourceTeams, "2026-11-02T09:00:00Z", func(e *Event) { e.AllDay, e.StartDate = TriTrue, "2026-11-03" })
			// The same occurrence later, the flag absent and the instants no longer all-day.
			absent := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
				e.AllDay = TriUnknown
				e.End = e.Start.Add(time.Hour)
			})
			if Key(stated) == Key(absent) {
				t.Fatal("the fixture must mint two keys")
			}
			for _, e := range []Event{stated, absent} {
				if path == "matches" {
					snap(t, db, SourceTeams, acctTeams, "2026-11-02T11:00:00Z", "2026-11-02T11:00:00Z", e)
					continue
				}
				w := Window{Source: SourceTeams, AccountID: acctTeams, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2026-12-01T00:00:00Z"),
					SyncedAt: mustTime(t, "2026-11-02T11:00:00Z"), CacheFreshAt: mustTime(t, "2026-11-02T11:00:00Z")}
				batch(t, db, Batch{Window: w, Events: []Event{e}}, "2026-11-02T11:00:00Z")
			}
			if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE source_id=?`, stated.SourceID); n != 1 {
				t.Fatalf("one occurrence, one row: %d", n)
			}
			if n := count(t, db, `SELECT count(*) FROM calendar_source_events WHERE event_key=?`, Key(stated)); n != 1 {
				t.Fatal("the row keeps the key it was first stored under")
			}
		})
	}
}

func TestAllDayOccurrenceMergesWithTwinWhoseFlagIsUnknown(t *testing.T) {
	db := linked(t)
	teams := occurrence(t, SourceTeams, "2026-11-02T12:00:00Z", func(e *Event) { e.AllDay, e.StartDate, e.EndDate = TriTrue, "2026-11-03", "2026-11-04" })
	outlook := occurrence(t, SourceOutlook, "2026-11-02T12:00:30Z", func(e *Event) {
		e.AllDay = TriUnknown
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T12:10:00Z", "2026-11-02T12:10:00Z", teams)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T12:10:00Z", "2026-11-02T12:10:00Z", outlook)
	res := day(t, db, AgendaQuery{From: mustTime(t, "2026-11-03T00:00:00Z"), To: mustTime(t, "2026-11-04T00:00:00Z")})
	if len(res.Items) != 1 || len(res.Items[0].Sources) != 2 {
		t.Fatalf("one item from both sources: %+v", res.Items)
	}
}

func TestLoadStoredReportsQueryError(t *testing.T) {
	db := openDB(t)
	exec(t, db, `DROP TABLE calendar_source_events`)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := loadStored(ctx, tx, thin(t, t1), "k"); err == nil {
		t.Fatal("a missing table is an error")
	}
}
