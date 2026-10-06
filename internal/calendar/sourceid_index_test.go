package calendar

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The lookups by source id that applying an event and marking one gone make must be served by an
// index on (source, account, source id). Without it each lookup scans the account's rows and the
// first derivation of an archive grows with the square of its events (2,500 events took 1.4
// seconds, 10,000 took 19 and 20,000 took 95).
func TestSourceIDLookupsUseTheIndex(t *testing.T) {
	db := linked(t)
	for name, q := range map[string]string{"storedKey": storedKeySQL, "markGone": markGoneSQL} {
		args := []any{"2026-11-02T00:00:00.000Z", "teams", acctTeams, "x", EventMaster}
		if name == "storedKey" {
			args = []any{"teams", acctTeams, "x"}
		}
		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		if joined := strings.Join(plan, "; "); !strings.Contains(joined, "calendar_source_events_source_id") {
			t.Errorf("%s does not use the source id index: %s", name, joined)
		}
	}
}

// BenchmarkApplyBatchByEvents applies one batch of n events; the time per event stays flat when the
// source id lookups are indexed. Run with -bench ApplyBatchByEvents and compare the ns/op of the
// sizes: a growing figure is the quadratic regression.
func BenchmarkApplyBatchByEvents(b *testing.B) {
	for _, n := range []int{1000, 4000, 16000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
			events := make([]Event, n)
			for i := range events {
				s := start.Add(time.Duration(i) * time.Minute)
				events[i] = Event{UnknownDeclared: true, Source: SourceTeams, AccountID: acctTeams, SourceID: fmt.Sprintf("e%06d", i),
					GlobalID: fmt.Sprintf("u%06d", i), ICalUID: fmt.Sprintf("u%06d", i), EventType: EventSingle, Subject: "s", Start: s, End: s.Add(time.Hour),
					AllDay: TriFalse}
			}
			w := Window{Source: SourceTeams, AccountID: acctTeams, Start: start, End: start.Add(time.Duration(n+60) * time.Minute), SyncedAt: start, CacheFreshAt: start}
			for b.Loop() {
				b.StopTimer()
				db := linked(b)
				b.StartTimer()
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := ApplyBatch(ctx, tx, Batch{Window: w, Events: events}, ApplyOptions{SkipMatches: true}, start); err != nil {
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
