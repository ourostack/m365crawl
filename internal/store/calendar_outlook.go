package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const (
	outlookFailureKey = "outlook_failure:"
	outlookStampKey   = "outlook_derivation:"
	outlookAttemptKey = "outlook_last_attempt:"
	outlookReadKey    = "outlook_read:"
	outlookSkippedKey = "outlook_skipped:"
	outlookCheckedKey = "outlook_checked:"
)

// OutlookBatch is one Outlook profile's events, read whole from its store copy.
type OutlookBatch struct {
	Account string // "outlook/<profile directory name>"
	Events  []calendar.Event
	FreshAt time.Time // the store file's modification time: when Outlook last wrote it
	At      time.Time
	Zone    *time.Location // days are taken in this zone
	// Stamp is what this build derives under (see OutlookStamp); it is kept per account.
	Stamp string
	// Read is what the read saw beyond the events, kept for `calendar sources`.
	Read OutlookRead
}

// OutlookLayout is one (class, tag) pair the reader does not know, with how many objects of it the
// store holds. It is shown so a layout change reads as "tag 0x456 appeared for class 0x6b".
type OutlookLayout struct {
	Class uint16 `json:"class"`
	Tag   uint16 `json:"tag"`
	Count int    `json:"count"`
}

// OutlookRead is the last good read's census of one profile's store: numbers only.
type OutlookRead struct {
	// At is when the read happened; `calendar sources` shows it as census_as_of.
	At             time.Time       `json:"at,omitzero"`
	UnknownLayouts []OutlookLayout `json:"unknown_layouts,omitempty"`
	BlocksFound    int             `json:"blocks_found"`
	BlocksInvalid  int             `json:"blocks_invalid"`
	// UnmappedValues counts events whose event type, show-as or response value is outside the mapped set.
	UnmappedValues map[string]int `json:"unmapped_values,omitempty"`
}

// OutlookStamp is the stamp of what an Outlook derivation was made under: the Outlook mapper
// version, the time zone and the scrub rules, in the form of the Teams stamp. A stored stamp that
// differs makes the next batch re-derive the events it holds (see ApplyOutlook).
func OutlookStamp(mapper int, zone *time.Location) string {
	return calendarDerivation(mapper, zoneStamp(zone), teamsdesktop.RulesStamp())
}

// CommitOutlook writes one profile's batch through the core and records the source's run, in one
// transaction. The batch is applied in the calendar savepoint; a failure rolls everything back and
// is returned. run builds the run row from what the batch did. Gone detection is
// off (no gone ids, no inference of unseen rows), so an event Outlook stops holding stays; past
// days stay covered, cumulatively.
//
// When the stored stamp is not b.Stamp (a changed mapper, time zone or rule set, or none) each
// event of the batch is blanked first, so the copy that is captured is the one this mapper
// gives, not a merge with what an older mapper stored. Without it Capture keeps a stored flag
// over an unknown incoming one, and an all-day flag an older mapper wrote as false could never
// become unknown. A changed time zone also forgets the covered days, which were taken in the old
// zone, and takes them again. Events the batch does not hold are left as they are.
func (s *Store) CommitOutlook(ctx context.Context, b OutlookBatch, run func(CalendarResult) Run) (res CalendarResult, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var aerr error
		if res, aerr = isolated(ctx, tx, func() (CalendarResult, error) { return applyOutlook(ctx, tx, b) }); aerr == nil {
			aerr = res.failure
		}
		if aerr != nil {
			return aerr
		}
		if _, err := tx.ExecContext(ctx, `delete from meta where key=?`, outlookFailureKey+b.Account); err != nil {
			return err
		}
		b.Read.At = b.At
		raw, _ := json.Marshal(b.Read) // plain numbers and a time
		if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookReadKey+b.Account, string(raw)); err != nil {
			return err
		}
		return recordRun(ctx, tx, run(res))
	})
	return res, err
}

