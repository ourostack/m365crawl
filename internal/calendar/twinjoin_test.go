package calendar

import (
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// applyBy stores events for one account through one of the two apply paths.
func applyBy(t *testing.T, path string, db *sql.DB, src Source, account, at string, events ...Event) {
	t.Helper()
	if path == "matches" {
		snap(t, db, src, account, at, at, events...)
		return
	}
	w := Window{Source: src, AccountID: account, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2026-12-01T00:00:00Z"),
		SyncedAt: mustTime(t, at), CacheFreshAt: mustTime(t, at)}
	batch(t, db, Batch{Window: w, Events: events}, at)
}

func novRange(t *testing.T, db *sql.DB) []AgendaItem {
	return day(t, db, AgendaQuery{From: mustTime(t, "2026-11-02T00:00:00Z"), To: mustTime(t, "2026-11-06T00:00:00Z")}).Items
}

func TestTwinsJoinWhenATimedKeyBecameAllDay(t *testing.T) {
	// Teams stored the occurrence as a 24-hour timed event, then it became all-day: its key stays
	// timed. Outlook's twin has an unknown flag and mints the date key.
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
	if Key(timed) == Key(outlook) {
		t.Fatal("the fixture must mint two keys")
	}
	type step struct {
		src     Source
		account string
		at      string
		e       Event
	}
	teams1 := step{SourceTeams, acctTeams, "2026-11-02T09:10:00Z", timed}
	teams2 := step{SourceTeams, acctTeams, "2026-11-02T10:10:00Z", allDay}
	out := step{SourceOutlook, acctOutlook, "2026-11-02T10:10:30Z", outlook}
	orders := map[string][]step{
		"teams then outlook": {teams1, teams2, out},
		"outlook first":      {out, teams1, teams2},
		"outlook between":    {teams1, out, teams2},
	}
	for _, path := range []string{"matches", "skip"} {
		for name, steps := range orders {
			t.Run(path+"/"+name, func(t *testing.T) {
				db := linked(t)
				for _, s := range steps {
					applyBy(t, path, db, s.src, s.account, s.at, s.e)
				}
				if n := count(t, db, `SELECT count(DISTINCT event_key) FROM calendar_source_events`); n != 2 {
					t.Fatalf("keys never move, so two stored keys remain: %d", n)
				}
				items := novRange(t, db)
				if len(items) != 1 || len(items[0].Sources) != 2 {
					t.Fatalf("one item from both sources: %d %+v", len(items), items)
				}
			})
		}
	}
}

func TestAdjacentDayOccurrencesNeverJoin(t *testing.T) {
	// Outlook's occurrence on the 3rd (date key) and Teams' different occurrence on the 4th,
	// timed at midnight and at 08:00 UTC, with the same global id.
	outlook := occurrence(t, SourceOutlook, "2026-11-02T10:00:30Z", func(e *Event) {
		e.AllDay = TriUnknown
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	for i, at := range []string{"2026-11-04T00:00:00Z", "2026-11-04T08:00:00Z"} {
		teams := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
			e.SourceID = fmt.Sprintf("teams-%d", i)
			e.OriginalStart = tp(t, at)
			e.Start = mustTime(t, at)
			e.End = e.Start.Add(24 * time.Hour)
		})
		db := linked(t)
		snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T10:10:00Z", "2026-11-02T10:10:00Z", outlook)
		snap(t, db, SourceTeams, acctTeams, "2026-11-02T10:10:00Z", "2026-11-02T10:10:00Z", teams)
		if items := novRange(t, db); len(items) != 2 {
			t.Fatalf("%s: two different occurrences stay two items: %d", at, len(items))
		}
	}
}

func TestOccurrenceDate(t *testing.T) {
	at := mustTime(t, "2026-11-03T03:00:00Z")
	la := Event{TimeZoneIANA: "America/Los_Angeles"}
	for name, c := range map[string]struct {
		at    string
		rows  []Event
		cands []string
		want  string
		dist  time.Duration
		ok    bool
	}{
		"iana zone moves the date back":   {"", []Event{la}, []string{"2026-11-02", "2026-11-03"}, "2026-11-02", 19 * time.Hour, true},
		"iana zone, no such date key":     {"", []Event{la}, []string{"2026-11-03"}, "", 0, false},
		"bad iana falls to the offset":    {"", []Event{{TimeZoneIANA: "Nowhere/Land", UTCOffset: "-07:00"}}, []string{"2026-11-02"}, "2026-11-02", 20 * time.Hour, true},
		"offset only":                     {"", []Event{{UTCOffset: "+14:00"}}, []string{"2026-11-03"}, "2026-11-03", 17 * time.Hour, true},
		"no zone, instant in the UTC day": {"", []Event{{}}, []string{"2026-11-02", "2026-11-03"}, "2026-11-03", 3 * time.Hour, true},
		"no zone, the evening before":     {"2026-11-02T20:00:00Z", []Event{{}}, []string{"2026-11-03"}, "", 0, false},
		"no zone, Teams west of UTC":      {"2026-11-03T08:00:00Z", []Event{{}}, []string{"2026-11-03"}, "2026-11-03", 8 * time.Hour, true},
		"unreadable offset is no zone":    {"", []Event{{UTCOffset: "later"}}, []string{"2026-11-03"}, "2026-11-03", 3 * time.Hour, true},
	} {
		when := at
		if c.at != "" {
			when = mustTime(t, c.at)
		}
		got, dist, ok := occurrenceDate(when, c.rows, c.cands)
		if got != c.want || ok != c.ok || (ok && dist != c.dist) {
			t.Errorf("%s: %q %v %v", name, got, dist, ok)
		}
	}
}

func TestDailySeriesEveningDoesNotJoinNextDayDateKey(t *testing.T) {
	// A daily series: the 2nd's occurrence timed at 20:00Z, and the 3rd's all-day twin by date.
	for _, path := range []string{"matches", "skip"} {
		db := linked(t)
		evening := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
			e.OriginalStart = tp(t, "2026-11-02T20:00:00Z")
			e.Start = mustTime(t, "2026-11-02T20:00:00Z")
			e.End = e.Start.Add(time.Hour)
		})
		next := occurrence(t, SourceOutlook, "2026-11-02T10:00:30Z", func(e *Event) {
			e.AllDay = TriUnknown
			e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
		})
		applyBy(t, path, db, SourceTeams, acctTeams, "2026-11-02T10:10:00Z", evening)
		applyBy(t, path, db, SourceOutlook, acctOutlook, "2026-11-02T10:10:00Z", next)
		if items := novRange(t, db); len(items) != 2 {
			t.Fatalf("%s: two occurrences of a daily series: %d", path, len(items))
		}
	}
}

