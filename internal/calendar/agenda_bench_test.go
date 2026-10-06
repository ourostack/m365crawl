package calendar

import (
	"fmt"
	"testing"
)

// BenchmarkAgendaWeekOverRecurringSeries is a one-week agenda over 200 weekly series of 250
// occurrences, each held by Teams and Outlook (100,000 rows): the join must not fetch every key of
// each candidate's series. Run with: go test ./internal/calendar -run xxx -bench Series
func BenchmarkAgendaWeekOverRecurringSeries(b *testing.B) {
	db := linked(b)
	// Seed by SQL: 200 series x 250 weekly occurrences x two sources.
	for _, src := range []Source{SourceTeams, SourceOutlook} {
		exec(b, db, fmt.Sprintf(`
WITH RECURSIVE s(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM s WHERE n<199),
 o(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM o WHERE n<249),
 e AS (SELECT s.n AS sn, o.n AS on_, datetime('2025-01-06 09:00:00', '+'||(o.n*7)||' days', '+'||(s.n%%8)||' hours') AS st FROM s, o)
INSERT INTO calendar_source_events (source, account_id, event_key, composite_key, source_id, global_id, original_start, start_at, end_at, all_day, subject, last_modified, first_seen_at, seen_at)
SELECT '%s', '%s', 'uid-'||sn||'|'||strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', st), 'composite|'||sn||'-'||on_, '%s-'||sn||'-'||on_, 'uid-'||sn,
 strftime('%%Y-%%m-%%dT%%H:%%M:%%f', st)||'Z', strftime('%%Y-%%m-%%dT%%H:%%M:%%f', st)||'Z', strftime('%%Y-%%m-%%dT%%H:%%M:%%f', st, '+1 hour')||'Z', 0, 'Series '||sn,
 '2026-11-02T09:00:00.000Z', '2026-11-02T09:00:00.000Z', '2026-11-02T09:00:00.000Z' FROM e`, src, acctOf(src), src))
	}
	q := AgendaQuery{From: mustTime(b, "2026-11-02T00:00:00Z"), To: mustTime(b, "2026-11-09T00:00:00Z")}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := Agenda(ctx, db, q)
		if err != nil || len(res.Items) == 0 {
			b.Fatal(err, len(res.Items))
		}
	}
}