func applyOutlook(ctx context.Context, tx *sql.Tx, b OutlookBatch) (CalendarResult, error) {
	res := CalendarResult{Omissions: map[string]int{}}
	var stored string
	if err := tx.QueryRowContext(ctx, `select value from meta where key=?`, outlookStampKey+b.Account).Scan(&stored); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	zoneChanged := stored != "" && !strings.Contains(stored, ";"+calendarZone(zoneStamp(b.Zone))+";")
	if stored != b.Stamp {
		if zoneChanged {
			if _, err := tx.ExecContext(ctx, `delete from calendar_covered_days where source=? and account_id=?`, string(calendar.SourceOutlook), b.Account); err != nil {
				return res, err
			}
		}
		if err := dropSupersededKeys(ctx, tx, b.Account, b.Events); err != nil {
			return res, err
		}
		if err := blankEvents(ctx, tx, b.Account, b.Events); err != nil {
			return res, err
		}
	}
	days := coveredDays(b.Events, b.Zone)
	w := calendar.Window{Source: calendar.SourceOutlook, AccountID: b.Account, SyncedAt: b.At, CacheFreshAt: b.FreshAt}
	if len(days) > 0 {
		lo, _ := time.ParseInLocation(time.DateOnly, days[0], b.Zone)
		hi, _ := time.ParseInLocation(time.DateOnly, days[len(days)-1], b.Zone)
		w.Start, w.End = lo, hi.AddDate(0, 0, 1)
	}
	bc, err := calendar.ApplyBatch(ctx, tx, calendar.Batch{Window: w, Events: b.Events, CoveredDays: days}, calendar.ApplyOptions{SkipMatches: true}, b.At)
	if err != nil {
		return res, err
	}
	if zoneChanged {
		// The days of events the store no longer holds were forgotten with the rest: take them
		// again from the archived rows, in the new zone, so a covered day never uncovers.
		if err := retakeOutlookDays(ctx, tx, b); err != nil {
			return res, err
		}
	}
	res.Counts = CalendarCounts{Events: toCounts(bc.Events), Refused: len(bc.Refused)}
	res.omit(OmitCalendarRefused, len(bc.Refused))
	if _, err := tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookStampKey+b.Account, b.Stamp); err != nil {
		return res, err
	}
	return res, nil
}

// retakeOutlookDays records the days, in the batch's zone, of every live archived event of the
// account, including events the batch no longer holds.
func retakeOutlookDays(ctx context.Context, tx *sql.Tx, b OutlookBatch) error {
	var stored []calendar.Event
	if err := eachRow(ctx, tx, `select start_at, coalesce(all_day,0), start_date, event_type from calendar_source_events where source=? and account_id=?`,
		[]any{string(calendar.SourceOutlook), b.Account}, func(r *sql.Rows) error {
			var start string
			var allDay int
			var e calendar.Event
			if err := r.Scan(&start, &allDay, &e.StartDate, &e.EventType); err != nil {
				return err
			}
			e.Start, _ = time.Parse(timeLayout, start)
			e.AllDay = calendar.TriOf(allDay == 1)
			stored = append(stored, e)
			return nil
		}); err != nil {
		return err
	}
	return calendar.RecordCoveredDays(ctx, tx, calendar.SourceOutlook, b.Account, coveredDays(stored, b.Zone), b.At)
}

// coveredDays are the days, in zone, on which the batch holds at least one event that is not a
// series master; an all-day event is on its stated start date.
func coveredDays(events []calendar.Event, zone *time.Location) []string {
	set := map[string]bool{}
	for _, e := range events {
		switch {
		case e.EventType == calendar.EventMaster:
		case e.AllDay.Is(true) && e.StartDate != "":
			set[e.StartDate] = true
		default:
			set[e.Start.In(zone).Format(time.DateOnly)] = true
		}
	}
	days := make([]string, 0, len(set))
	for d := range set {
		days = append(days, d)
	}
	sort.Strings(days)
	return days
}