func TestAtMostOneRowPerSourceInAJoinedGroup(t *testing.T) {
	db := linked(t)
	// Two timed Teams occurrences on one UTC day and an Outlook date key for that day.
	near := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
		e.SourceID = "teams-near"
		e.OriginalStart = tp(t, "2026-11-03T08:00:00Z")
		e.End = e.Start.Add(24 * time.Hour)
	})
	far := occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
		e.SourceID = "teams-far"
		e.OriginalStart = tp(t, "2026-11-03T20:00:00Z")
		e.Start = mustTime(t, "2026-11-03T20:00:00Z")
		e.End = e.Start.Add(time.Hour)
	})
	outlook := occurrence(t, SourceOutlook, "2026-11-02T10:00:30Z", func(e *Event) {
		e.AllDay = TriUnknown
		e.Start, e.End = mustTime(t, "2026-11-03T00:00:00Z"), mustTime(t, "2026-11-04T00:00:00Z")
	})
	snap(t, db, SourceTeams, acctTeams, "2026-11-02T10:10:00Z", "2026-11-02T10:10:00Z", near, far)
	snap(t, db, SourceOutlook, acctOutlook, "2026-11-02T10:10:00Z", "2026-11-02T10:10:00Z", outlook)
	items := novRange(t, db)
	if len(items) != 2 {
		t.Fatalf("the nearest Teams key joins Outlook, the other stays alone: %d", len(items))
	}
	for _, it := range items {
		if len(it.Sources) > 2 {
			t.Fatalf("one row per source: %+v", it.Sources)
		}
	}
	// A date group that already holds a row of the source takes no timed key of it.
	groups := map[groupKey][]Event{
		{"p", "uid|2026-11-03"}:           {{Source: SourceTeams}},
		{"p", "uid|2026-11-03T08:00:00Z"}: {{Source: SourceTeams}},
	}
	joinOccurrences(groups)
	if len(groups) != 2 {
		t.Fatalf("a second Teams row never joins: %d", len(groups))
	}
}

