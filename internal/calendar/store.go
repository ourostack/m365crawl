package calendar

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Match methods recorded in calendar_matches.
const (
	matchGlobal    = "global"
	matchComposite = "composite"
	matchUpgraded  = "upgraded"
	matchAmbiguous = "ambiguous"
)

// scanRow scans one row; tests replace it to force the failure.
var scanRow = (*sql.Rows).Scan

// rowsErr reports the error that ended a row scan; tests replace it to force the failure.
var rowsErr = (*sql.Rows).Err

// eventColumns are the calendar_source_events columns in insert order; seen_at and removed_at are
// handled by the SQL itself.
var eventColumns = []string{
	"source", "event_key", "composite_key", "source_id", "global_id", "original_start", "start_at", "end_at",
	"all_day", "start_date", "end_date", "time_zone", "subject", "organizer", "attendees_json", "location",
	"online_meeting_url", "teams_thread_id", "series_key", "cancelled", "response", "show_as", "body_preview",
	"last_modified", "seen_at",
}

// upsertSQL inserts a row or refreshes every column of the existing one and clears removed_at.
var upsertSQL = func() string {
	sets := make([]string, 0, len(eventColumns))
	for _, c := range eventColumns[2:] {
		sets = append(sets, c+"=excluded."+c)
	}
	return "INSERT INTO calendar_source_events (" + strings.Join(eventColumns, ",") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?,", len(eventColumns)), ",") +
		") ON CONFLICT(source, event_key) DO UPDATE SET " + strings.Join(sets, ",") + ", removed_at=NULL"
}()

// ApplySnapshot records one source's snapshot: it upserts events under their resolved keys, sets
// removed_at on that source's live rows inside the window that events no longer contain, and
// records the window. Timed rows are inside when their start is in [w.Start, w.End); all-day rows
// when their start date is within the window's UTC dates. It runs in one transaction.
func ApplySnapshot(ctx context.Context, db *sql.DB, w Window, events []Event, at time.Time) (err error) {
	for _, e := range events {
		if e.Source != w.Source {
			return fmt.Errorf("calendar: event %q is from source %q, snapshot is for %q", e.SourceID, e.Source, w.Source)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// Process in SourceID order so keys never depend on the order the adapter listed events in.
	events = append([]Event(nil), events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].SourceID < events[j].SourceID })
	snap := newSnapshot(events)
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		key, err := resolveKey(ctx, tx, e, snap)
		if err != nil {
			return err
		}
		if err := upsertEvent(ctx, tx, e, key, at); err != nil {
			return err
		}
		seen[key] = true
	}
	if err := markRemoved(ctx, tx, w, seen, at); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO calendar_sources (source, window_start, window_end, synced_at, cache_fresh_at) VALUES (?,?,?,?,?)
		 ON CONFLICT(source) DO UPDATE SET window_start=excluded.window_start, window_end=excluded.window_end,
		 synced_at=excluded.synced_at, cache_fresh_at=excluded.cache_fresh_at`,
		string(w.Source), formatTime(w.Start), formatTime(w.End), formatTime(w.SyncedAt), formatTime(w.CacheFreshAt)); err != nil {
		return err
	}
	return tx.Commit()
}

// snapshot indexes a snapshot's events so resolveKey can count same-hash events in the snapshot
// no matter where in the list the current event sits.
type snapshot struct {
	ids    map[string]bool            // every source id in the snapshot
	byHash map[string]map[string]bool // composite hash -> source ids with that hash
	keys   map[string]bool            // keys the snapshot's global-id events will take
}

func newSnapshot(events []Event) snapshot {
	s := snapshot{ids: map[string]bool{}, byHash: map[string]map[string]bool{}, keys: map[string]bool{}}
	for _, e := range events {
		h := compositeKey(e)
		if s.byHash[h] == nil {
			s.byHash[h] = map[string]bool{}
		}
		s.byHash[h][e.SourceID] = true
		s.ids[e.SourceID] = true
		if e.GlobalID != "" {
			s.keys[Key(e)] = true
		}
	}
	return s
}

// otherRow is a live row of the other source that shares an event's composite hash.
type otherRow struct {
	key       string
	composite bool // the row is keyed without a global id
	blocked   bool // moving it would collide with a row that already holds the target key
}

// resolveKey returns the key e is stored under, recording the match the first time. An existing
// match for (source, source_id) always wins so keys never flip between syncs.
//
// Upgrade rule, symmetric so the result does not depend on which source syncs first: events with
// the same composite hash are joined across sources only when exactly one event on each side has
// that hash (and one of them has a global id and the other none). Any other count is ambiguous and
// nothing is joined. The counts see the whole current snapshot plus stored rows, so the decision
// is the same wherever e sits in the snapshot. A twin that first appears in a later sync cannot
// undo an upgrade already made: the requirement is "same snapshots, same agenda".
func resolveKey(ctx context.Context, tx *sql.Tx, e Event, snap snapshot) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT event_key FROM calendar_matches WHERE source=? AND source_id=?`,
		string(e.Source), e.SourceID).Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	composite := compositeKey(e)
	global := e.GlobalID != ""
	key, method := composite, matchComposite
	if global {
		key, method = Key(e), matchGlobal
	}
	own, err := ownCount(ctx, tx, e.Source, composite, snap)
	if err != nil {
		return "", err
	}
	others, err := otherRows(ctx, tx, e, composite, key)
	if err != nil {
		return "", err
	}
	// Candidates are the other side's rows of the opposite kind.
	var cands []otherRow
	for _, o := range others {
		if o.composite == global {
			cands = append(cands, o)
		}
	}
	upgrade := false
	if len(cands) > 0 {
		switch {
		case len(cands) > 1 || len(others) > 1 || own > 1:
			method = matchAmbiguous
		case !cands[0].blocked && (global || !snap.keys[cands[0].key]):
			upgrade = true
		}
	}
	switch {
	case upgrade && global:
		if err := rekey(ctx, tx, e.Source, cands[0].key, key); err != nil {
			return "", err
		}
	case upgrade:
		key, method = cands[0].key, matchUpgraded
	case !global && own > 1:
		// Colliding twins all carry their source id; none owns the bare hash.
		key = composite + "#" + e.SourceID
	}
	if err := recordMatch(ctx, tx, key, e.Source, e.SourceID, method); err != nil {
		return "", err
	}
	return key, nil
}

