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
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		key, err := resolveKey(ctx, tx, e)
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

// resolveKey returns the key e is stored under, recording the match the first time. An existing
// match for (source, source_id) always wins so keys never flip between syncs.
func resolveKey(ctx context.Context, tx *sql.Tx, e Event) (string, error) {
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
	method := matchComposite
	var cands []string
	if e.GlobalID != "" {
		key, method = Key(e), matchGlobal
		// Other-source rows minted without an id that carry this event's composite hash.
		if cands, err = candidates(ctx, tx, e.Source, composite, true, key); err != nil {
			return "", err
		}
		switch len(cands) {
		case 0:
		case 1:
			if err = rekey(ctx, tx, e.Source, cands[0], key); err != nil {
				return "", err
			}
		default:
			method = matchAmbiguous
		}
	} else {
		key = composite
		// Other-source rows that already hold a global key with this event's composite hash.
		if cands, err = candidates(ctx, tx, e.Source, composite, false, ""); err != nil {
			return "", err
		}
		switch len(cands) {
		case 0:
		case 1:
			key, method = cands[0], matchUpgraded
		default:
			method = matchAmbiguous
		}
		if method != matchUpgraded {
			if key, err = freeKey(ctx, tx, e, composite); err != nil {
				return "", err
			}
		}
	}
	if err := recordMatch(ctx, tx, key, e.Source, e.SourceID, method); err != nil {
		return "", err
	}
	return key, nil
}

const candidatesHead = `SELECT c.event_key FROM calendar_source_events c WHERE c.source <> ? AND c.composite_key = ?
  AND substr(c.event_key, 1, ?) `

// compositeCandidates finds composite-keyed rows of the other source to move onto a global key (arg 5).
const compositeCandidates = candidatesHead + `= ? AND NOT EXISTS (SELECT 1 FROM calendar_source_events m
  WHERE m.source = c.source AND m.event_key = ?) ORDER BY c.source, c.event_key`

// globalCandidates finds global-keyed rows of the other source that this source has no row under (arg 5).
const globalCandidates = candidatesHead + `<> ? AND NOT EXISTS (SELECT 1 FROM calendar_source_events m
  WHERE m.source = ? AND m.event_key = c.event_key) ORDER BY c.source, c.event_key`

// candidates lists the keys of the other source's rows (live or removed) whose composite hash is
// composite: composite-keyed rows when wantComposite, else global-keyed rows. A row is skipped
// when the upgrade would collide: moving an other-source row to target when that source already
// holds target, or taking an other-source key this source already holds.
func candidates(ctx context.Context, tx *sql.Tx, self Source, composite string, wantComposite bool, target string) ([]string, error) {
	query, guardArg := globalCandidates, string(self)
	if wantComposite {
		query, guardArg = compositeCandidates, target
	}
	rows, err := tx.QueryContext(ctx, query, string(self), composite, len(compositePrefix), compositePrefix, guardArg)
	if err != nil {
		return nil, err
	}
	return scanStrings(rows)
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

// freeKey returns composite, or composite#source_id when another event of the same source already
// holds it (two distinct events with the same organizer, subject and start).
func freeKey(ctx context.Context, tx *sql.Tx, e Event, composite string) (string, error) {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM calendar_source_events WHERE source=? AND event_key=? AND source_id<>?`,
		string(e.Source), composite, e.SourceID).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return composite + "#" + e.SourceID, nil
	}
	return composite, nil
}

func recordMatch(ctx context.Context, tx *sql.Tx, key string, source Source, sourceID, method string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO calendar_matches (event_key, source, source_id, match_method) VALUES (?,?,?,?)`,
		key, string(source), sourceID, method)
	return err
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
