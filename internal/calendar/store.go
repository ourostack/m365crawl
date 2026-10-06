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

// outcome is what applying one row did to the table.
type outcome int

const (
	outcomeNew outcome = iota
	outcomeChanged
	outcomeUnchanged
)

// Counts tallies what a batch did to one kind of row.
type Counts struct{ New, Changed, Unchanged int }

func (c *Counts) add(o outcome) {
	switch o {
	case outcomeNew:
		c.New++
	case outcomeChanged:
		c.Changed++
	default:
		c.Unchanged++
	}
}

// BatchCounts is what ApplyBatch did. Gone counts events newly marked gone, Linked recaps linked
// by time.
type BatchCounts struct {
	Events, Recaps, RecapItems Counts
	Gone, Linked               int
	// Refused lists the events that could not be stored (see ValidateEvent). Nothing was stored for
	// them and the rest of the batch was applied.
	Refused []*InvalidEventError
}

// Batch is one source's contribution for one account: the events it read, the source ids it knows
// are gone, the days it verified, and its recaps and recap items. Window names the source and
// account; its range is recorded in calendar_sources.
type Batch struct {
	Window Window
	Events []Event
	// GoneSourceIDs are source ids the source knows no longer exist (deleted, cancelled or
	// declined away). They get removed_at and keep all their data. Masters are never marked gone,
	// and an event of this batch that appears in Events is live whatever this list says.
	GoneSourceIDs []string
	// CoveredDays are the dates (YYYY-MM-DD) the source verified in this batch; see RecordCoveredDays.
	CoveredDays []string
	Recaps      []Recap
	RecapItems  []RecapItem
}

// ApplyOptions tune ApplyBatch.
type ApplyOptions struct {
	// InferUnseenInWindow marks the source's live rows inside Window that Events no longer holds as
	// removed. Only a source that reads one contiguous range completely may set it.
	InferUnseenInWindow bool
	// SkipMatches keys every event by Key(e) directly and writes no calendar_matches rows. A source
	// whose ids are unique per occurrence (Teams) sets it.
	SkipMatches bool
	// RecapTolerance is how far recap and event times may differ for LinkRecaps; zero means
	// DefaultRecapTolerance.
	RecapTolerance time.Duration
}

// ApplySnapshot records one source's contiguous snapshot in its own transaction: it upserts
// events under their resolved keys, sets removed_at on that source's live rows inside the window
// that events no longer contain, and records the window. Timed rows are inside when their start
// is in [w.Start, w.End); all-day rows when their start date is within the window's UTC dates.
// An event that cannot be stored (ValidateEvent) is refused: nothing is stored for it, the other
// events are applied and committed, and the refused ones come back in the counts (Refused) with a
// nil error, so a stored snapshot is never retried or alerted on. Only a storage error returns one.
func ApplySnapshot(ctx context.Context, db *sql.DB, w Window, events []Event, at time.Time) (counts BatchCounts, err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return counts, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if counts, err = ApplyBatch(ctx, tx, Batch{Window: w, Events: events}, ApplyOptions{InferUnseenInWindow: true}, at); err != nil {
		return counts, err
	}
	return counts, tx.Commit()
}

// ApplySnapshotTx is ApplySnapshot inside the caller's transaction.
func ApplySnapshotTx(ctx context.Context, tx *sql.Tx, w Window, events []Event, at time.Time) (BatchCounts, error) {
	return ApplyBatch(ctx, tx, Batch{Window: w, Events: events}, ApplyOptions{InferUnseenInWindow: true}, at)
}