// ownCount counts the distinct events of source that share composite: those in the snapshot, plus
// live stored rows that the snapshot does not mention.
func ownCount(ctx context.Context, tx *sql.Tx, source Source, composite string, snap snapshot) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT source_id FROM calendar_source_events WHERE source=? AND composite_key=? AND removed_at IS NULL`,
		string(source), composite)
	if err != nil {
		return 0, err
	}
	ids, err := scanStrings(rows)
	if err != nil {
		return 0, err
	}
	n := len(snap.byHash[composite])
	for _, id := range ids {
		if !snap.ids[id] {
			n++
		}
	}
	return n, nil
}

// otherRows lists the other source's live rows with the composite hash. target is the key e is
// about to take: for an event with a global id, a row is blocked when its source already holds
// target; for one without, when this source already holds the row's key.
func otherRows(ctx context.Context, tx *sql.Tx, e Event, composite, target string) ([]otherRow, error) {
	query, blockedArg := compositeArrivalRows, string(e.Source)
	if e.GlobalID != "" {
		query, blockedArg = globalArrivalRows, target
	}
	rows, err := tx.QueryContext(ctx, query, blockedArg, string(e.Source), composite)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []otherRow
	for rows.Next() {
		var o otherRow
		if err := scanRow(rows, &o.key, &o.composite, &o.blocked); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rowsErr(rows)
}

const otherRowsHead = `SELECT c.event_key, substr(c.event_key, 1, 10) = 'composite|', `
const otherRowsTail = ` FROM calendar_source_events c WHERE c.source <> ? AND c.composite_key = ? AND c.removed_at IS NULL
  ORDER BY c.source, c.event_key`

const globalArrivalRows = otherRowsHead +
	`EXISTS (SELECT 1 FROM calendar_source_events m WHERE m.source = c.source AND m.event_key = ?)` + otherRowsTail

const compositeArrivalRows = otherRowsHead +
	`EXISTS (SELECT 1 FROM calendar_source_events m WHERE m.source = ? AND m.event_key = c.event_key)` + otherRowsTail

// rekey moves the other source's row from key old to key to, and its match.
func rekey(ctx context.Context, tx *sql.Tx, self Source, old, to string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE calendar_source_events SET event_key=? WHERE source<>? AND event_key=?`,
		to, string(self), old); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE calendar_matches SET event_key=?, match_method=? WHERE source<>? AND event_key=?`,
		to, matchUpgraded, string(self), old)
	return err
}

func recordMatch(ctx context.Context, tx *sql.Tx, key string, source Source, sourceID, method string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO calendar_matches (event_key, source, source_id, match_method) VALUES (?,?,?,?)`,
		key, string(source), sourceID, method)
	return err
}

