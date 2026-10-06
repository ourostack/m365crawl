package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// attachedRecap names a recap an event holds. SeriesLevel is true when the recap carries the id
// that several occurrences of a series share and none of them starts near its meeting start (or
// it has no meeting start), so it cannot be placed on one occurrence and every occurrence holds it.
type attachedRecap struct {
	account, call string
	SeriesLevel   bool
}

// occurrenceStart is the start of one live occurrence that carries an iCalUID.
type occurrenceStart struct {
	key   string
	start time.Time
}

// seriesOccurrences lists the live, non-master occurrences of the accounts that carry ical and are
// neither cancelled nor declined, one entry per event key, in key order: the same candidates a
// start-time link considers.
func (s *Store) seriesOccurrences(ctx context.Context, accounts []string, ical string) ([]occurrenceStart, error) {
	args := append(stringArgs(accounts), ical, calendar.EventMaster)
	rows, err := s.query(ctx, `select event_key, start_at from calendar_source_events
	  where account_id in `+inList(len(accounts))+` and ical_uid=? and removed_at is null and event_type<>? and coalesce(cancelled,0)<>1
	  and response<>'declined' and start_at<>'' order by event_key, source, account_id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []occurrenceStart
	for rows.Next() {
		var o occurrenceStart
		var start string
		if err := rows.Scan(&o.key, &start); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].key == o.key {
			continue
		}
		o.start = parseTime(sql.NullString{String: start, Valid: true})
		out = append(out, o)
	}
	return out, rows.Err()
}

// ownerOccurrence is the occurrence a recap with meeting start at belongs to: the one that starts
// nearest to it within calendar.RecapStartTolerance, the lower key on a tie. ok is false when none
// does, or when the recap has no meeting start.
func ownerOccurrence(occs []occurrenceStart, at time.Time) (key string, ok bool) {
	if at.IsZero() {
		return "", false
	}
	best := time.Duration(-1)
	for _, o := range occs {
		d := o.start.Sub(at)
		if d < 0 {
			d = -d
		}
		if d <= calendar.RecapStartTolerance && (best < 0 || d < best) {
			key, best, ok = o.key, d, true
		}
	}
	return key, ok
}

// attachedRecaps lists the recaps linked to the event's iCalUID that the event holds. When one
// iCalUID is shared by several occurrences (a series id), a recap belongs to the occurrence its
// meeting start matches and to no other; one that matches none stays on every occurrence as a
// series-level link. A uid held by one occurrence attaches all of its recaps, as before.
func (s *Store) attachedRecaps(ctx context.Context, accounts []string, it calendar.AgendaItem) ([]attachedRecap, error) {
	if it.ICalUID == "" {
		return nil, nil
	}
	args := append(stringArgs(accounts), it.ICalUID)
	rows, err := s.query(ctx, `select account_id, call_id, meeting_start_at from calendar_recaps
	  where account_id in `+inList(len(accounts))+` and ical_uid=?
	  order by coalesce(meeting_start_at, recording_start_at, ''), call_id`, args...)
	if err != nil {
		return nil, err
	}
	type pending struct {
		attachedRecap
		start time.Time
	}
	var all []pending
	for rows.Next() {
		var p pending
		var ms sql.NullString
		if err := rows.Scan(&p.account, &p.call, &ms); err != nil {
			_ = rows.Close()
			return nil, err
		}
		p.start = parseTime(ms)
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(all) == 0 {
		return nil, nil
	}
	occs, err := s.seriesOccurrences(ctx, accounts, it.ICalUID)
	if err != nil {
		return nil, err
	}
	mine := map[string]bool{it.Key: true}
	for _, k := range it.JoinedKeys {
		mine[k] = true
	}
	var out []attachedRecap
	for _, p := range all {
		if len(occs) > 1 {
			owner, ok := ownerOccurrence(occs, p.start)
			switch {
			case !ok:
				p.SeriesLevel = true
			case !mine[owner]:
				continue
			}
		}
		out = append(out, p.attachedRecap)
	}
	return out, nil
}

// recapCounts counts the recaps an event holds and their action items.
func (s *Store) recapCounts(ctx context.Context, accounts []string, it calendar.AgendaItem) (recaps, actions int, err error) {
	held, err := s.attachedRecaps(ctx, accounts, it)
	if err != nil {
		return 0, 0, err
	}
	for _, h := range held {
		var n int
		if err = s.db.QueryRowContext(ctx, `select count(*) from calendar_recap_items i where i.account_id=? and i.call_id=? and i.kind='action_item' and `+preferredItems, h.account, h.call).Scan(&n); err != nil {
			return 0, 0, err
		}
		actions += n
	}
	return len(held), actions, nil
}

// recaps loads the recaps an event holds with their preferred items, oldest meeting first.
func (s *Store) recaps(ctx context.Context, accounts []string, it calendar.AgendaItem) ([]CalendarRecap, error) {
	held, err := s.attachedRecaps(ctx, accounts, it)
	if err != nil || len(held) == 0 {
		return nil, err
	}
	all, accts, err := s.recapRows(ctx, `account_id in `+inList(len(accounts))+` and ical_uid=?`, append(stringArgs(accounts), it.ICalUID))
	if err != nil {
		return nil, err
	}
	var out []CalendarRecap
	for i, r := range all {
		for _, h := range held {
			if h.account == accts[i] && h.call == r.CallID {
				r.SeriesLevel = h.SeriesLevel
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

// CalendarActionFilter selects the action items of the recaps of events that start in [From, To).
type CalendarActionFilter struct {
	Account  *teamsdesktop.Account // nil: every account
	From, To time.Time
	// Owner keeps the items whose owner contains it, ignoring case.
	Owner string
	// Mine keeps the items whose owner is the account's own display name, ignoring case.
	Mine  bool
	Limit int // 0 means DefaultLimit
}

// CalendarAction is one action item of a recap, with the event it was held on.
type CalendarAction struct {
	EventID, EventKey, Subject string
	EventStart                 time.Time
	CallID                     string
	CalendarRecapItem
	// Mine says whether the owner is the account's own user: true or false when known, nil when
	// the owner is a first name that another person of the meeting shares (MineBasis is then
	// MineAmbiguous). MineBasis says how sure a true answer is: MineFullName, or MineFirstName for
	// a one-word owner equal to the user's first name that nobody else in the meeting has. It is
	// empty when Mine is false.
	Mine      *bool
	MineBasis string
	// ExpiresAt is when Teams stops serving the recap; the items stay in the archive.
	ExpiresAt time.Time
	// SeriesLevel says the recap is linked to the series, not this occurrence, and appears once,
	// under the first occurrence of the range that holds it.
	SeriesLevel bool
}

// How CalendarAction.Mine was decided.
const (
	MineFullName  = "full_name"
	MineFirstName = "first_name"
	MineAmbiguous = "ambiguous"
)

// CalendarActions is the result of CalendarActions.
type CalendarActions struct {
	Items []CalendarAction
	// MineAmbiguous counts the items --mine left out because their owner is a first name another
	// person of the meeting shares.
	MineAmbiguous int
	Total         int
	Truncated     bool
	// The coverage fields are CalendarAgenda's: they say how far an absent item is evidence.
	Gap           bool
	AsOf          time.Time
	UncoveredDays []string
	Accounts      []calendar.AccountCoverage
	Unlinked      []string
	// NoTables is true for an archive from before the calendar tables.
	NoTables bool
}

// noOwnName is the usage error of Mine when no account in scope has its own name in the archive yet.
func noOwnName() *errs.Coded {
	c := errs.Usage("--mine needs your own name, which the archive does not hold yet for this account")
	c.Fix = "Run `teamscrawl sync` so your own user is archived (`teamscrawl whoami` shows display_name), or filter with --owner <name>."
	return c
}

// CalendarActions lists the action items of the recaps held by the events that start in the
// range, by event start then recap then item order. A recap shared by several occurrences with no
// way to tell which (a series-level link) is listed once, under the first occurrence. Names are
// matched against the principal's own display name for Mine.
func (s *Store) CalendarActions(ctx context.Context, f CalendarActionFilter) (CalendarActions, error) {
	var out CalendarActions
	ok, err := s.hasCalendarTables(ctx)
	if err != nil {
		return out, err
	}
	if !ok {
		out.NoTables = true
		return out, nil
	}
	names, err := s.ownNames(ctx, f.Account)
	if err != nil {
		return out, err
	}
	if f.Mine && len(names) == 0 {
		return out, noOwnName()
	}
	res, err := calendar.Agenda(ctx, s.db, calendar.AgendaQuery{AccountID: accountString(f.Account), From: f.From, To: f.To})
	if err != nil {
		return out, err
	}
	out.Gap, out.AsOf, out.Unlinked, out.UncoveredDays, out.Accounts = res.Gap, res.AsOf, res.Unlinked, res.UncoveredDays, res.Accounts
	principals, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return out, err
	}
	items := res.Items
	sort.SliceStable(items, func(i, j int) bool { return items[i].Start.Before(items[j].Start) })
	listed := map[string]bool{}
	var all []CalendarAction
	for _, it := range items {
		accounts := principals.Accounts(it.Principal)
		held, err := s.attachedRecaps(ctx, accounts, it)
		if err != nil {
			return out, err
		}
		if len(held) == 0 {
			continue
		}
		recaps, accts, err := s.recapRows(ctx, `account_id in `+inList(len(accounts))+` and ical_uid=?`, append(stringArgs(accounts), it.ICalUID))
		if err != nil {
			return out, err
		}
		for i, r := range recaps {
			var h *attachedRecap
			for j := range held {
				if held[j].account == accts[i] && held[j].call == r.CallID {
					h = &held[j]
				}
			}
			id := it.Principal + "\x00" + accts[i] + "\x00" + r.CallID
			if h == nil || listed[id] {
				continue
			}
			listed[id] = true
			for _, a := range r.ActionItems {
				if f.Owner != "" && !strings.Contains(strings.ToLower(a.Owner), strings.ToLower(f.Owner)) {
					continue
				}
				mine, basis := ownerIsMe(names[it.Principal], a.Owner, func() []string { return meetingNames(it.AttendeesJSON, r) })
				if f.Mine && (mine == nil || !*mine) {
					if basis == MineAmbiguous {
						out.MineAmbiguous++
					}
					continue
				}
				all = append(all, CalendarAction{
					EventID: EventID(it.Principal, it.Key), EventKey: it.Key, Subject: it.Subject, EventStart: it.Start, CallID: r.CallID,
					CalendarRecapItem: a, Mine: mine, MineBasis: basis, ExpiresAt: r.ExpiresAt, SeriesLevel: h.SeriesLevel,
				})
			}
		}
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	out.Total = len(all)
	if len(all) > limit {
		all, out.Truncated = all[:limit], true
	}
	out.Items = all
	return out, nil
}

// ownNames maps each account in scope to the display name of its own user, leaving out accounts
// whose own user has not been seen as a person yet.
func (s *Store) ownNames(ctx context.Context, account *teamsdesktop.Account) (map[string]string, error) {
	rows, err := s.Whoami(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		if r.DisplayName == "" || account != nil && (r.TenantID != account.TenantID || r.UserID != account.UserID) {
			continue
		}
		out[r.TenantID+"/"+r.UserID] = r.DisplayName
	}
	return out, nil
}

// linkCalendar fills CalendarSeriesKey and CalendarEventCount of the Meeting conversations whose id
// is the meeting chat of at least one live, non-master event of the same principal. The count is
// of distinct event keys, so an event two sources hold counts once; the key is the series the
// events share, and stays empty when they belong to several. A chat with no event, and an archive
// without the calendar tables, are left as they are.
func (s *Store) linkCalendar(ctx context.Context, rows []ConversationRow) error {
	var meetings []int
	for i, r := range rows {
		if strings.EqualFold(r.Kind, "Meeting") {
			meetings = append(meetings, i)
		}
	}
	if len(meetings) == 0 {
		return nil
	}
	ok, err := s.hasCalendarTables(ctx)
	if err != nil || !ok {
		return err
	}
	principals, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return err
	}
	for _, i := range meetings {
		r := &rows[i]
		accounts := principals.Accounts(principals.Of(r.TenantID + "/" + r.UserID))
		args := append([]any{calendar.EventSingle, calendar.EventSingle}, stringArgs(accounts)...)
		args = append(args, r.ID, calendar.EventMaster)
		var n, series int
		var key string
		// A single event is its own "series" and does not vote for the chat's series key.
		if err := s.db.QueryRowContext(ctx, `select count(distinct event_key), count(distinct case when event_type<>? then series_key end),
		  coalesce(min(case when event_type<>? then series_key end),'') from calendar_source_events
		  where account_id in `+inList(len(accounts))+` and teams_thread_id=? and removed_at is null and event_type<>?`, args...).Scan(&n, &series, &key); err != nil {
			return err
		}
		r.CalendarEventCount = n
		if series == 1 {
			r.CalendarSeriesKey = key
		}
	}
	return nil
}

// ownerIsMe decides whether an action item's owner is the account's own user, whose display name
// is own. Recap owners are mostly a first name only, so a one-word owner equal to the first word
// of own counts, unless another person of the meeting (people returns their names) has that first
// word: then the answer is unknown (nil, MineAmbiguous) and is never guessed. An owner that is
// empty, or that matches neither way, is not me; an account with no known name is false too.
func ownerIsMe(own, owner string, people func() []string) (mine *bool, basis string) {
	yes, no := true, false
	owner = strings.TrimSpace(owner)
	switch {
	case own == "" || owner == "":
		return &no, ""
	case strings.EqualFold(owner, own):
		return &yes, MineFullName
	}
	first := strings.Fields(own)[0]
	if len(strings.Fields(owner)) != 1 || !strings.EqualFold(owner, first) {
		return &no, ""
	}
	for _, n := range people() {
		if f := strings.Fields(n); len(f) > 0 && strings.EqualFold(f[0], first) && !strings.EqualFold(strings.Join(f, " "), own) {
			return nil, MineAmbiguous
		}
	}
	return &yes, MineFirstName
}

// meetingNames lists the people of a meeting: the event's attendees when it has an attendee list,
// otherwise the recap's speakers (and the speakers of its items).
func meetingNames(attendeesJSON string, r CalendarRecap) []string {
	var out []string
	for _, a := range parseAttendees(attendeesJSON) {
		out = append(out, a.Name)
	}
	if len(out) > 0 {
		return out
	}
	var speakers []map[string]any
	if json.Unmarshal(r.Speakers, &speakers) == nil {
		for _, sp := range speakers {
			for _, k := range []string{"name", "displayName", "speakerName"} {
				if n, ok := sp[k].(string); ok && n != "" {
					out = append(out, n)
					break
				}
			}
		}
	}
	for _, a := range r.ActionItems {
		out = append(out, a.Speaker)
	}
	for _, a := range r.Mentions {
		out = append(out, a.Speaker)
	}
	return out
}