// ApplyBatch applies b inside the caller's transaction (SQLite allows one writer, and a sync
// writes everything for a source in one). Order: gone ids are marked, events are captured (an
// event of the batch is live even if listed gone), unseen rows are inferred gone when asked,
// covered days and the window are recorded, then recaps and items are applied and unlinked recaps
// are linked by time. It never deletes a row.
func ApplyBatch(ctx context.Context, tx *sql.Tx, b Batch, opts ApplyOptions, at time.Time) (BatchCounts, error) {
	var counts BatchCounts
	w := b.Window
	for _, e := range b.Events {
		if e.Source != w.Source {
			return counts, fmt.Errorf("calendar: event %q is from source %q, snapshot is for %q", e.SourceID, e.Source, w.Source)
		}
		if e.AccountID != w.AccountID {
			return counts, fmt.Errorf("calendar: event %q is from account %q, snapshot is for %q", e.SourceID, e.AccountID, w.AccountID)
		}
	}
	for _, r := range b.Recaps {
		if err := checkRecapRow(w, r.AccountID, r.CallID); err != nil {
			return counts, err
		}
	}
	for _, it := range b.RecapItems {
		if err := checkRecapRow(w, it.AccountID, it.CallID); err != nil {
			return counts, err
		}
	}
	gone, err := markGone(ctx, tx, w, b.GoneSourceIDs, at)
	if err != nil {
		return counts, err
	}
	counts.Gone = gone
	// Process in SourceID order so keys never depend on the order the adapter listed events in.
	// Refused events are set aside first: they are counted and returned, and the rest is applied.
	events := make([]Event, 0, len(b.Events))
	seen := make(map[string]bool, len(b.Events))
	for _, e := range b.Events {
		if err := ValidateEvent(e); err != nil {
			var bad *InvalidEventError
			errors.As(err, &bad)
			counts.Refused = append(counts.Refused, bad)
			continue
		}
		events = append(events, e)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].SourceID < events[j].SourceID })
	snap := newSnapshot(events)
	for _, e := range events {
		key := Key(e)
		if !opts.SkipMatches {
			if key, err = resolveKey(ctx, tx, e, snap); err != nil {
				return counts, err
			}
		}
		res, err := upsertEvent(ctx, tx, e, key, at)
		if err != nil {
			return counts, err
		}
		counts.Events.add(res)
		seen[key] = true
	}
	// Removals are always inferred, but a live row of the same source and source id as a refused
	// event is spared: the event is still reported, only its copy is unusable. A refused event with
	// no source id spares nothing and is only counted.
	if opts.InferUnseenInWindow {
		if err := spare(ctx, tx, w, counts.Refused, seen); err != nil {
			return counts, err
		}
		if err := markRemoved(ctx, tx, w, seen, at); err != nil {
			return counts, err
		}
	}
	if err := RecordCoveredDays(ctx, tx, w.Source, w.AccountID, b.CoveredDays, at); err != nil {
		return counts, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO calendar_sources (source, account_id, window_start, window_end, synced_at, cache_fresh_at) VALUES (?,?,?,?,?,?)
		 ON CONFLICT(source, account_id) DO UPDATE SET window_start=excluded.window_start, window_end=excluded.window_end,
		 synced_at=excluded.synced_at, cache_fresh_at=excluded.cache_fresh_at`,
		string(w.Source), w.AccountID, formatTime(w.Start), formatTime(w.End), formatTime(w.SyncedAt), formatTime(w.CacheFreshAt)); err != nil {
		return counts, err
	}
	for _, r := range b.Recaps {
		res, err := applyRecap(ctx, tx, r, at)
		if err != nil {
			return counts, err
		}
		counts.Recaps.add(res)
	}
	if counts.RecapItems, err = applyItems(ctx, tx, b.RecapItems, at); err != nil {
		return counts, err
	}
	tol := opts.RecapTolerance
	if tol == 0 {
		tol = DefaultRecapTolerance
	}
	counts.Linked, err = LinkRecaps(ctx, tx, w.AccountID, tol, at)
	return counts, err
}

func checkRecapRow(w Window, account, call string) error {
	if account != w.AccountID {
		return fmt.Errorf("calendar: recap row %q is from account %q, snapshot is for %q", call, account, w.AccountID)
	}
	if call == "" {
		return errors.New("calendar: recap row without a call id")
	}
	return nil
}

// markGone sets removed_at on the live, non-master rows of the window's source and account whose
// source id is listed. The rows keep all their data.
func markGone(ctx context.Context, tx *sql.Tx, w Window, ids []string, at time.Time) (int, error) {
	n := 0
	for _, id := range ids {
		rows, err := tx.QueryContext(ctx,
			`UPDATE calendar_source_events SET removed_at=? WHERE source=? AND account_id=? AND source_id=? AND removed_at IS NULL AND event_type<>?
			 RETURNING event_key`,
			formatTime(at), string(w.Source), w.AccountID, id, EventMaster)
		if err != nil {
			return n, err
		}
		keys, err := scanStrings(rows)
		if err != nil {
			return n, err
		}
		n += len(keys)
	}
	return n, nil
}

// RecordCoveredDays records that source verified days (YYYY-MM-DD) for the account at the given
// time. The table is cumulative: a day's first_verified_at never changes and no day is removed;
// last_verified_at moves forward, so a reader can say "as of three weeks ago".
func RecordCoveredDays(ctx context.Context, tx *sql.Tx, source Source, accountID string, days []string, at time.Time) error {
	for _, d := range days {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO calendar_covered_days (source, account_id, day, first_verified_at, last_verified_at) VALUES (?,?,?,?,?)
			 ON CONFLICT(source, account_id, day) DO UPDATE SET last_verified_at=excluded.last_verified_at`,
			string(source), accountID, d, formatTime(at), formatTime(at)); err != nil {
			return err
		}
	}
	return nil
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
// match for (source, account, source_id) always wins so keys never flip between syncs. Sources
// join only inside one account.
//
// Upgrade rule, symmetric so the result does not depend on which source syncs first: events with
// the same composite hash are joined across sources only when exactly one event on each side has
// that hash (and one of them has a global id and the other none). Any other count is ambiguous and
// nothing is joined. The counts see the whole current snapshot plus stored rows, so the decision
// is the same wherever e sits in the snapshot. A twin that first appears in a later sync cannot
// undo an upgrade already made: the requirement is "same snapshots, same agenda".
func resolveKey(ctx context.Context, tx *sql.Tx, e Event, snap snapshot) (string, error) {
	var key string
	err := tx.QueryRowContext(ctx, `SELECT event_key FROM calendar_matches WHERE source=? AND account_id=? AND source_id=?`,
		string(e.Source), e.AccountID, e.SourceID).Scan(&key)
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
	own, err := ownCount(ctx, tx, e, composite, snap)
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
		if err := rekey(ctx, tx, e, cands[0].key, key); err != nil {
			return "", err
		}
	case upgrade:
		key, method = cands[0].key, matchUpgraded
	case !global && own > 1:
		// Colliding twins all carry their source id; none owns the bare hash.
		key = composite + "#" + e.SourceID
	}
	if err := recordMatch(ctx, tx, key, e, method); err != nil {
		return "", err
	}
	return key, nil
}

