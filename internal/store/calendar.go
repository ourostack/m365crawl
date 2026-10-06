package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamscal"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// The omission codes the calendar derivation counts. A record that cannot be mapped and an event
// the core refuses are losses; an unknown zone name and an all-day event whose times fit no
// whole-day shape are not (the event is stored and shown, only less precisely).
const (
	OmitCalendarUnmapped        = "calendar_unmapped"
	OmitCalendarRefused         = "calendar_refused"
	OmitCalendarUnknownZone     = "calendar_unknown_time_zone"
	OmitCalendarAllDayUnaligned = "calendar_all_day_unaligned"
)

// calendarMetaKey is the meta row that remembers which mapper and which scrub rules the calendar
// tables were derived under.
const calendarMetaKey = "calendar_derivation"

// CalendarCounts is what a derivation did to the calendar tables. Seen counts the rows it mapped
// and applied; Gone the events it newly marked as gone; Linked the recaps it linked to an event by
// time; Refused the events the core could not store.
type CalendarCounts struct {
	Events     Counts `json:"events"`
	Recaps     Counts `json:"recaps"`
	RecapItems Counts `json:"recap_items"`
	Gone       int    `json:"gone"`
	Linked     int    `json:"linked"`
	Refused    int    `json:"refused"`
}

// Add adds b to c.
func (c *CalendarCounts) Add(b CalendarCounts) {
	for _, p := range [][2]*Counts{{&c.Events, &b.Events}, {&c.Recaps, &b.Recaps}, {&c.RecapItems, &b.RecapItems}} {
		p[0].Seen += p[1].Seen
		p[0].Inserted += p[1].Inserted
		p[0].Updated += p[1].Updated
		p[0].Unchanged += p[1].Unchanged
	}
	c.Gone += b.Gone
	c.Linked += b.Linked
	c.Refused += b.Refused
}

// CalendarResult is a derivation's counts and the omissions it counted by code.
type CalendarResult struct {
	Counts    CalendarCounts
	Omissions map[string]int
}

func (r *CalendarResult) omit(code string, n int) {
	if n > 0 {
		r.Omissions[code] += n
	}
}

// Test seams: the scrub and denylist the replace mode applies.
var (
	calendarScrub  = teamsdesktop.Scrub
	calendarDenied = teamsdesktop.Denied
)

// calendarMode says which rows a derivation maps and how it writes them.
type calendarMode struct {
	source  string // only this records source; empty means every source
	since   string // only rows updated at or after this time (store text); empty means every row
	replace bool   // blank the derived rows first, so nothing of an older copy survives
	zone    *time.Location
	at      time.Time
}

// calGroup is one source's rows for one account.
type calGroup struct {
	source      string
	acct        teamsdesktop.Account
	hasCalendar bool
	days        map[string]struct{} // days the cache holds now
	gone        []string
	events      []calendar.Event
	recaps      []calendar.Recap
	items       []calendar.RecapItem
	stamp       time.Time
}

type calGroupKey struct{ source, tenant, user string }

// calendarStoreNames is the SQL list of the claimed object stores' names.
func calendarStoreNames() (list string, args []any) {
	seen := map[string]bool{}
	for _, c := range teamscal.ClaimedStores() {
		if !seen[c.Store] {
			seen[c.Store] = true
			args = append(args, c.Store)
		}
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")", args
}

// DeriveCalendar maps the calendar and recap records this sync wrote (rows of source updated at or
// after at), marks the events the Teams cache no longer holds on a day it still holds, applies
// the result to the calendar tables and links recaps to events, all inside the session's
// transaction. Days are taken in zone. It reads only the records table, so it needs the cache
// for nothing and does not depend on how many records the sync read.
func (x *Session) DeriveCalendar(ctx context.Context, source string, at time.Time, zone *time.Location) (CalendarResult, error) {
	return deriveCalendar(ctx, x.tx, calendarMode{source: source, since: at.UTC().Format(timeLayout), zone: zone, at: at})
}

// EnsureCalendar derives the whole calendar from the archived records when the mapper or the scrub
// rules it was derived under are not the ones this build has (including never: an archive from
// before the calendar tables). A changed mapper maps every record and captures the result over
// what is stored; changed scrub rules also blank the derived rows first and rebuild them from the
// records, so nothing an older rule let through survives in a derived copy. It returns nil when
// nothing was due.
func (s *Store) EnsureCalendar(ctx context.Context, zone *time.Location, at time.Time) (*CalendarResult, error) {
	var res *CalendarResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var stored string
		switch err := tx.QueryRowContext(ctx, `select value from meta where key=?`, calendarMetaKey).Scan(&stored); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		}
		want := calendarDerivation(teamscal.MapperVersion, teamsdesktop.RulesStamp())
		if stored == want {
			return nil
		}
		rulesChanged := stored != "" && !strings.HasSuffix(stored, ";"+calendarRules(teamsdesktop.RulesStamp()))
		r, err := deriveCalendar(ctx, tx, calendarMode{replace: rulesChanged, zone: zone, at: at})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, calendarMetaKey, want); err != nil {
			return err
		}
		res = &r
		return nil
	})
	return res, err
}

