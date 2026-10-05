package calendar

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// AgendaQuery selects the events overlapping [From, To). All-day events are compared by date in
// From's zone: the range covers the dates of From through the last instant before To.
type AgendaQuery struct {
	// AccountID limits the agenda to one account; empty means every account (events of different
	// accounts are never merged with each other).
	AccountID string
	From, To  time.Time
	// IncludeCancelled and IncludeDeclined show events the default agenda hides. IncludeMasters
	// shows recurring masters, which are otherwise never listed.
	IncludeCancelled, IncludeDeclined, IncludeMasters bool
	// Query keeps events whose subject, organizer or location contains it, ignoring case.
	Query string
}

// AgendaResult is an agenda and what the archive can say about its completeness.
type AgendaResult struct {
	Items []AgendaItem
	// Gap is true when some day of the range is not covered by any source: the archive cannot tell
	// "empty day" from "day never loaded", so an absent event is not evidence.
	Gap bool
	// AsOf is the oldest verification time among the sources and covered days that cover the range;
	// zero when nothing covers it.
	AsOf time.Time
}

// AgendaItem is one merged event and the key it is stored under.
type AgendaItem struct {
	Event
	Key string
}

// Agenda returns the merged events overlapping the query's range, sorted by start. Events are
// merged across sources by key within an account. Cancelled, declined and master events are left
// out unless the query asks for them.
func Agenda(ctx context.Context, db *sql.DB, q AgendaQuery) (AgendaResult, error) {
	var res AgendaResult
	fromDate := q.From.Format(dateLayout)
	toDate := q.To.Add(-time.Nanosecond).In(q.From.Location()).Format(dateLayout)
	rows, err := loadEvents(ctx, db, q.AccountID, q.From, q.To, fromDate, toDate)
	if err != nil {
		return res, err
	}
	windows, err := loadWindows(ctx, db, q.AccountID)
	if err != nil {
		return res, err
	}
	days, err := loadCoveredDays(ctx, db, q.AccountID)
	if err != nil {
		return res, err
	}
	if !q.To.After(q.From) {
		return res, nil
	}
	res.Gap, res.AsOf = coverage(windows, days, q.From, q.To)
	fresh := map[string]map[Source]time.Time{}
	for _, w := range windows {
		if fresh[w.AccountID] == nil {
			fresh[w.AccountID] = map[Source]time.Time{}
		}
		fresh[w.AccountID][w.Source] = w.CacheFreshAt
	}
	type groupKey struct{ account, key string }
	groups := map[groupKey][]Event{}
	for _, r := range rows {
		if overlaps(r.Event, q.From, q.To, fromDate, toDate) {
			k := groupKey{r.AccountID, r.key}
			groups[k] = append(groups[k], r.Event)
		}
	}
	needle := strings.ToLower(q.Query)
	for k, g := range groups {
		m := Merge(g, fresh[k.account])
		if !visible(m, q, needle) {
			continue
		}
		res.Items = append(res.Items, AgendaItem{Event: m, Key: k.key})
	}
	sort.Slice(res.Items, func(i, j int) bool {
		a, b := res.Items[i], res.Items[j]
		if sa, sb := sortStart(a.Event, q.From), sortStart(b.Event, q.From); !sa.Equal(sb) {
			return sa.Before(sb)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.AccountID < b.AccountID
	})
	return res, nil
}

// visible applies the query's filters to a merged event.
func visible(e Event, q AgendaQuery, needle string) bool {
	switch {
	case e.EventType == EventMaster && !q.IncludeMasters:
		return false
	case e.Cancelled && !q.IncludeCancelled:
		return false
	case e.Response == "declined" && !q.IncludeDeclined:
		return false
	case needle == "":
		return true
	}
	for _, s := range []string{e.Subject, e.Organizer, e.Location} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

// overlaps reports whether e touches [from, to).
func overlaps(e Event, from, to time.Time, fromDate, toDate string) bool {
	if e.AllDay {
		return e.StartDate <= toDate && (e.EndDate > fromDate || e.StartDate >= fromDate)
	}
	return e.Start.Before(to) && (e.End.After(from) || e.Start.Equal(from))
}

// sortStart is the instant an event sorts by: its start, or midnight of its date in loc's zone.
func sortStart(e Event, ref time.Time) time.Time {
	if e.AllDay {
		t, _ := time.ParseInLocation(dateLayout, e.StartDate, ref.Location())
		return t
	}
	return e.Start
}

// coveredDay is one verified day of a source.
type coveredDay struct {
	Source          Source
	AccountID, Day  string
	LastVerifiedAt  time.Time
	FirstVerifiedAt time.Time
}

// coverage reports whether any part of [from, to) is uncovered, and the oldest verification time
// among what covers it. Coverage is keyed by (source, account) and judged per account. Within an
// account, a source with covered-day rows covers exactly those days (dates in from's zone) and a
// source without any covers its whole window; sources of one account complement each other. A day
// is a gap when any account in scope does not cover it, so an all-accounts query never hides an
// account that skipped the day. With nothing in scope every day is a gap.
func coverage(windows []Window, days []coveredDay, from, to time.Time) (gap bool, asOf time.Time) {
	type srcKey struct {
		source  Source
		account string
	}
	type accountCover struct {
		verified map[string]time.Time // date -> newest verification across the account's sources
		spans    []Window             // windows of the account's sources that have no covered days
	}
	withDays := map[srcKey]bool{}
	accounts := map[string]*accountCover{}
	of := func(account string) *accountCover {
		if accounts[account] == nil {
			accounts[account] = &accountCover{verified: map[string]time.Time{}}
		}
		return accounts[account]
	}
	for _, d := range days {
		withDays[srcKey{d.Source, d.AccountID}] = true
		c := of(d.AccountID)
		if d.LastVerifiedAt.After(c.verified[d.Day]) {
			c.verified[d.Day] = d.LastVerifiedAt
		}
	}
	for _, w := range windows {
		c := of(w.AccountID)
		if !withDays[srcKey{w.Source, w.AccountID}] {
			c.spans = append(c.spans, w)
		}
	}
	if len(accounts) == 0 {
		return true, asOf
	}
	note := func(t time.Time) {
		if asOf.IsZero() || t.Before(asOf) {
			asOf = t
		}
	}
	loc := from.Location()
	y, m, d := from.Date()
	usedSpan := map[string]bool{}
	for start := time.Date(y, m, d, 0, 0, 0, 0, loc); start.Before(to); {
		next := time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, loc)
		pieceFrom, pieceTo := start, next
		if pieceFrom.Before(from) {
			pieceFrom = from
		}
		if pieceTo.After(to) {
			pieceTo = to
		}
		for name, c := range accounts {
			switch t, ok := c.verified[start.Format(dateLayout)]; {
			case ok:
				note(t)
			case coveredBySpans(c.spans, pieceFrom, pieceTo):
				usedSpan[name] = true
			default:
				gap = true
			}
		}
		start = next
	}
	for name := range usedSpan {
		for _, w := range accounts[name].spans {
			if w.Start.Before(to) && w.End.After(from) {
				note(w.SyncedAt)
			}
		}
	}
	return gap, asOf
}

// coveredBySpans reports whether the union of the windows contains all of [from, to).
func coveredBySpans(windows []Window, from, to time.Time) bool {
	ordered := append([]Window(nil), windows...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })
	cur := from
	for _, w := range ordered {
		if w.Start.After(cur) {
			break
		}
		if w.End.After(cur) {
			cur = w.End
		}
	}
	return !cur.Before(to)
}