// dropSupersededKeys deletes the account's stored rows whose key an event of the batch now has in
// another case. A mapper that changes the case of an id (version 4 lower-cased them) gives every
// event a new key, and the old row would stay live beside it as a second copy of the same event.
// Only a row the batch re-derives under another key goes: an event Outlook no longer holds has no
// counterpart and stays, as the archive keeps what a source dropped.
func dropSupersededKeys(ctx context.Context, tx *sql.Tx, account string, events []calendar.Event) error {
	for _, e := range events {
		key := calendar.Key(e)
		for _, q := range []string{
			`delete from calendar_source_events where source=? and account_id=? and lower(event_key)=lower(?) and event_key<>?`,
			`delete from calendar_matches where source=? and account_id=? and lower(event_key)=lower(?) and event_key<>?`,
		} {
			if _, err := tx.ExecContext(ctx, q, string(calendar.SourceOutlook), account, key, key); err != nil {
				return err
			}
		}
	}
	return nil
}

// blankEvents empties the content of the stored rows of the batch's events (by source id),
// keeping their keys and first sighting, so the batch is captured over nothing. An event the
// core will refuse is not blanked: nothing is written for it, so blanking would erase its row.
func blankEvents(ctx context.Context, tx *sql.Tx, account string, events []calendar.Event) error {
	sets, err := blankSets(ctx, tx, blankTables[0].table, blankTables[0].keep)
	if err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `update calendar_source_events set `+strings.Join(sets, ", ")+` where source=? and account_id=? and source_id=?`) //nolint:gosec // G202: names come from pragma_table_info of a package-owned table
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()
	for _, e := range events {
		if calendar.ValidateEvent(e) != nil {
			continue // the core refuses it and writes nothing: its stored copy keeps its content
		}
		if _, err := stmt.ExecContext(ctx, string(calendar.SourceOutlook), account, e.SourceID); err != nil {
			return err
		}
	}
	return nil
}

// OutlookState is what the archive remembers of one Outlook profile before a read.
type OutlookState struct {
	LastAttempt time.Time      // when the store was last copied, whether it worked or not; zero for never
	Fingerprint string         // of the last successful read
	Omissions   map[string]int // of the last successful read, so a run that reads nothing still reports them
	HoldsEvents bool           // the archive holds events of the account
	// Failure is why the last read failed, until a read succeeds; nil when it did not.
	Failure *OutlookFailure
}

// OutlookFailure is a failed read's coded error, kept so a skipped read can report it.
type OutlookFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
	Exit    int    `json:"exit"`
}

// SetOutlookFailure remembers why the account's last read failed; nil forgets it.
func (s *Store) SetOutlookFailure(ctx context.Context, account string, f *OutlookFailure) error {
	if f == nil {
		_, err := s.db.ExecContext(ctx, `delete from meta where key=?`, outlookFailureKey+account)
		return err
	}
	b, _ := json.Marshal(f) // plain strings and an int
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookFailureKey+account, string(b))
	return err
}

// OutlookState reads it. The last attempt is recorded whether the copy worked or not, so a
// failing store is not retried in a loop.
func (s *Store) OutlookState(ctx context.Context, profile, source, account string) (OutlookState, error) {
	var st OutlookState
	var attempt, failure sql.NullString
	err := s.db.QueryRowContext(ctx, `select (select value from meta where key=?), (select value from meta where key=?), exists(select 1 from calendar_source_events where source=? and account_id=?)`,
		outlookAttemptKey+profile, outlookFailureKey+account, string(calendar.SourceOutlook), account).Scan(&attempt, &failure, &st.HoldsEvents)
	if err != nil {
		return st, err
	}
	st.LastAttempt, _ = time.Parse(timeLayout, attempt.String)
	if failure.Valid {
		st.Failure = new(OutlookFailure)
		if err = json.Unmarshal([]byte(failure.String), st.Failure); err != nil {
			return st, err
		}
	}
	if st.Fingerprint, err = s.LastFingerprint(ctx, source); err != nil {
		return st, err
	}
	var raw sql.NullString
	err = s.db.QueryRowContext(ctx, `select omissions_json from sync_runs where source=? and status in `+successStatuses+` order by id desc limit 1`, source).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if raw.Valid && raw.String != "" {
		err = json.Unmarshal([]byte(raw.String), &st.Omissions)
	}
	return st, err
}