func calendarRules(stamp string) string { return "rules=" + stamp }

func calendarDerivation(mapper int, stamp string) string {
	return fmt.Sprintf("mapper=%d;%s", mapper, calendarRules(stamp))
}

// recordKey is the text a record key mappers see: the string a JSON string key holds, or the key's
// JSON text.
func recordKey(keyJSON string) string {
	var s string
	if json.Unmarshal([]byte(keyJSON), &s) == nil {
		return s
	}
	return keyJSON
}

func deriveCalendar(ctx context.Context, tx *sql.Tx, m calendarMode) (CalendarResult, error) {
	res := CalendarResult{Omissions: map[string]int{}}
	if m.replace {
		if err := blankCalendar(ctx, tx, m.at); err != nil {
			return res, err
		}
	}
	groups := map[calGroupKey]*calGroup{}
	group := func(source, database string) (*calGroup, string, bool) {
		manager, acct, ok := teamsdesktop.ParseDatabaseName(database)
		if !ok {
			return nil, "", false
		}
		acct.Locale = ""
		k := calGroupKey{source, acct.TenantID, acct.UserID}
		g := groups[k]
		if g == nil {
			g = &calGroup{source: source, acct: acct, days: map[string]struct{}{}}
			groups[k] = g
		}
		return g, manager, true
	}
	var srcArgs []any
	srcSQL := ""
	if m.source != "" {
		srcSQL, srcArgs = ` and source=?`, []any{m.source}
	}
	names, nameArgs := calendarStoreNames()

	// Every claimed database: it makes its account a group, and says whether the account has a
	// calendar database at all.
	if err := eachRow(ctx, tx, `select distinct source, database from records where store in `+names+srcSQL, append(nameArgs, srcArgs...), func(r *sql.Rows) error {
		var source, database string
		if err := r.Scan(&source, &database); err != nil {
			return err
		}
		if g, manager, ok := group(source, database); ok && manager == "calendar" {
			g.hasCalendar = true
		}
		return nil
	}); err != nil {
		return res, err
	}

	// The days the cache holds now come from the live calendar rows by SQL, so the answer does not
	// depend on which records this sync happened to read.
	if err := eachRow(ctx, tx, `select source, database, json_extract(value_json,'$.startTime'), json_extract(value_json,'$.eventType') from records where store='calendar' and removed_at is null and value_json is not null`+srcSQL, srcArgs, func(r *sql.Rows) error {
		var source, database string
		var start, typ sql.NullString
		if err := r.Scan(&source, &database, &start, &typ); err != nil {
			return err
		}
		g, manager, ok := group(source, database)
		if day, has := teamscal.EventDay(start.String, typ.String, m.zone); ok && manager == "calendar" && has {
			g.days[day] = struct{}{}
		}
		return nil
	}); err != nil {
		return res, err
	}

	// A removed calendar record whose day the cache still holds was deleted, cancelled or declined
	// away; one whose day is gone from the cache was only evicted, and its event stays live.
	type removedRow struct {
		g   *calGroup
		key string
		day string
	}
	var removed []removedRow
	if err := eachRow(ctx, tx, `select source, database, key_json, json_extract(value_json,'$.startTime'), json_extract(value_json,'$.eventType') from records where store='calendar' and removed_at is not null and value_json is not null`+srcSQL, srcArgs, func(r *sql.Rows) error {
		var source, database, key string
		var start, typ sql.NullString
		if err := r.Scan(&source, &database, &key, &start, &typ); err != nil {
			return err
		}
		g, manager, ok := group(source, database)
		if day, has := teamscal.EventDay(start.String, typ.String, m.zone); ok && manager == "calendar" && has {
			removed = append(removed, removedRow{g, recordKey(key), day})
		}
		return nil
	}); err != nil {
		return res, err
	}
	for _, rm := range removed {
		if _, loaded := rm.g.days[rm.day]; loaded {
			rm.g.gone = append(rm.g.gone, rm.key)
		}
	}

	// The cache's own "last successful sync" time.
	if err := eachRow(ctx, tx, `select source, database, value_json from records where store='calendar-internal-data' and key_json='"lastSuccesfulSyncTimestamp"' and value_json is not null`+srcSQL, srcArgs, func(r *sql.Rows) error {
		var source, database, value string
		if err := r.Scan(&source, &database, &value); err != nil {
			return err
		}
		if g, _, ok := group(source, database); ok {
			g.stamp, _ = teamscal.SyncStamp([]byte(value))
		}
		return nil
	}); err != nil {
		return res, err
	}

	// The rows to map.
	q, args := `select source, database, store, key_json, value_json from records where store in `+names+` and value_json is not null`+srcSQL, append(slices.Clone(nameArgs), srcArgs...)
	if m.since != "" {
		q, args = q+` and updated_at>=?`, append(args, m.since)
	}
	if err := eachRow(ctx, tx, q, args, func(r *sql.Rows) error {
		var source, database, store, key, value string
		if err := r.Scan(&source, &database, &store, &key, &value); err != nil {
			return err
		}
		g, manager, ok := group(source, database)
		kind, claimed := teamscal.Claimed(manager, store)
		if !ok || !claimed {
			return nil
		}
		raw := []byte(value)
		if m.replace {
			if calendarDenied(database) || calendarDenied(store) {
				return nil
			}
			raw, _ = calendarScrub(raw)
		}
		g.mapRecord(&res, kind, recordKey(key), raw, m.zone)
		return nil
	}); err != nil {
		return res, err
	}

	keys := make([]calGroupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.source != b.source {
			return a.source < b.source
		}
		if a.tenant != b.tenant {
			return a.tenant < b.tenant
		}
		return a.user < b.user
	})
	for _, k := range keys {
		g := groups[k]
		if !g.hasCalendar && len(g.events)+len(g.recaps)+len(g.items)+len(g.gone) == 0 {
			continue // an account with no calendar database and nothing to apply gets no row
		}
		counts, err := g.apply(ctx, tx, m)
		if err != nil {
			return res, err
		}
		res.Counts.Add(counts)
		res.omit(OmitCalendarRefused, counts.Refused)
	}
	if m.replace {
		// A derived event with no record left to rebuild it from (its store was denied) keeps
		// nothing and reads as removed.
		if _, err := tx.ExecContext(ctx, `update calendar_source_events set removed_at=coalesce(removed_at, ?) where source=? and detail_raw_json=''`,
			m.at.UTC().Format(timeLayout), string(calendar.SourceTeams)); err != nil {
			return res, err
		}
	}
	return res, nil
}