func TestSiblingsAreBoundedToTheRange(t *testing.T) {
	db := linked(t)
	var evs []Event
	for d := 0; d < 120; d++ {
		start := mustTime(t, "2026-11-01T09:00:00Z").AddDate(0, 0, d)
		evs = append(evs, occurrence(t, SourceTeams, "2026-11-02T10:00:00Z", func(e *Event) {
			e.SourceID = fmt.Sprintf("s-%d", d)
			e.OriginalStart = &start
			e.Start, e.End = start, start.Add(time.Hour)
		}))
	}
	w := Window{Source: SourceTeams, AccountID: acctTeams, Start: mustTime(t, "2026-11-01T00:00:00Z"), End: mustTime(t, "2027-03-01T00:00:00Z"),
		SyncedAt: mustTime(t, "2026-11-02T11:00:00Z"), CacheFreshAt: mustTime(t, "2026-11-02T11:00:00Z")}
	batch(t, db, Batch{Window: w, Events: evs}, "2026-11-02T11:00:00Z")
	groups, err := loadGroups(ctx, db, Principals{}, nil, mustTime(t, "2026-11-10T00:00:00Z"), mustTime(t, "2026-11-11T00:00:00Z"), "2026-11-10", "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	// The day itself, plus its neighbours that a join could need; never the other 100 occurrences.
	if len(groups) < 1 || len(groups) > 5 {
		t.Fatalf("a long series loads a few keys, not all of them: %d", len(groups))
	}
}

func TestSplitKeyAndDateSuffix(t *testing.T) {
	for _, k := range []string{"composite|abc", "nokey"} {
		if _, _, ok := splitKey(k); ok {
			t.Errorf("%q has no global id", k)
		}
	}
	if g, s, ok := splitKey("uid|2026-11-03"); !ok || g != "uid" || s != "2026-11-03" {
		t.Fatal(g, s, ok)
	}
	if !isDateSuffix("2026-11-03") || isDateSuffix("2026-11-03T08:00:00Z") || isDateSuffix("") {
		t.Fatal("date suffix")
	}
}

func TestJoinOccurrencesIgnoresKeysItDidNotMint(t *testing.T) {
	groups := map[groupKey][]Event{
		{"p", "uid|2026-11-03"}:           {{}},
		{"p", "uid|not-a-time"}:           {{}},
		{"p", "composite|abc"}:            {{}},
		{"p", "single"}:                   {{}},
		{"p", "uid|"}:                     {{}},
		{"q", "uid|2026-11-03T08:00:00Z"}: {{}}, // another principal never joins
	}
	joinOccurrences(groups)
	if len(groups) != 6 {
		t.Fatalf("nothing joins: %d", len(groups))
	}
}

func TestJoinTiesOnDistanceAreOrderedByKey(t *testing.T) {
	// Two timed keys of different sources, both eight hours from the date's local midnight.
	groups := map[groupKey][]Event{
		{"p", "uid|2026-11-03"}:           {{Source: SourceOutlook}},
		{"p", "uid|2026-11-03T08:00:00Z"}: {{Source: SourceTeams}},
		{"p", "uid|2026-11-03T03:00:00Z"}: {{Source: SourceTeams, UTCOffset: "+05:00"}},
	}
	joinOccurrences(groups)
	// One Teams key joins (the earlier key by order), the other stays alone.
	if len(groups) != 2 || len(groups[groupKey{"p", "uid|2026-11-03"}]) != 2 {
		t.Fatalf("%d groups, %d in the joined one", len(groups), len(groups[groupKey{"p", "uid|2026-11-03"}]))
	}
}
