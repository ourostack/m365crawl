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
	if _, ok := occurrenceDate(mustTime(t, "2026-11-02T12:00:00Z"), []Event{{}}, []string{"2026-11-02", "2026-11-03"}); ok {
		t.Error("an instant equally near two dates joins neither")
	}
	la := Event{TimeZoneIANA: "America/Los_Angeles"}
	for name, c := range map[string]struct {
		rows  []Event
		cands []string
		want  string
		ok    bool
	}{
		"iana zone moves the date back":   {[]Event{la}, []string{"2026-11-02", "2026-11-03"}, "2026-11-02", true},
		"iana zone, no such date key":     {[]Event{la}, []string{"2026-11-03"}, "", false},
		"bad iana falls to the offset":    {[]Event{{TimeZoneIANA: "Nowhere/Land", UTCOffset: "-07:00"}}, []string{"2026-11-02"}, "2026-11-02", true},
		"offset only":                     {[]Event{{UTCOffset: "+14:00"}}, []string{"2026-11-03"}, "2026-11-03", true},
		"unknown zone, nearest date":      {[]Event{{}}, []string{"2026-11-02", "2026-11-03"}, "2026-11-03", true},
		"unknown zone, equally near":      {[]Event{{}}, []string{"2026-11-02", "2026-11-04"}, "", false},
		"unknown zone, beyond the window": {[]Event{{}}, []string{"2026-11-01", "2026-11-05"}, "", false},
		"unreadable offset is no zone":    {[]Event{{UTCOffset: "later"}}, []string{"2026-11-03"}, "2026-11-03", true},
	} {
		got, ok := occurrenceDate(at, c.rows, c.cands)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %q %v", name, got, ok)
		}
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