// mapRecord maps one record into the group, counting what it cannot map.
func (g *calGroup) mapRecord(res *CalendarResult, kind teamscal.Kind, key string, raw []byte, zone *time.Location) {
	switch kind {
	case teamscal.KindEvent:
		e, notes, err := teamscal.MapEventRecord(g.acct, key, raw, zone)
		if err != nil {
			res.omit(OmitCalendarUnmapped, 1)
			return
		}
		if notes.UnknownZone != "" {
			res.omit(OmitCalendarUnknownZone, 1)
		}
		if notes.AllDayUnaligned {
			res.omit(OmitCalendarAllDayUnaligned, 1)
		}
		g.events = append(g.events, e)
	case teamscal.KindCatchUp:
		recaps, items, notes, err := teamscal.MapCatchUpRecord(g.acct, key, raw)
		res.omit(OmitCalendarUnmapped, notes.Skipped+notes.SkippedItems)
		if err != nil {
			res.omit(OmitCalendarUnmapped, 1)
			return
		}
		g.recaps, g.items = append(g.recaps, recaps...), append(g.items, items...)
	case teamscal.KindRecap:
		recap, items, notes, err := teamscal.MapRecapRecord(g.acct, key, raw)
		res.omit(OmitCalendarUnmapped, notes.Skipped+notes.SkippedItems)
		if err != nil {
			res.omit(OmitCalendarUnmapped, 1)
			return
		}
		g.recaps, g.items = append(g.recaps, recap), append(g.items, items...)
	}
}

