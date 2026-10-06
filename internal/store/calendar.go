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
	// OmitCalendarFailed counts a derivation that failed and was rolled back; the sync's other
	// data was kept, and the next sync derives again.
	OmitCalendarFailed = "calendar_derivation_failed"
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
	// LinkedByStart, UnlinkedNoEvent and UnlinkedAmbiguous are a census after the derivation, summed over
	// the accounts it read: the recaps linked by meeting start, and the recaps with a meeting start and
	// no iCalUID that stay unlinked because no event, or several, start within five minutes. They are
	// not what this run changed: start links are recomputed from the archive on every derivation.
	LinkedByStart     int `json:"linked_by_start"`
	UnlinkedNoEvent   int `json:"unlinked_no_event"`
	UnlinkedAmbiguous int `json:"unlinked_ambiguous"`
	Refused           int `json:"refused"`
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
	c.LinkedByStart += b.LinkedByStart
	c.UnlinkedNoEvent += b.UnlinkedNoEvent
	c.UnlinkedAmbiguous += b.UnlinkedAmbiguous
	c.Refused += b.Refused
}

// CalendarResult is a derivation's counts and the omissions it counted by code.
type CalendarResult struct {
	Counts    CalendarCounts
	Omissions map[string]int
	losses    []loss
	// failure is the error a rolled-back derivation was counted for; the omission is what readers see.
	failure error
}

func (r *CalendarResult) omit(code string, n int) {
	if n > 0 {
		r.Omissions[code] += n
	}
}

// Test seams: the scrub and denylist the replace mode applies, and the event mapper (which
// validates what it maps, so only a seam reaches the core's own refusal).
var (
	calendarMapEvent = teamscal.MapEventRecord
	calendarScrub    = teamsdesktop.Scrub
	calendarDenied   = teamsdesktop.Denied
)

// calendarMode says which rows a derivation maps and how it writes them.
type calendarMode struct {
	source  string // only this records source; empty means every source
	since   string // only rows updated at or after this time (store text); empty means every row
	replace bool   // blank the derived rows first, so nothing of an older copy survives
	zone    *time.Location
	at      time.Time
	// read is the accounts whose Teams cache this derivation read, as {tenant, user}. Only they get
	// a fresh window and fresh covered days; nil means every account (a rebuild from the records
	// reads no cache, and stamps nothing it did not see).
	read map[[2]string]bool
	// retake takes the days of removed records too (not only live ones) as covered days: a zone
	// change forgets the covered days, and a day whose events were evicted must stay covered.
	retake bool
}

// calGroup is one source's rows for one account.
type calGroup struct {
	source      string
	acct        teamsdesktop.Account
	hasCalendar bool
	days        map[string]struct{} // days the cache holds now
	retaken     map[string]struct{} // days of removed records, taken again after a zone change
	gone        []string
	events      []calendar.Event
	recaps      []calendar.Recap
	items       []calendar.RecapItem
	stamp       time.Time
	rows        map[string]recordRef // the record each event came from, by source id
}

// recordRef names one record of the records table.
type recordRef struct{ source, database, store, key string }