// eventQuery is one SELECT of loadEvents with its bound arguments.
type eventQuery struct {
	sql  string
	args []any
}

// The exact shapes the package writes: formatTime (timeLayout) and dateLayout. GLOB is
// case-sensitive and anchored.
const (
	timeGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9]Z"
	dateGlob = "[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]"
)

// eventQueries builds the SELECTs that read the live events that can overlap [from, to), so memory
// does not grow with the archive. The predicates mirror overlaps with bound parameters; stored
// instants have millisecond precision, so the instant bounds are widened to whole milliseconds
// (from down, to up) and the caller's exact overlaps check decides the edge.
//
// Timed and all-day events are read by two queries, not one OR, so each can search its own index
// (account_id, start_at) or start_at, and the partial all-day index on start_date; the account predicate is present only when an
// account is given.
func eventQueries(cols []column, accountID string, from, to time.Time, fromDate, toDate string) []eventQuery {
	fromText := formatTime(from.UTC().Truncate(time.Millisecond))
	toText := formatTime(to.UTC().Truncate(time.Millisecond).Add(time.Millisecond))
	account, args := "", []any(nil)
	if accountID != "" {
		account, args = " AND account_id=?", []any{accountID}
	}
	with := func(more ...any) []any { return append(append([]any(nil), args...), more...) }
	return []eventQuery{
		{
			sql: selectSQL(cols, "removed_at IS NULL"+account+` AND all_day<>1
	  AND start_at < ? AND (end_at > ? OR start_at >= ?)`),
			args: with(toText, fromText, fromText),
		},
		{
			sql: selectSQL(cols, "removed_at IS NULL"+account+` AND all_day=1
	  AND start_date <= ? AND (end_date > ? OR start_date >= ?)`),
			args: with(toDate, fromDate, fromDate),
		},
	}
}