// ownCount counts the distinct events of e's source and account that share composite: those in
// the snapshot, plus live stored rows that the snapshot does not mention.
func ownCount(ctx context.Context, tx *sql.Tx, e Event, composite string, snap snapshot) (int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT source_id FROM calendar_source_events WHERE source=? AND account_id=? AND composite_key=? AND removed_at IS NULL`,
		string(e.Source), e.AccountID, composite)
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

// otherRows lists the other sources' live rows of the same account with the composite hash.
// target is the key e is about to take: for an event with a global id, a row is blocked when its
// source already holds target; for one without, when this source already holds the row's key.
func otherRows(ctx context.Context, tx *sql.Tx, e Event, composite, target string) ([]otherRow, error) {
	query, blockedArg := compositeArrivalRows, string(e.Source)
	if e.GlobalID != "" {
		query, blockedArg = globalArrivalRows, target
	}
	rows, err := tx.QueryContext(ctx, query, blockedArg, string(e.Source), e.AccountID, composite)
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
const otherRowsTail = ` FROM calendar_source_events c WHERE c.source <> ? AND c.account_id = ? AND c.composite_key = ? AND c.removed_at IS NULL
  ORDER BY c.source, c.event_key`

const globalArrivalRows = otherRowsHead +
	`EXISTS (SELECT 1 FROM calendar_source_events m WHERE m.source = c.source AND m.account_id = c.account_id AND m.event_key = ?)` + otherRowsTail

const compositeArrivalRows = otherRowsHead +
	`EXISTS (SELECT 1 FROM calendar_source_events m WHERE m.source = ? AND m.account_id = c.account_id AND m.event_key = c.event_key)` + otherRowsTail

// rekey moves the other sources' row of e's account from key old to key to, and its match.
func rekey(ctx context.Context, tx *sql.Tx, self Event, old, to string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE calendar_source_events SET event_key=? WHERE source<>? AND account_id=? AND event_key=?`,
		to, string(self.Source), self.AccountID, old); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE calendar_matches SET event_key=?, match_method=? WHERE source<>? AND account_id=? AND event_key=?`,
		to, matchUpgraded, string(self.Source), self.AccountID, old)
	return err
}

func recordMatch(ctx context.Context, tx *sql.Tx, key string, e Event, method string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO calendar_matches (event_key, source, account_id, source_id, match_method) VALUES (?,?,?,?,?)`,
		key, string(e.Source), e.AccountID, e.SourceID, method)
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

