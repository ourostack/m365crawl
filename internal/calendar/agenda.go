package calendar

import (
	"context"
	"database/sql"
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

// accountFilter is the optional account condition of a query: with an empty account it matches
// every row. It takes the account twice.
const accountFilter = " AND (?='' OR account_id=?)"

// loadEvents reads the live events that can overlap [from, to) so memory does not grow with the
// archive. The SQL predicate mirrors overlaps with bound parameters; stored instants have
// millisecond precision, so the instant bounds are widened to whole milliseconds (from down, to up)
// and the caller's exact overlaps check decides the edge.
func loadEvents(ctx context.Context, db *sql.DB, accountID string, from, to time.Time, fromDate, toDate string) ([]keyedEvent, error) {
	cols := selectColumns(false)
	fromText := formatTime(from.UTC().Truncate(time.Millisecond))
	toText := formatTime(to.UTC().Truncate(time.Millisecond).Add(time.Millisecond))
	where := "removed_at IS NULL" + accountFilter + ` AND ((all_day<>1 AND start_at < ? AND (end_at > ? OR start_at >= ?))
	  OR (all_day=1 AND start_date <= ? AND (end_date > ? OR start_date >= ?)))`
	rows, err := db.QueryContext(ctx, selectSQL(cols, where), accountID, accountID,
		toText, fromText, fromText, toDate, fromDate, fromDate)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []keyedEvent
	for rows.Next() {
		e, err := scanKeyed(rows, cols)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rowsErr(rows)
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
