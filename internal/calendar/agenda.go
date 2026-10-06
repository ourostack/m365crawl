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
	// AccountID limits the agenda to one principal: the account named and every account linked to
	// the same principal. Empty means every account. Events of different principals are never
	// merged with each other.
	AccountID string
	From, To  time.Time
	// IncludeCancelled and IncludeDeclined show events the default agenda hides. IncludeMasters
	// shows recurring masters, which are otherwise never listed. IncludeRemoved shows events a
	// source saw go (see Merge, rule M0).
	IncludeCancelled, IncludeDeclined, IncludeMasters, IncludeRemoved bool
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
	// Unlinked lists the accounts in scope of sources other than Teams that have no active link, so
	// their events are not merged with any Teams account's. Sorted; nil when there are none.
	Unlinked []string
}

// AgendaItem is one merged event and the key it is stored under.
type AgendaItem struct {
	Merged
	Key string
	// Principal is the account the event is grouped under: a Teams account, or the account itself
	// when nothing links it.
	Principal string
	// Accounts lists the accounts whose rows are in the group, sorted.
	Accounts []string
}

// Agenda returns the merged events overlapping the query's range, sorted by start. It loads in
// two steps, so a meeting that one source holds as all-day and another as timed, or that one
// source saw go, is judged as one event: it finds the (principal, key) groups that have a row
// near the range, loads every row, live and removed, of those groups, merges each group, and only
// then applies the overlap test, the visibility rules and the removal rule to the merged event.
// Events are merged across sources by key within a principal. Cancelled, declined, master and
// removed events are left out unless the query asks for them.
func Agenda(ctx context.Context, db *sql.DB, q AgendaQuery) (AgendaResult, error) {
	var res AgendaResult
	principals, err := LoadPrincipals(ctx, db)
	if err != nil {
		return res, err
	}
	scope := principals.Resolve(q.AccountID)
	fromDate := q.From.Format(dateLayout)
	toDate := q.To.Add(-time.Nanosecond).In(q.From.Location()).Format(dateLayout)
	groups, err := loadGroups(ctx, db, principals, scope, q.From, q.To, fromDate, toDate)
	if err != nil {
		return res, err
	}
	windows, err := loadWindows(ctx, db, scope)
	if err != nil {
		return res, err
	}
	days, err := loadCoveredDays(ctx, db, scope)
	if err != nil {
		return res, err
	}
	if !q.To.After(q.From) {
		return res, nil
	}
	res.Gap, res.AsOf = coverage(windows, days, principals, q.From, q.To)
	fresh := map[string]map[Source]time.Time{}
	unlinked := map[string]bool{}
	for _, w := range windows {
		p := principals.Of(w.AccountID)
		if fresh[p] == nil {
			fresh[p] = map[Source]time.Time{}
		}
		fresh[p][w.Source] = w.CacheFreshAt
		if w.Source != SourceTeams && p == w.AccountID {
			unlinked[w.AccountID] = true
		}
	}
	for a := range unlinked {
		res.Unlinked = append(res.Unlinked, a)
	}
	sort.Strings(res.Unlinked)
	needle := strings.ToLower(q.Query)
	for k, g := range groups {
		m := Merge(g, fresh[k.principal])
		if m.Removed && !q.IncludeRemoved || !overlaps(m.Event, q.From, q.To, fromDate, toDate) || !visible(m.Event, q, needle) {
			continue
		}
		accounts := map[string]bool{}
		for _, r := range g {
			accounts[r.AccountID] = true
		}
		item := AgendaItem{Merged: m, Key: k.key, Principal: k.principal}
		for a := range accounts {
			item.Accounts = append(item.Accounts, a)
		}
		sort.Strings(item.Accounts)
		res.Items = append(res.Items, item)
	}
	sort.Slice(res.Items, func(i, j int) bool {
		a, b := res.Items[i], res.Items[j]
		if sa, sb := sortStart(a.Event, q.From), sortStart(b.Event, q.From); !sa.Equal(sb) {
			return sa.Before(sb)
		}
		if aa, ba := a.AllDay.Is(true), b.AllDay.Is(true); aa != ba {
			return aa
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.Principal < b.Principal
	})
	return res, nil
}

// groupKey names one event of one principal.
type groupKey struct{ principal, key string }

// visible applies the query's filters to a merged event.
func visible(e Event, q AgendaQuery, needle string) bool {
	switch {
	case e.EventType == EventMaster && !q.IncludeMasters:
		return false
	case e.Cancelled.Is(true) && !q.IncludeCancelled:
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

// overlaps reports whether e touches [from, to). An event whose all-day flag is unknown is timed
// here: its instants decide.
func overlaps(e Event, from, to time.Time, fromDate, toDate string) bool {
	if e.AllDay.Is(true) {
		return e.StartDate <= toDate && (e.EndDate > fromDate || e.StartDate >= fromDate)
	}
	return e.Start.Before(to) && (e.End.After(from) || e.Start.Equal(from))
}

// sortStart is the instant an event sorts by: its start, or midnight of its date in loc's zone.
func sortStart(e Event, ref time.Time) time.Time {
	if e.AllDay.Is(true) {
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
// among what covers it. Coverage is recorded by (source, account) and judged per principal. Within
// a principal, a source with covered-day rows covers exactly those days (dates in from's zone) and
// a source without any covers its whole window; the sources and accounts of one principal
// complement each other, so a day covered by either the Teams days or the Outlook days is covered.
// A day is a gap when any principal in scope does not cover it, so an all-accounts query never
// hides an account that skipped the day; an unlinked account is its own principal and is judged
// alone. With nothing in scope every day is a gap.
func coverage(windows []Window, days []coveredDay, p Principals, from, to time.Time) (gap bool, asOf time.Time) {
	type srcKey struct {
		source  Source
		account string
	}
	type accountCover struct {
		verified map[string]time.Time // date -> newest verification across the principal's sources
		spans    []Window             // windows of the principal's sources that have no covered days
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
		c := of(p.Of(d.AccountID))
		if d.LastVerifiedAt.After(c.verified[d.Day]) {
			c.verified[d.Day] = d.LastVerifiedAt
		}
	}
	for _, w := range windows {
		c := of(p.Of(w.AccountID))
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

// eventQuery is one SELECT of loadGroups with its bound arguments.
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

// eventQueries builds the SELECTs that read the rows, live and removed, that can overlap
// [from, to), so memory does not grow with the archive. The predicates mirror overlaps with bound
// parameters; stored instants have millisecond precision, so the instant bounds are widened to
// whole milliseconds (from down, to up) and the caller's exact overlaps check decides the edge. An
// unknown all-day flag (NULL) reads as timed, so such a row is found by its instants and never
// silently dropped.
//
// Timed and all-day events are read by two queries, not one OR, so each can search its own index
// (account_id, start_at) or start_at, and the partial all-day index on start_date; the account
// predicate is present only when accounts are given.
func eventQueries(cols []column, accounts []string, from, to time.Time, fromDate, toDate string) []eventQuery {
	fromText := formatTime(from.UTC().Truncate(time.Millisecond))
	toText := formatTime(to.UTC().Truncate(time.Millisecond).Add(time.Millisecond))
	account, args := "", []any(nil)
	if len(accounts) > 0 {
		account = " AND account_id IN (" + placeholders(len(accounts)) + ")"
		for _, a := range accounts {
			args = append(args, a)
		}
	}
	with := func(more ...any) []any { return append(append([]any(nil), args...), more...) }
	return []eventQuery{
		{
			sql: selectSQL(cols, "COALESCE(all_day,0)<>1"+account+`
	  AND start_at < ? AND (end_at > ? OR start_at >= ?)`),
			args: with(toText, fromText, fromText),
		},
		{
			sql: selectSQL(cols, "all_day=1"+account+`
	  AND start_date <= ? AND (end_date > ? OR start_date >= ?)`),
			args: with(toDate, fromDate, fromDate),
		},
	}
}

// checkQueries build the counts of stored events whose start is not in the exact shape the package
// writes: a non-empty start_at that is not a timeLayout instant, an all-day event with an empty or
// malformed start_date, and a timed event with no start_at. Each runs on a start index (the
// partial all-day indexes for the all-day checks), so the cost is one pass over the index entries,
// and a row lookup for each all-day event in the timed-empty check, never a scan of the table.
func checkQueries() []eventQuery {
	count := func(where string) eventQuery {
		return eventQuery{sql: "SELECT count(*) FROM calendar_source_events WHERE " + where}
	}
	return []eventQuery{
		count("start_at<>'' AND NOT (start_at GLOB '" + timeGlob + "')"),
		count("all_day=1 AND (start_date='' OR NOT (start_date GLOB '" + dateGlob + "'))"),
		count("COALESCE(all_day,0)<>1 AND start_at=''"),
	}
}

// CheckStoredTimes counts the stored events, in any account, whose start is empty or not in the
// exact shape the package writes. Writes refuse such events (ValidateEvent), so a non-zero count
// means the archive was damaged or written by something else. It is not run on every agenda read
// (about 35 to 47 ms on 100,000 rows, half the read); the doctor command calls it. The shape is
// checked, not the calendar: a well-shaped impossible date such as 2026-13-45 is not counted.
func CheckStoredTimes(ctx context.Context, db *sql.DB) (int, error) {
	bad := 0
	for _, q := range checkQueries() {
		var n int
		if err := db.QueryRowContext(ctx, q.sql).Scan(&n); err != nil {
			return 0, err
		}
		bad += n
	}
	return bad, nil
}

// queryKeyed runs one SELECT built with selectSQL and scans its rows. A stored time that does not
// parse fails the load when its row is scanned, so a corrupt row inside the window is an error, not
// an omission.
func queryKeyed(ctx context.Context, db *sql.DB, cols []column, q eventQuery) ([]keyedEvent, error) {
	rows, err := db.QueryContext(ctx, q.sql, q.args...)
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

// keyChunk bounds the keys in one IN list, well under SQLite's variable limit.
const keyChunk = 400

// loadGroups is the two-step load. Step one finds the candidate groups: (principal, key) of every
// row of the accounts in scope, live or removed, that can overlap the range, with all-day rows
// widened by a day on each side because an all-day row and a timed row of one meeting can fall on
// different sides of an edge. Step two loads every row of those keys, of every account of the
// principals involved, and groups them by (principal, key). A corrupt start that sorts outside the
// range is not read; writes refuse such events and CheckStoredTimes counts the ones already stored.
func loadGroups(ctx context.Context, db *sql.DB, p Principals, scope []string, from, to time.Time, fromDate, toDate string) (map[groupKey][]Event, error) {
	first, _ := time.Parse(dateLayout, fromDate)
	last, _ := time.Parse(dateLayout, toDate)
	idCols := []column{eventColumns[1]} // account_id
	want := map[groupKey]bool{}
	var keys []string
	have := map[string]bool{}
	collect := func(queries []eventQuery) error {
		for _, q := range queries {
			found, err := queryKeyed(ctx, db, idCols, q)
			if err != nil {
				return err
			}
			for _, r := range found {
				if g := (groupKey{p.Of(r.AccountID), r.key}); !want[g] {
					want[g] = true
					if !have[r.key] {
						have[r.key] = true
						keys = append(keys, r.key)
					}
				}
			}
		}
		return nil
	}
	err := collect(eventQueries(idCols, scope, from, to, first.AddDate(0, 0, -1).Format(dateLayout), last.AddDate(0, 0, 1).Format(dateLayout)))
	if err == nil {
		// Keys never move, so one occurrence can sit under a timed key in one source and a date key
		// in another. Every key of a candidate's global id is a candidate too; joinOccurrences decides.
		err = collect(siblingQueries(idCols, keys, first.AddDate(0, 0, -2).Format(dateLayout), last.AddDate(0, 0, 3).Format(dateLayout)))
	}
	if err != nil {
		return nil, err
	}
	sort.Strings(keys)
	cols := selectColumns(false)
	groups := map[groupKey][]Event{}
	for len(keys) > 0 {
		n := min(len(keys), keyChunk)
		args := make([]any, n)
		for i, k := range keys[:n] {
			args[i] = k
		}
		found, err := queryKeyed(ctx, db, cols, eventQuery{sql: selectSQL(cols, "event_key IN ("+placeholders(n)+")"), args: args})
		if err != nil {
			return nil, err
		}
		for _, r := range found {
			if g := (groupKey{p.Of(r.AccountID), r.key}); want[g] {
				groups[g] = append(groups[g], r.Event)
			}
		}
		keys = keys[n:]
	}
	joinOccurrences(groups)
	for _, rows := range groups {
		sort.Slice(rows, func(i, j int) bool { return rows[i].Source < rows[j].Source })
	}
	return groups, nil
}

// siblingQueries select the account and key of the keys of each global id in keys that can join a
// candidate: the date-form keys within the range widened by one day, and the timed keys whose
// start is within one day of those dates. Key suffixes sort by their leading date, so one range
// [id|lo, id|hi) per id covers both, and a long series costs a few rows, not all of its
// occurrences. lo and hi are dates (the range's first date less two days, its last plus three).
func siblingQueries(idCols []column, keys []string, lo, hi string) []eventQuery {
	seen := map[string]bool{}
	var out []eventQuery
	for _, k := range keys {
		gid, suffix, ok := splitKey(k)
		if !ok || suffix == "" || seen[gid] {
			continue
		}
		seen[gid] = true
		out = append(out, eventQuery{sql: selectSQL(idCols, "event_key >= ? AND event_key < ?"), args: []any{gid + "|" + lo, gid + "|" + hi}})
	}
	return out
}

// splitKey splits a global key "<id>|<suffix>"; a composite key has no global id.
func splitKey(key string) (gid, suffix string, ok bool) {
	i := strings.LastIndex(key, "|")
	if i < 0 || strings.HasPrefix(key, compositePrefix) {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// joinOccurrences merges, within one principal, the group of a date key with the group of a timed
// key of the same global id whose original start falls on that date. Keys never move, so an
// occurrence first stored as timed that later became all-day keeps its timed key while its
// twin mints the date key; reading joins them.
//
// The date of an instant is read in the rows' own zone (IANA name, then UTC offset). With no zone
// on any row the instant must lie in the date's UTC day: that holds for Outlook, which stores
// an all-day event at midnight UTC, and for Teams, which stores it at local midnight, in every
// zone west of UTC. An instant before the date's UTC midnight (an eastern zone, or the previous
// evening's occurrence of a daily series) is not joined, because without a zone the two cannot be
// told apart. Two occurrences on adjacent days therefore never join.
//
// A joined group holds at most one row per source: of the timed keys that fall on a date, a source
// contributes the one nearest the date's local midnight, and only if the date group has no row of
// that source already. The rest stay groups of their own.
func joinOccurrences(groups map[groupKey][]Event) {
	type id struct{ principal, gid string }
	dates := map[id][]string{}
	var timed []groupKey
	for g := range groups {
		gid, suffix, ok := splitKey(g.key)
		switch {
		case !ok || suffix == "":
		case isDateSuffix(suffix):
			dates[id{g.principal, gid}] = append(dates[id{g.principal, gid}], suffix)
		default:
			timed = append(timed, g)
		}
	}
	type offer struct {
		from groupKey
		to   groupKey
		dist time.Duration
	}
	var offers []offer
	for _, g := range timed {
		gid, suffix, _ := splitKey(g.key)
		cands := dates[id{g.principal, gid}]
		at, err := time.Parse(time.RFC3339, suffix)
		if len(cands) == 0 || err != nil { // err: not a key this package minted
			continue
		}
		if date, dist, ok := occurrenceDate(at, groups[g], cands); ok {
			offers = append(offers, offer{g, groupKey{g.principal, gid + "|" + date}, dist})
		}
	}
	sort.Slice(offers, func(i, j int) bool {
		if offers[i].dist != offers[j].dist {
			return offers[i].dist < offers[j].dist
		}
		return offers[i].from.key < offers[j].from.key
	})
	held := map[groupKey]map[Source]bool{}
	for _, o := range offers {
		if held[o.to] == nil {
			held[o.to] = map[Source]bool{}
			for _, r := range groups[o.to] {
				held[o.to][r.Source] = true
			}
		}
		clash := false
		for _, r := range groups[o.from] {
			clash = clash || held[o.to][r.Source]
		}
		if clash {
			continue
		}
		for _, r := range groups[o.from] {
			held[o.to][r.Source] = true
		}
		groups[o.to] = append(groups[o.to], groups[o.from]...)
		delete(groups, o.from)
	}
}

func isDateSuffix(s string) bool {
	_, err := time.Parse(dateLayout, s)
	return err == nil && len(s) == len(dateLayout)
}

// occurrenceDate is the date key among cands that the instant at belongs to, and how far at is
// from that date's local midnight. The rows' own zone decides when one is known; otherwise the
// date is the UTC day that holds at, and the distance is from its UTC midnight.
func occurrenceDate(at time.Time, rows []Event, cands []string) (string, time.Duration, bool) {
	loc := time.UTC
	known := false
	for _, r := range rows {
		if r.TimeZoneIANA != "" {
			if l, err := time.LoadLocation(r.TimeZoneIANA); err == nil {
				loc, known = l, true
				break
			}
		}
	}
	if !known {
		for _, r := range rows {
			if t, err := time.Parse("-07:00", r.UTCOffset); err == nil {
				_, secs := t.Zone()
				loc, known = time.FixedZone("", secs), true
				break
			}
		}
	}
	d := at.In(loc).Format(dateLayout)
	for _, c := range cands {
		if c == d {
			midnight, _ := time.ParseInLocation(dateLayout, c, loc)
			return d, at.Sub(midnight).Abs(), true
		}
	}
	return "", 0, false
}

// accountClause is the WHERE condition that limits a table to accounts; empty means all.
func accountClause(accounts []string) (string, []any) {
	if len(accounts) == 0 {
		return "1", nil
	}
	args := make([]any, len(accounts))
	for i, a := range accounts {
		args[i] = a
	}
	return "account_id IN (" + placeholders(len(accounts)) + ")", args
}

func loadWindows(ctx context.Context, db *sql.DB, accounts []string) ([]Window, error) {
	where, args := accountClause(accounts)
	rows, err := db.QueryContext(ctx, strings.Replace(`SELECT source, account_id, window_start, window_end, synced_at, cache_fresh_at
	  FROM calendar_sources WHERE @where ORDER BY source, account_id`, "@where", where, 1), args...)
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

func loadCoveredDays(ctx context.Context, db *sql.DB, accounts []string) ([]coveredDay, error) {
	where, args := accountClause(accounts)
	rows, err := db.QueryContext(ctx, strings.Replace(`SELECT source, account_id, day, first_verified_at, last_verified_at
	  FROM calendar_covered_days WHERE @where ORDER BY source, account_id, day`, "@where", where, 1), args...)
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