// checkQueries build the count of stored events whose start is not in the exact shape the package
// writes: a non-empty start_at that is not a timeLayout instant, a non-empty start_date that is
// not a date, an all-day event with no start_date, and a timed event with no start_at. They run on every load, whatever the window, so a corrupt row
// cannot hide outside it; each runs on a start index (the partial all-day indexes for the
// all-day checks), so the cost is one pass over the index entries, and a row lookup for each
// all-day event in the timed-empty check, never a scan of the table.
func checkQueries(accountID string) []eventQuery {
	account, args := "", []any(nil)
	if accountID != "" {
		account, args = " AND account_id=?", []any{accountID}
	}
	count := func(where string) eventQuery {
		return eventQuery{sql: "SELECT count(*) FROM calendar_source_events WHERE " + where + account, args: args}
	}
	return []eventQuery{
		count("start_at<>'' AND NOT (start_at GLOB '" + timeGlob + "')"),
		count("all_day=1 AND (start_date='' OR NOT (start_date GLOB '" + dateGlob + "'))"),
		count("all_day<>1 AND start_at=''"),
	}
}

// checkStoredTimes returns an error naming how many stored events have a start that is not in the
// shape the package writes.
func checkStoredTimes(ctx context.Context, db *sql.DB, accountID string) error {
	bad := 0
	for _, q := range checkQueries(accountID) {
		var n int
		if err := db.QueryRowContext(ctx, q.sql, q.args...).Scan(&n); err != nil {
			return err
		}
		bad += n
	}
	if bad > 0 {
		return fmt.Errorf("calendar: %d stored events have a start that is empty or not in the stored time format", bad)
	}
	return nil
}

// loadEvents runs eventQueries and returns every row they select. A corrupt stored time is never
// a silent omission. The guarantee is exact:
//
//   - A stored start (start_at of a timed event, start_date of an all-day one) that is empty or not
//     in the exact stored shape fails the load with an error that counts the rows, wherever the
//     value sorts and whether or not the row is in the window (checkStoredTimes). The shape is
//     checked, not the calendar: a well-shaped impossible date such as 2026-13-45 is not detected
//     here, and fails only when its row is loaded and parsed.
//   - Any other stored time of a row in the window (end_at, original_start, last_modified,
//     detail_as_of and the rest) that does not parse fails the load when the row is scanned. Those
//     columns of a row outside the window are not read.
func loadEvents(ctx context.Context, db *sql.DB, accountID string, from, to time.Time, fromDate, toDate string) ([]keyedEvent, error) {
	if err := checkStoredTimes(ctx, db, accountID); err != nil {
		return nil, err
	}
	cols := selectColumns(false)
	var out []keyedEvent
	for _, q := range eventQueries(cols, accountID, from, to, fromDate, toDate) {
		rows, err := db.QueryContext(ctx, q.sql, q.args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			e, err := scanKeyed(rows, cols)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			out = append(out, e)
		}
		err = rowsErr(rows)
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		return a.key < b.key
	})
	return out, nil
}

func loadWindows(ctx context.Context, db *sql.DB, accountID string) ([]Window, error) {
	rows, err := db.QueryContext(ctx, `SELECT source, account_id, window_start, window_end, synced_at, cache_fresh_at
	  FROM calendar_sources WHERE (?='' OR account_id=?) ORDER BY source, account_id`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Window
	for rows.Next() {
		var src string
		var w Window
		var ws, we, sy, cf timeText
		if err := rows.Scan(&src, &w.AccountID, &ws, &we, &sy, &cf); err != nil {
			return nil, err
		}
		w.Source, w.Start, w.End, w.SyncedAt, w.CacheFreshAt = Source(src), ws.t, we.t, sy.t, cf.t
		out = append(out, w)
	}
	return out, rowsErr(rows)
}

func loadCoveredDays(ctx context.Context, db *sql.DB, accountID string) ([]coveredDay, error) {
	rows, err := db.QueryContext(ctx, `SELECT source, account_id, day, first_verified_at, last_verified_at
	  FROM calendar_covered_days WHERE (?='' OR account_id=?) ORDER BY source, account_id, day`, accountID, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []coveredDay
	for rows.Next() {
		var src string
		var d coveredDay
		var first, last timeText
		if err := rows.Scan(&src, &d.AccountID, &d.Day, &first, &last); err != nil {
			return nil, err
		}
		d.Source, d.FirstVerifiedAt, d.LastVerifiedAt = Source(src), first.t, last.t
		out = append(out, d)
	}
	return out, rowsErr(rows)
}