// SetOutlookLastAttempt records the time of a copy attempt.
func (s *Store) SetOutlookLastAttempt(ctx context.Context, profile string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookAttemptKey+profile, at.UTC().Format(timeLayout))
	return err
}

// SetOutlookSkipped records that the last sync did not read the profile because its minimum read
// interval had not passed. SetOutlookChecked forgets it.
func (s *Store) SetOutlookSkipped(ctx context.Context, profile string) error {
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, '1') on conflict(key) do update set value=excluded.value`, outlookSkippedKey+profile)
	return err
}

// SetOutlookChecked records that a sync looked at the profile's store (a read, or the fingerprint
// check that found it unchanged) and forgets a skip.
func (s *Store) SetOutlookChecked(ctx context.Context, profile string, at time.Time) error {
	_, forget := s.db.ExecContext(ctx, `delete from meta where key=?`, outlookSkippedKey+profile)
	_, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, outlookCheckedKey+profile, at.UTC().Format(timeLayout))
	return errors.Join(forget, err)
}

// OutlookLinkMethod is how an operator's explicit link is recorded in calendar_account_links.
const OutlookLinkMethod = "config"

// SetOutlookLink links the Outlook account to the Teams account principal, or ends its link when
// principal is empty. It is idempotent: an account already linked to principal, or an unlink of an
// account with no link, changes nothing, so a link given on every run (an environment variable)
// does not rewrite the row. A rule of the core (an unknown Teams account, a principal that
// already has another Outlook account) comes back as its usage-class error and nothing changes.
func (s *Store) SetOutlookLink(ctx context.Context, account, principal string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		p, err := calendar.LoadPrincipals(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case principal == "" && p.Of(account) == account:
			return nil
		case principal != "" && p.Of(account) == principal:
			return nil
		case principal == "":
			return calendar.UnlinkAccount(ctx, tx, calendar.SourceOutlook, account, at)
		}
		return calendar.LinkAccount(ctx, tx, calendar.SourceOutlook, account, principal, OutlookLinkMethod, at)
	})
}

// OutlookLinkInEffect says whether an Outlook link already is what an operator asked for: with a
// principal, that account (any Outlook account when account is empty) is linked to it; with none,
// the account (or every Outlook account) has no link. A read command uses it to know whether the
// flag still needs a sync to take effect.
func (s *Store) OutlookLinkInEffect(ctx context.Context, account, principal string) (bool, error) {
	p, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return false, err
	}
	switch {
	case principal != "" && account != "":
		return p.Of(account) == principal, nil
	case principal != "":
		return len(p.Accounts(principal)) > 1, nil
	case account != "":
		return p.Of(account) == account, nil
	}
	var active int
	err = s.db.QueryRowContext(ctx, `select count(*) from calendar_account_links where unlinked_at is null and source=?`, string(calendar.SourceOutlook)).Scan(&active)
	return active == 0, err
}

// OutlookLinkedAccounts lists the Outlook accounts that have an active link, sorted.
func (s *Store) OutlookLinkedAccounts(ctx context.Context) ([]string, error) {
	var joined string
	err := s.db.QueryRowContext(ctx, `select coalesce(group_concat(account_id, char(10)), '') from (select account_id from calendar_account_links where unlinked_at is null and source=? order by account_id)`,
		string(calendar.SourceOutlook)).Scan(&joined)
	if joined == "" {
		return nil, err
	}
	return strings.Split(joined, "\n"), err
}

// OutlookReadTimes is, for each Outlook account the archive has read, when its last good read
// happened. An account whose census cannot be read is left out.
func (s *Store) OutlookReadTimes(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `select key, value from meta where key like ?`, outlookReadKey+"%")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]time.Time{}
	for rows.Next() {
		var key, raw string
		var read OutlookRead
		if rows.Scan(&key, &raw) == nil && json.Unmarshal([]byte(raw), &read) == nil && !read.At.IsZero() {
			out[strings.TrimPrefix(key, outlookReadKey)] = read.At
		}
	}
	return out, rows.Err()
}