// apply writes one group through the core. The window is the span of every day the cache ever
// showed for the account, so it never shrinks when a later sync sees fewer days.
func (g *calGroup) apply(ctx context.Context, tx *sql.Tx, m calendarMode) (CalendarCounts, error) {
	var counts CalendarCounts
	account := teamscal.AccountID(g.acct)
	days := make([]string, 0, len(g.days))
	for d := range g.days {
		days = append(days, d)
	}
	sort.Strings(days)
	var first, last sql.NullString
	var known int
	if err := tx.QueryRowContext(ctx, `select min(day), max(day), count(*) from calendar_covered_days where source=? and account_id=?`,
		string(calendar.SourceTeams), account).Scan(&first, &last, &known); err != nil {
		return counts, err
	}
	lo, hi := first.String, last.String
	if len(days) > 0 {
		if lo == "" || days[0] < lo {
			lo = days[0]
		}
		if hi == "" || days[len(days)-1] > hi {
			hi = days[len(days)-1]
		}
	}
	w := calendar.Window{Source: calendar.SourceTeams, AccountID: account, SyncedAt: m.at}
	if lo != "" {
		start, _ := time.ParseInLocation(time.DateOnly, lo, m.zone)
		end, _ := time.ParseInLocation(time.DateOnly, hi, m.zone)
		w.Start, w.End = start, end.AddDate(0, 0, 1)
	}
	fresh, err := g.freshAt(ctx, tx, account, m.at)
	if err != nil {
		return counts, err
	}
	w.CacheFreshAt = fresh
	// The core marks the ids it is given as gone before it captures the batch's events, and an event
	// of the batch is live whatever the list says. A removed record that is mapped here (a
	// backfill maps every row) must end up gone, so the gone ids are applied after the events.
	opts := calendar.ApplyOptions{SkipMatches: true, RecapTolerance: teamscal.RecapTimeTolerance}
	bc, err := calendar.ApplyBatch(ctx, tx, calendar.Batch{Window: w, Events: g.events, CoveredDays: days, Recaps: g.recaps, RecapItems: g.items}, opts, m.at)
	if err != nil {
		return counts, err
	}
	counts = CalendarCounts{Events: toCounts(bc.Events), Recaps: toCounts(bc.Recaps), RecapItems: toCounts(bc.RecapItems), Gone: bc.Gone, Linked: bc.Linked, Refused: len(bc.Refused)}
	if len(g.gone) > 0 {
		gc, err := calendar.ApplyBatch(ctx, tx, calendar.Batch{Window: w, GoneSourceIDs: g.gone}, opts, m.at)
		if err != nil {
			return counts, err
		}
		counts.Gone += gc.Gone
	}
	return counts, nil
}