// scanStrings drains rows of one text column and closes them.
func scanStrings(rows *sql.Rows) ([]string, error) {
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := scanRow(rows, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rowsErr(rows)
}

func upsertEvent(ctx context.Context, tx *sql.Tx, e Event, key string, at time.Time) error {
	_, err := tx.ExecContext(ctx, upsertSQL,
		string(e.Source), key, compositeKey(e), e.SourceID, e.GlobalID, formatTimePtr(e.OriginalStart),
		formatTime(e.Start), formatTime(e.End), e.AllDay, e.StartDate, e.EndDate, e.TimeZone, e.Subject,
		e.Organizer, e.AttendeesJSON, e.Location, e.OnlineMeetingURL, e.TeamsThreadID, e.SeriesKey, e.Cancelled,
		e.Response, e.ShowAs, e.BodyPreview, formatTimePtr(e.LastModified), formatTime(at))
	return err
}

// markRemoved sets removed_at on the source's live rows inside the window whose key is not seen.
func markRemoved(ctx context.Context, tx *sql.Tx, w Window, seen map[string]bool, at time.Time) error {
	firstDate := w.Start.UTC().Format(dateLayout)
	lastDate := w.End.UTC().Add(-time.Nanosecond).Format(dateLayout)
	rows, err := tx.QueryContext(ctx,
		`SELECT event_key FROM calendar_source_events WHERE source=? AND removed_at IS NULL AND (
		   (all_day=0 AND start_at >= ? AND start_at < ?) OR (all_day=1 AND start_date >= ? AND start_date <= ?))`,
		string(w.Source), formatTime(w.Start), formatTime(w.End), firstDate, lastDate)
	if err != nil {
		return err
	}
	keys, err := scanStrings(rows)
	if err != nil {
		return err
	}
	for _, k := range keys {
		if seen[k] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE calendar_source_events SET removed_at=? WHERE source=? AND event_key=?`,
			formatTime(at), string(w.Source), k); err != nil {
			return err
		}
	}
	return nil
}

// AgendaItem is one merged event and the key it is stored under.
type AgendaItem struct {
	Event
	Key string
}

// Agenda returns the merged events overlapping [from, to), sorted by start, and whether any part
// of the range lies outside every source's window (so an absent event is not evidence). All-day
// events are compared by date in from's zone: a range covers the dates of from through the last
// instant before to.
func Agenda(ctx context.Context, db *sql.DB, from, to time.Time) (items []AgendaItem, gap bool, err error) {
	rows, err := loadEvents(ctx, db)
	if err != nil {
		return nil, false, err
	}
	windows, err := loadWindows(ctx, db)
	if err != nil {
		return nil, false, err
	}
	if !to.After(from) {
		return nil, covered(windows, from, to), nil
	}
	fromDate := from.Format(dateLayout)
	toDate := to.Add(-time.Nanosecond).In(from.Location()).Format(dateLayout)
	fresh := make(map[Source]time.Time, len(windows))
	for _, w := range windows {
		fresh[w.Source] = w.CacheFreshAt
	}
	groups := map[string][]Event{}
	for _, r := range rows {
		if overlaps(r.Event, from, to, fromDate, toDate) {
			groups[r.key] = append(groups[r.key], r.Event)
		}
	}
	for key, g := range groups {
		items = append(items, AgendaItem{Event: Merge(g, fresh), Key: key})
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if sa, sb := sortStart(a.Event, from), sortStart(b.Event, from); !sa.Equal(sb) {
			return sa.Before(sb)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		if a.Subject != b.Subject {
			return a.Subject < b.Subject
		}
		return a.Key < b.Key
	})
	return items, !covered(windows, from, to), nil
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

// covered reports whether the union of the windows contains all of [from, to).
func covered(windows []Window, from, to time.Time) bool {
	sort.Slice(windows, func(i, j int) bool { return windows[i].Start.Before(windows[j].Start) })
	cur := from
	for _, w := range windows {
		if w.Start.After(cur) {
			break
		}
		if w.End.After(cur) {
			cur = w.End
		}
	}
	return !cur.Before(to)
}

type keyedEvent struct {
	Event
	key string
}

const selectEvents = `SELECT source, event_key, source_id, global_id, original_start, start_at, end_at, all_day, start_date,
  end_date, time_zone, subject, organizer, attendees_json, location, online_meeting_url, teams_thread_id, series_key,
  cancelled, response, show_as, body_preview, last_modified FROM calendar_source_events WHERE removed_at IS NULL
  ORDER BY source, event_key`

func loadEvents(ctx context.Context, db *sql.DB) ([]keyedEvent, error) {
	rows, err := db.QueryContext(ctx, selectEvents)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []keyedEvent
	for rows.Next() {
		var e keyedEvent
		var src string
		var orig, start, end, mod timeText
		if err := rows.Scan(&src, &e.key, &e.SourceID, &e.GlobalID, &orig, &start, &end, &e.AllDay, &e.StartDate,
			&e.EndDate, &e.TimeZone, &e.Subject, &e.Organizer, &e.AttendeesJSON, &e.Location, &e.OnlineMeetingURL,
			&e.TeamsThreadID, &e.SeriesKey, &e.Cancelled, &e.Response, &e.ShowAs, &e.BodyPreview, &mod); err != nil {
			return nil, err
		}
		e.Source, e.OriginalStart, e.Start, e.End, e.LastModified = Source(src), orig.ptr(), start.t, end.t, mod.ptr()
		out = append(out, e)
	}
	return out, rowsErr(rows)
}

func loadWindows(ctx context.Context, db *sql.DB) ([]Window, error) {
	rows, err := db.QueryContext(ctx, `SELECT source, window_start, window_end, synced_at, cache_fresh_at FROM calendar_sources`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Window
	for rows.Next() {
		var src string
		var ws, we, sy, cf timeText
		if err := rows.Scan(&src, &ws, &we, &sy, &cf); err != nil {
			return nil, err
		}
		out = append(out, Window{Source: Source(src), Start: ws.t, End: we.t, SyncedAt: sy.t, CacheFreshAt: cf.t})
	}
	return out, rowsErr(rows)
}