var upsertSQL = func() string {
	names := []string{"event_key", "composite_key"}
	sets := make([]string, 0, len(eventColumns))
	for _, c := range eventColumns {
		names = append(names, c.name)
		switch c.name {
		case "source", "account_id", "first_seen_at":
		default:
			sets = append(sets, c.name+"=excluded."+c.name)
		}
	}
	return "INSERT INTO calendar_source_events (" + strings.Join(names, ",") + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") +
		") ON CONFLICT(source, account_id, event_key) DO UPDATE SET composite_key=excluded.composite_key," + strings.Join(sets, ",")
}()

// loadStored reads the full stored row of key, or nil when there is none.
func loadStored(ctx context.Context, tx *sql.Tx, e Event, key string) (*Event, error) {
	cols := selectColumns(true)
	rows, err := tx.QueryContext(ctx, selectSQL(cols, "source=? AND account_id=? AND event_key=?"), string(e.Source), e.AccountID, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rowsErr(rows)
	}
	k, err := scanKeyed(rows, cols)
	if err != nil {
		return nil, err
	}
	return &k.Event, nil
}

// upsertEvent captures e over the row stored under key (read in the same transaction) and writes
// the result. A row seen again is live again; first_seen_at is set once, and detail_seen_at
// moves whenever a copy that carries detail is seen.
func upsertEvent(ctx context.Context, tx *sql.Tx, e Event, key string, at time.Time) (outcome, error) {
	old, err := loadStored(ctx, tx, e, key)
	if err != nil {
		return 0, err
	}
	got, err := Capture(old, e)
	if err != nil {
		return 0, err
	}
	got.FirstSeenAt, got.SeenAt = at, at
	res := outcomeNew
	if old != nil {
		res = outcomeChanged
		got.FirstSeenAt = old.FirstSeenAt
		if storedEqual(withSeen(*old, at), withSeen(got, at)) {
			res = outcomeUnchanged
		}
	}
	if !detailEqual(Event{}, e) || e.DetailRawJSON != "" {
		got.DetailSeenAt = &at
	}
	args := []any{key, compositeKey(got)}
	for _, c := range eventColumns {
		args = append(args, c.val(got))
	}
	_, err = tx.ExecContext(ctx, upsertSQL, args...)
	return res, err
}

// withSeen sets the visit-only bookkeeping to at so two rows compare by what they hold.
func withSeen(e Event, at time.Time) Event {
	e.SeenAt, e.DetailSeenAt = at, nil
	return e
}

// markRemoved sets removed_at on the source's live rows of the window's account, inside the
// window, whose key is not seen. Masters are never marked.
// spare adds to seen the keys of the live rows that share source and source id with a refused
// event.
func spare(ctx context.Context, tx *sql.Tx, w Window, refused []*InvalidEventError, seen map[string]bool) error {
	for _, r := range refused {
		if r.SourceID == "" {
			continue
		}
		rows, err := tx.QueryContext(ctx, `SELECT event_key FROM calendar_source_events WHERE source=? AND account_id=? AND source_id=? AND removed_at IS NULL`,
			string(w.Source), w.AccountID, r.SourceID)
		if err != nil {
			return err
		}
		keys, err := scanStrings(rows)
		if err != nil {
			return err
		}
		for _, k := range keys {
			seen[k] = true
		}
	}
	return nil
}

func markRemoved(ctx context.Context, tx *sql.Tx, w Window, seen map[string]bool, at time.Time) error {
	firstDate := w.Start.UTC().Format(dateLayout)
	lastDate := w.End.UTC().Add(-time.Nanosecond).Format(dateLayout)
	rows, err := tx.QueryContext(ctx,
		`SELECT event_key FROM calendar_source_events WHERE source=? AND account_id=? AND removed_at IS NULL AND event_type<>? AND (
		   (COALESCE(all_day,0)=0 AND start_at >= ? AND start_at < ?) OR (all_day=1 AND start_date >= ? AND start_date <= ?))`,
		string(w.Source), w.AccountID, EventMaster, formatTime(w.Start), formatTime(w.End), firstDate, lastDate)
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
		if _, err := tx.ExecContext(ctx, `UPDATE calendar_source_events SET removed_at=? WHERE source=? AND account_id=? AND event_key=?`,
			formatTime(at), string(w.Source), w.AccountID, k); err != nil {
			return err
		}
	}
	return nil
}