// freshAt is when the cache last synced with the calendar service: its own timestamp when it has
// one, else the newest modification time of an event of the account, else (for an account that
// has a calendar database) the time of this sync.
func (g *calGroup) freshAt(ctx context.Context, tx *sql.Tx, account string, at time.Time) (time.Time, error) {
	if !g.stamp.IsZero() {
		return g.stamp, nil
	}
	var newest sql.NullString
	var events int
	if err := tx.QueryRowContext(ctx, `select max(last_modified), count(*) from calendar_source_events where source=? and account_id=?`,
		string(calendar.SourceTeams), account).Scan(&newest, &events); err != nil {
		return time.Time{}, err
	}
	t, _ := time.Parse(timeLayout, newest.String)
	for _, e := range g.events {
		if e.LastModified != nil && e.LastModified.After(t) {
			t = *e.LastModified
		}
	}
	switch {
	case !t.IsZero():
		return t, nil
	case g.hasCalendar:
		return at, nil
	}
	return time.Time{}, nil
}

func toCounts(c calendar.Counts) Counts {
	return Counts{Seen: c.New + c.Changed + c.Unchanged, Inserted: c.New, Updated: c.Changed, Unchanged: c.Unchanged}
}

// eachRow runs q and calls fn for each row.
func eachRow(ctx context.Context, tx *sql.Tx, q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, q, args...) //nolint:gosec // G202: q is built from package constants and placeholders
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// blankCalendar empties the content of every derived row, keeping each row's key and its first
// sighting. It never deletes: events keep removed_at, and the items blanked here are marked
// superseded so a reader never shows an empty one. The rows that the records still back are
// rebuilt by the derivation that follows.
func blankCalendar(ctx context.Context, tx *sql.Tx, at time.Time) error {
	stamp := at.UTC().Format(timeLayout)
	for _, t := range []struct {
		table string
		keep  []string
	}{
		{"calendar_source_events", []string{"composite_key", "source_id", "first_seen_at", "seen_at", "removed_at"}},
		{"calendar_recaps", []string{"first_seen_at", "updated_at"}},
		{"calendar_recap_items", []string{"kind", "origin", "first_seen_at", "updated_at", "superseded_at"}},
	} {
		var sets []string
		if err := eachRow(ctx, tx, `select name, dflt_value from pragma_table_info(?) where pk=0`, []any{t.table}, func(r *sql.Rows) error {
			var name string
			var dflt sql.NullString
			if err := r.Scan(&name, &dflt); err != nil {
				return err
			}
			for _, k := range t.keep {
				if k == name {
					return nil
				}
			}
			switch {
			case dflt.Valid:
				sets = append(sets, name+"="+dflt.String)
			default:
				sets = append(sets, name+"=NULL")
			}
			return nil
		}); err != nil {
			return err
		}
		if t.table == "calendar_recap_items" {
			sets = append(sets, "superseded_at=coalesce(superseded_at, '"+stamp+"')")
		}
		if _, err := tx.ExecContext(ctx, `update `+t.table+` set `+strings.Join(sets, ", ")); err != nil { //nolint:gosec // G202: names come from pragma_table_info of a package-owned table
			return err
		}
	}
	return nil
}

// CalendarCache is what the doctor needs to judge the calendar cache: how many accounts the
// archive holds, how many of them have no Teams calendar database in it, whether the calendar
// tables exist, and the newest time any account's calendar cache was fresh. It holds no event
// content.
type CalendarCache struct {
	Accounts        int
	WithoutDatabase int
	HasTables       bool
	FreshAt         time.Time
}

// CalendarCache reads the calendar cache state.
func (s *Store) CalendarCache(ctx context.Context) (CalendarCache, error) {
	var c CalendarCache
	if err := s.db.QueryRowContext(ctx, `select count(*), count(case when not exists (select 1 from records r where r.tenant_id=a.tenant_id and r.user_id=a.user_id and r.database like 'Teams:calendar:%') then 1 end) from accounts a`).
		Scan(&c.Accounts, &c.WithoutDatabase); err != nil {
		return c, err
	}
	var tables int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='calendar_sources'`).Scan(&tables); err != nil {
		return c, err
	}
	if c.HasTables = tables == 1; !c.HasTables {
		return c, nil
	}
	var newest sql.NullString
	var rows int
	if err := s.db.QueryRowContext(ctx, `select max(cache_fresh_at), count(*) from calendar_sources where cache_fresh_at<>''`).Scan(&newest, &rows); err != nil {
		return c, err
	}
	c.FreshAt = parseTime(newest)
	return c, nil
}