// loss is a record (or part of one) the derivation could not use, kept until the record is
// mapped again so every sync can count what is still missing.
type loss struct {
	ref    recordRef
	kind   string
	reason string
	n      int
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
//
// present is the databases the sync found in the cache; the accounts they belong to are the ones
// it read, and the only ones whose freshness it moves. A failure in the derivation is rolled back
// to a savepoint and counted as the omission calendar_derivation_failed, so it never fails the
// source's message sync.
func (x *Session) DeriveCalendar(ctx context.Context, source string, at time.Time, zone *time.Location, present []string) (CalendarResult, error) {
	read := map[[2]string]bool{}
	for _, d := range present {
		if _, a, ok := teamsdesktop.ParseDatabaseName(d); ok {
			read[[2]string{a.TenantID, a.UserID}] = true
		}
	}
	res, err := isolated(ctx, x.tx, func() (CalendarResult, error) {
		return deriveCalendar(ctx, x.tx, calendarMode{source: source, since: at.UTC().Format(timeLayout), zone: zone, at: at, read: read})
	})
	if err == nil && res.failure != nil {
		// The records of this sync are kept but were not derived, and a later sync maps only what
		// it writes: forget the stamp, so the next run derives the whole calendar again.
		_, err = x.tx.ExecContext(ctx, `delete from meta where key=?`, calendarMetaKey)
	}
	return res, err
}

// isolated runs fn in a savepoint. When fn fails the savepoint is rolled back and the failure is a
// counted loss; only a failure to manage the savepoint itself is returned.
func isolated(ctx context.Context, tx *sql.Tx, fn func() (CalendarResult, error)) (CalendarResult, error) {
	if _, err := tx.ExecContext(ctx, `savepoint calendar_derive`); err != nil {
		return CalendarResult{}, err
	}
	res, err := fn()
	if err != nil {
		if _, rerr := tx.ExecContext(ctx, `rollback to savepoint calendar_derive`); rerr != nil {
			return CalendarResult{}, rerr
		}
		if _, rerr := tx.ExecContext(ctx, `release savepoint calendar_derive`); rerr != nil {
			return CalendarResult{}, rerr
		}
		return CalendarResult{Omissions: map[string]int{OmitCalendarFailed: 1}, failure: err}, nil
	}
	if _, err := tx.ExecContext(ctx, `release savepoint calendar_derive`); err != nil {
		return CalendarResult{}, err
	}
	return res, nil
}

// CalendarLosses counts the records the calendar derivation could not use and still cannot, by
// omission code, for the accounts of source (every account when account is nil). It reads what
// each derivation recorded, so it needs no cache and costs one query.
func (s *Store) CalendarLosses(ctx context.Context, source string, account *teamsdesktop.Account) (map[string]int, error) {
	return currentLosses(ctx, s.db, source, func(tenant, user string) bool {
		return account == nil || (account.TenantID == tenant && account.UserID == user)
	})
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// currentLosses counts, by omission code, the stored losses of live records of source (every
// source when empty) whose account keep accepts.
func currentLosses(ctx context.Context, db queryer, source string, keep func(tenant, user string) bool) (map[string]int, error) {
	out := map[string]int{}
	q := `select r.tenant_id, r.user_id, l.kind, coalesce(sum(l.n),0) from calendar_losses l join records r
	  on r.source=l.source and r.database=l.database and r.store=l.store and r.key_json=l.key_json
	  where r.removed_at is null and r.value_json is not null and (?='' or l.source=?) group by r.tenant_id, r.user_id, l.kind`
	rows, err := db.QueryContext(ctx, q, source, source)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tenant, user, kind string
		var n int
		if err := rows.Scan(&tenant, &user, &kind, &n); err != nil {
			return nil, err
		}
		if keep(tenant, user) {
			out[lossCodes[kind]] += n
		}
	}
	return out, rows.Err()
}

// recordLosses replaces the stored losses of every record this derivation mapped with what the
// mapping found, so a record that maps cleanly now loses its old losses.
func recordLosses(ctx context.Context, tx *sql.Tx, m calendarMode, mapped []recordRef, losses []loss) error {
	if m.replace {
		if _, err := tx.ExecContext(ctx, `delete from calendar_losses`); err != nil {
			return err
		}
	}
	del, err := tx.PrepareContext(ctx, `delete from calendar_losses where source=? and database=? and store=? and key_json=?`)
	if err != nil {
		return err
	}
	defer func() { _ = del.Close() }()
	for _, r := range mapped {
		if _, err := del.ExecContext(ctx, r.source, r.database, r.store, r.key); err != nil {
			return err
		}
	}
	for _, l := range losses {
		if _, err := tx.ExecContext(ctx, `insert into calendar_losses(source, database, store, key_json, kind, reason, n) values(?,?,?,?,?,?,?)
		  on conflict(source, database, store, key_json, kind, reason) do update set n=n+excluded.n`, l.ref.source, l.ref.database, l.ref.store, l.ref.key, l.kind, l.reason, l.n); err != nil {
			return err
		}
	}
	return nil
}

// countLosses adds the losses that still stand, not only the ones this derivation found, to the
// omissions: a record that cannot be used stays missing until it changes.
func countLosses(ctx context.Context, tx *sql.Tx, m calendarMode, res *CalendarResult) error {
	cur, err := currentLosses(ctx, tx, m.source, func(tenant, user string) bool { return m.read == nil || m.read[[2]string{tenant, user}] })
	if err != nil {
		return err
	}
	for code, n := range cur {
		res.omit(code, n)
	}
	return nil
}

// lossCodes maps a stored loss kind to the omission code it is reported as.
var lossCodes = map[string]string{lossUnmapped: OmitCalendarUnmapped, lossRefused: OmitCalendarRefused}

const (
	lossUnmapped = "unmapped"
	lossRefused  = "refused"
)

// EnsureCalendar derives the whole calendar from the archived records when the mapper, the scrub
// rules or the time zone it was derived under are not the ones this build has (including never:
// an archive from before the calendar tables), or when the stamp is current but the calendar
// tables are empty while calendar records exist. A changed mapper maps every record and captures
// the result over what is stored; changed scrub rules also blank the derived rows first and
// rebuild them from the records, so nothing an older rule let through survives in a derived copy.
// A changed zone also forgets the covered days, which were taken in the old zone and never
// shrink, and takes them again. A rebuild that fails is rolled back, counted as the omission
// calendar_derivation_failed and tried again by the next sync. It returns nil when nothing was due.
func (s *Store) EnsureCalendar(ctx context.Context, zone *time.Location, at time.Time) (*CalendarResult, error) {
	var res *CalendarResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var stored string
		switch err := tx.QueryRowContext(ctx, `select value from meta where key=?`, calendarMetaKey).Scan(&stored); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		}
		want := calendarDerivation(teamscal.MapperVersion, zoneStamp(zone), teamsdesktop.RulesStamp())
		if stored == want {
			empty, err := calendarLost(ctx, tx)
			if err != nil || !empty {
				return err
			}
		}
		rulesChanged := stored != "" && !strings.HasSuffix(stored, ";"+calendarRules(teamsdesktop.RulesStamp()))
		zoneChanged := stored != "" && !strings.Contains(stored, ";"+calendarZone(zoneStamp(zone))+";")
		r, err := isolated(ctx, tx, func() (CalendarResult, error) {
			if zoneChanged {
				if _, err := tx.ExecContext(ctx, `delete from calendar_covered_days where source=?`, string(calendar.SourceTeams)); err != nil {
					return CalendarResult{}, err
				}
			}
			return deriveCalendar(ctx, tx, calendarMode{replace: rulesChanged, zone: zone, at: at, retake: zoneChanged})
		})
		if err != nil {
			return err
		}
		res = &r
		if r.Omissions[OmitCalendarFailed] > 0 {
			return nil // no stamp: the next sync derives again
		}
		_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, calendarMetaKey, want)
		return err
	})
	return res, err
}

// calendarLost reports that the calendar tables hold nothing although calendar records exist: the
// derivation was lost (a restore, a deleted table) while its stamp stayed.
func calendarLost(ctx context.Context, tx *sql.Tx) (bool, error) {
	names, args := calendarStoreNames()
	var records, derived int
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from records where store in `+names+` and value_json is not null)`, args...).Scan(&records); err != nil {
		return false, err
	}
	if records == 0 {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from calendar_source_events) or exists(select 1 from calendar_recaps) or exists(select 1 from calendar_sources)`).Scan(&derived); err != nil {
		return false, err
	}
	return derived == 0, nil
}

func calendarRules(stamp string) string { return "rules=" + stamp }

func calendarZone(stamp string) string { return "zone=" + stamp }

// calendarDerivation is the stamp of what the calendar tables were derived under. The rules come
// last: a change of them alone is told by the suffix.
func calendarDerivation(mapper int, zone, stamp string) string {
	return fmt.Sprintf("mapper=%d;%s;%s", mapper, calendarZone(zone), calendarRules(stamp))
}

// zoneStamp names a zone by what it does, because time.Local is only ever called "Local": its
// offsets in the middle of January and July of a fixed year, which change when the machine moves
// to another zone (or another daylight saving rule).
func zoneStamp(z *time.Location) string {
	if z == nil {
		z = time.UTC
	}
	_, jan := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC).In(z).Zone()
	_, jul := time.Date(2024, 7, 15, 12, 0, 0, 0, time.UTC).In(z).Zone()
	return fmt.Sprintf("%d/%d", jan, jul)
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
	// depend on which records this sync happened to read. The live rows of every source are read,
	// because two profiles can hold one account: an event is live while any source holds it.
	live := map[[2]string]map[string]bool{} // account -> record keys some source holds live
	if err := eachRow(ctx, tx, `select source, database, key_json, json_extract(value_json,'$.startTime'), json_extract(value_json,'$.eventType') from records where store='calendar' and removed_at is null and value_json is not null`, nil, func(r *sql.Rows) error {
		var source, database, key string
		var start, typ sql.NullString
		if err := r.Scan(&source, &database, &key, &start, &typ); err != nil {
			return err
		}
		manager, acct, ok := teamsdesktop.ParseDatabaseName(database)
		day, has := teamscal.EventDay(start.String, typ.String, m.zone)
		if !ok || manager != "calendar" || !has {
			return nil
		}
		a := [2]string{acct.TenantID, acct.UserID}
		if live[a] == nil {
			live[a] = map[string]bool{}
		}
		live[a][recordKey(key)] = true
		if m.source == "" || source == m.source {
			g, _, _ := group(source, database)
			g.days[day] = struct{}{}
		}
		return nil
	}); err != nil {
		return res, err
	}

	// A removed calendar record whose day the cache still holds was deleted, cancelled or declined
	// away; one whose day is gone from the cache was only evicted, and its event stays live. An
	// event is gone only when no source that holds its account has it live.
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
			if m.retake {
				if g.retaken == nil {
					g.retaken = map[string]struct{}{}
				}
				g.retaken[day] = struct{}{}
			}
		}
		return nil
	}); err != nil {
		return res, err
	}
	for _, rm := range removed {
		if _, loaded := rm.g.days[rm.day]; loaded && !live[[2]string{rm.g.acct.TenantID, rm.g.acct.UserID}][rm.key] {
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
	var mapped []recordRef
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
		ref := recordRef{source, database, store, key}
		mapped = append(mapped, ref)
		g.mapRecord(&res, ref, kind, recordKey(key), raw, m.zone)
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
		if m.read != nil && !m.read[[2]string{g.acct.TenantID, g.acct.UserID}] {
			continue // its cache was not read: nothing here says it is fresher than it was
		}
		counts, refused, err := g.apply(ctx, tx, m)
		if err != nil {
			return res, err
		}
		res.Counts.Add(counts)
		for _, bad := range refused {
			if ref, ok := g.rows[bad.SourceID]; ok {
				res.losses = append(res.losses, loss{ref, lossRefused, bad.Reason, 1})
			}
		}
	}
	if err := recordLosses(ctx, tx, m, mapped, res.losses); err != nil {
		return res, err
	}
	if err := countLosses(ctx, tx, m, &res); err != nil {
		return res, err
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

// mapRecord maps one record into the group. What it cannot use becomes a loss of that record.
func (g *calGroup) mapRecord(res *CalendarResult, ref recordRef, kind teamscal.Kind, key string, raw []byte, zone *time.Location) {
	lose := func(reason string, n int) {
		if n > 0 {
			res.losses = append(res.losses, loss{ref, lossUnmapped, reason, n})
		}
	}
	switch kind {
	case teamscal.KindEvent:
		e, notes, err := calendarMapEvent(g.acct, key, raw, zone)
		if err != nil {
			lose(unmappedReason(err), 1)
			return
		}
		if notes.UnknownZone != "" {
			res.omit(OmitCalendarUnknownZone, 1)
		}
		if notes.AllDayUnaligned {
			res.omit(OmitCalendarAllDayUnaligned, 1)
		}
		if g.rows == nil {
			g.rows = map[string]recordRef{}
		}
		g.rows[e.SourceID] = ref
		g.events = append(g.events, e)
	case teamscal.KindCatchUp:
		recaps, items, notes, err := teamscal.MapCatchUpRecord(g.acct, key, raw)
		lose("catch-up item without a callId", notes.Skipped)
		lose("catch-up task or mention that is not an object", notes.SkippedItems)
		if err != nil {
			lose(unmappedReason(err), 1)
			return
		}
		g.recaps, g.items = append(g.recaps, recaps...), append(g.items, items...)
	case teamscal.KindRecap:
		recap, items, notes, err := teamscal.MapRecapRecord(g.acct, key, raw)
		lose("recap item without a callId", notes.Skipped)
		lose("recap task or mention that is not an object", notes.SkippedItems)
		if err != nil {
			lose(unmappedReason(err), 1)
			return
		}
		g.recaps, g.items = append(g.recaps, recap), append(g.items, items...)
	}
}

// unmappedReason is why a record could not be mapped, as a short fixed phrase: the mapper's own
// reason names a field or a shape, never the record's content, except for the parser's message,
// which can quote a byte of it and is reduced to its first words.
func unmappedReason(err error) string {
	var u *teamscal.UnmappedError
	if !errors.As(err, &u) {
		return "record could not be mapped"
	}
	if i := strings.LastIndex(u.Reason, ": "); strings.HasPrefix(u.Reason, "calendar: refused event") && i >= 0 {
		return "event refused: " + u.Reason[i+2:] // the core's reason, without the event's id
	}
	if strings.HasPrefix(u.Reason, "not JSON") {
		return "not JSON"
	}
	if strings.HasPrefix(u.Reason, "value is ") {
		return "value is not an object"
	}
	return u.Reason
}

// apply writes one group through the core. The window is the span of every day the cache ever
// showed for the account, so it never shrinks when a later sync sees fewer days.
func (g *calGroup) apply(ctx context.Context, tx *sql.Tx, m calendarMode) (CalendarCounts, []*calendar.InvalidEventError, error) {
	var counts CalendarCounts
	account := teamscal.AccountID(g.acct)
	days := make([]string, 0, len(g.days)+len(g.retaken))
	for d := range g.days {
		days = append(days, d)
	}
	for d := range g.retaken {
		if _, live := g.days[d]; !live {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	var first, last sql.NullString
	var known int
	if err := tx.QueryRowContext(ctx, `select min(day), max(day), count(*) from calendar_covered_days where source=? and account_id=?`,
		string(calendar.SourceTeams), account).Scan(&first, &last, &known); err != nil {
		return counts, nil, err
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
	if m.read == nil {
		// A rebuild reads no cache: it verifies no day again and does not say the account synced now.
		var prev string
		if err := tx.QueryRowContext(ctx, `select synced_at from calendar_sources where source=? and account_id=?`, string(calendar.SourceTeams), account).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return counts, nil, err
		}
		if t := parseTime(sql.NullString{String: prev, Valid: prev != ""}); !t.IsZero() {
			w.SyncedAt = t
		}
		fresh := days[:0:0]
		for _, d := range days {
			var n int
			if err := tx.QueryRowContext(ctx, `select count(*) from calendar_covered_days where source=? and account_id=? and day=?`, string(calendar.SourceTeams), account, d).Scan(&n); err != nil {
				return counts, nil, err
			}
			if n == 0 {
				fresh = append(fresh, d)
			}
		}
		days = fresh
	}
	if lo != "" {
		start, _ := time.ParseInLocation(time.DateOnly, lo, m.zone)
		end, _ := time.ParseInLocation(time.DateOnly, hi, m.zone)
		w.Start, w.End = start, end.AddDate(0, 0, 1)
	}
	fresh, err := g.freshAt(ctx, tx, account, m.at)
	if err != nil {
		return counts, nil, err
	}
	w.CacheFreshAt = fresh
	// The core marks the ids it is given as gone before it captures the batch's events, and an event
	// of the batch is live whatever the list says. A removed record that is mapped here (a
	// backfill maps every row) must end up gone, so the gone ids are applied after the events.
	opts := calendar.ApplyOptions{SkipMatches: true, RecapTolerance: teamscal.RecapTimeTolerance}
	bc, err := calendar.ApplyBatch(ctx, tx, calendar.Batch{Window: w, Events: g.events, CoveredDays: days, Recaps: g.recaps, RecapItems: g.items}, opts, m.at)
	if err != nil {
		return counts, nil, err
	}
	counts = CalendarCounts{Events: toCounts(bc.Events), Recaps: toCounts(bc.Recaps), RecapItems: toCounts(bc.RecapItems), Gone: bc.Gone, Linked: bc.Linked, LinkedByStart: bc.LinkedByStart, UnlinkedNoEvent: bc.NoStartMatch, UnlinkedAmbiguous: bc.AmbiguousStart, Refused: len(bc.Refused)}
	if len(g.gone) > 0 {
		gc, err := calendar.ApplyBatch(ctx, tx, calendar.Batch{Window: w, GoneSourceIDs: g.gone}, opts, m.at)
		if err != nil {
			return counts, nil, err
		}
		counts.Gone += gc.Gone
		// The census after the gone ids are applied: an event that went takes its start links with it.
		counts.LinkedByStart, counts.UnlinkedNoEvent, counts.UnlinkedAmbiguous = gc.LinkedByStart, gc.NoStartMatch, gc.AmbiguousStart
	}
	return counts, bc.Refused, nil
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
	for _, t := range blankTables {
		sets, err := blankSets(ctx, tx, t.table, t.keep)
		if err != nil {
			return err
		}
		if t.table == "calendar_recap_items" {
			sets = append(sets, "superseded_at=coalesce(superseded_at, '"+stamp+"')")
		}
		// The rebuild is the Teams one: an Outlook row has no record to be rebuilt from. The recap
		// tables have no source column; an Outlook account is "outlook/<profile>", so they are
		// scoped by account. Outlook writes no recap today (TestOutlookWritesNoRecaps).
		where, args := ` where account_id not like 'outlook/%'`, []any(nil)
		if t.table == "calendar_source_events" {
			where, args = ` where source=?`, []any{string(calendar.SourceTeams)}
		}
		if _, err := tx.ExecContext(ctx, `update `+t.table+` set `+strings.Join(sets, ", ")+where, args...); err != nil { //nolint:gosec // G202: names come from pragma_table_info of a package-owned table
			return err
		}
	}
	return nil
}

// blankTables are the derived tables a blank empties and the columns each keeps.
var blankTables = []struct {
	table string
	keep  []string
}{
	{"calendar_source_events", []string{"composite_key", "source_id", "first_seen_at", "seen_at", "removed_at"}},
	{"calendar_recaps", []string{"first_seen_at", "updated_at"}},
	{"calendar_recap_items", []string{"kind", "origin", "first_seen_at", "updated_at", "superseded_at"}},
}

// blankSets is the "column=default" list that empties the content of a table's rows: every
// non-key column except keep goes back to its default, or NULL.
func blankSets(ctx context.Context, tx *sql.Tx, table string, keep []string) ([]string, error) {
	var sets []string
	err := eachRow(ctx, tx, `select name, dflt_value from pragma_table_info(?) where pk=0`, []any{table}, func(r *sql.Rows) error {
		var name string
		var dflt sql.NullString
		if err := r.Scan(&name, &dflt); err != nil {
			return err
		}
		if slices.Contains(keep, name) {
			return nil
		}
		if dflt.Valid {
			sets = append(sets, name+"="+dflt.String)
		} else {
			sets = append(sets, name+"=NULL")
		}
		return nil
	})
	return sets, err
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
