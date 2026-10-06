package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// Outlook source statuses of `calendar sources`. A failed read is named by what it says about the
// store, so a caller sees "Outlook is not being read, and why" in one word.
const (
	SourceOK                 = "ok"
	SourceSkippedInterval    = "skipped_interval"
	SourceUnsupportedVersion = "unsupported_version"
	SourceUnsupportedLayout  = "unsupported_layout"
	SourceUnreadable         = "unreadable"
)

// maxUnknownZones bounds unknown_time_zones; the names are zone labels, never event content.
const maxUnknownZones = 50

// CalendarSourcesFilter selects the rows. Account limits the rows to that Teams account and the
// accounts linked to it. Now and ReadInterval are the clock and the Outlook minimum read interval
// that next_read_after and the skipped_interval status are judged by.
type CalendarSourcesFilter struct {
	Account      *teamsdesktop.Account
	Now          time.Time
	ReadInterval time.Duration
}

// CalendarSource is one account of one source: how much of its calendar the archive holds and how
// fresh it is. Outlook is set on an Outlook row.
type CalendarSource struct {
	Source         string
	AccountID      string
	Principal      string
	Link           string // "config" when an Outlook account is linked to a Teams account, else "none"
	WindowStart    time.Time
	WindowEnd      time.Time
	SyncedAt       time.Time
	CacheFreshAt   time.Time
	CoveredDays    int
	LastVerifiedAt time.Time
	EventsLive     int
	EventsRemoved  int
	WithDetail     int
	WithAttendees  int
	WithBody       int
	Online         int
	RecapsTotal    int
	RecapsContent  int
	RecapsLinked   int
	RecapActions   int
	UnknownZones   []string
	Outlook        *OutlookSource
}

// OutlookSource is what only an Outlook row says: the read's status and interval, and the census of
// the last good read.
type OutlookSource struct {
	Status         string
	LastReadAt     time.Time
	LastAttemptAt  time.Time
	LastCheckedAt  time.Time // the latest sync that looked at the store at all
	CensusAsOf     time.Time // when the read the census below comes from happened
	NextReadAfter  time.Time // zero when the next sync may read
	IntervalSecond int
	UnknownLayouts []OutlookLayout
	BlocksRatio    *float64 // nil until a read has succeeded
	UnmappedValues map[string]int
	Failure        *OutlookFailure
}

// CalendarSources is the result of CalendarSources. NoTables says the archive predates the calendar.
type CalendarSources struct {
	NoTables bool
	Rows     []CalendarSource
}

type sourceKey struct{ source, account string }

// sourceSet holds the rows while the queries fill them.
type sourceSet struct {
	rows       map[sourceKey]*CalendarSource
	principals calendar.Principals
}

func (ss *sourceSet) row(source, account string) *CalendarSource {
	k := sourceKey{source, account}
	if ss.rows[k] == nil {
		ss.rows[k] = &CalendarSource{Source: source, AccountID: account, Principal: ss.principals.Of(account), Link: "none"}
	}
	return ss.rows[k]
}

// CalendarSources reports, per account and source, what the calendar tables hold. It reads counts
// and zone labels only, never an event's text.
func (s *Store) CalendarSources(ctx context.Context, f CalendarSourcesFilter) (out CalendarSources, err error) {
	ok, err := s.hasCalendarTables(ctx)
	if err != nil || !ok {
		out.NoTables = err == nil
		return out, err
	}
	principals, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return out, err
	}
	set := &sourceSet{rows: map[sourceKey]*CalendarSource{}, principals: principals}
	steps := []func() error{
		func() error { return s.sourceWindows(ctx, set) },
		func() error { return s.sourceEvents(ctx, set) },
		func() error { return s.sourceCoveredDays(ctx, set) },
		func() error { return s.sourceZones(ctx, set) },
		func() error { return s.sourceRecaps(ctx, set) },
		func() error { return s.sourceLinks(ctx, set) },
		func() error { return s.sourceOutlook(ctx, f, set) },
	}
	for _, step := range steps {
		if err = step(); err != nil {
			return out, err
		}
	}
	want := ""
	if f.Account != nil {
		want = accountString(f.Account)
	}
	for _, r := range set.rows {
		if want == "" || r.Principal == want {
			out.Rows = append(out.Rows, *r)
		}
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Source != b.Source {
			return a.Source > b.Source // teams first
		}
		return a.AccountID < b.AccountID
	})
	return out, nil
}

// each runs a query and hands every row to scan.
func (s *Store) each(ctx context.Context, q string, scan func(*sql.Rows) error, args ...any) error {
	rs, err := s.query(ctx, q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		if err = scan(rs); err != nil {
			return err
		}
	}
	return rs.Err()
}

func (s *Store) sourceWindows(ctx context.Context, set *sourceSet) error {
	return s.each(ctx, `select source, account_id, window_start, window_end, synced_at, cache_fresh_at from calendar_sources`, func(rs *sql.Rows) error {
		var src, acct string
		var ws, we, sy, fr sql.NullString
		if err := rs.Scan(&src, &acct, &ws, &we, &sy, &fr); err != nil {
			return err
		}
		r := set.row(src, acct)
		r.WindowStart, r.WindowEnd, r.SyncedAt, r.CacheFreshAt = lenientTime(ws), lenientTime(we), parseTime(sy), parseTime(fr)
		return nil
	})
}

// lenientTime reads a window bound, which the core stores as RFC 3339.
func lenientTime(ns sql.NullString) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, ns.String) // an unreadable bound is the zero time
	return t.UTC()
}

// sourceEvents counts the live rows by what they hold. A flag the source did not state is not
// counted as set.
func (s *Store) sourceEvents(ctx context.Context, set *sourceSet) error {
	return s.each(ctx, `select source, account_id,
	    coalesce(sum(removed_at is null), 0),
	    coalesce(sum(removed_at is not null), 0),
	    coalesce(sum(removed_at is null and detail_as_of is not null), 0),
	    coalesce(sum(removed_at is null and attendees_json not in ('', '[]', 'null')), 0),
	    coalesce(sum(removed_at is null and (body_text <> '' or body_html <> '')), 0),
	    coalesce(sum(removed_at is null and is_online_meeting = 1), 0)
	  from calendar_source_events group by source, account_id`, func(rs *sql.Rows) error {
		var src, acct string
		var live, gone, detail, att, body, online int
		if err := rs.Scan(&src, &acct, &live, &gone, &detail, &att, &body, &online); err != nil {
			return err
		}
		r := set.row(src, acct)
		r.EventsLive, r.EventsRemoved, r.WithDetail, r.WithAttendees, r.WithBody, r.Online = live, gone, detail, att, body, online
		return nil
	})
}

func (s *Store) sourceCoveredDays(ctx context.Context, set *sourceSet) error {
	return s.each(ctx, `select source, account_id, count(*), max(last_verified_at) from calendar_covered_days group by source, account_id`, func(rs *sql.Rows) error {
		var src, acct string
		var n int
		var last sql.NullString
		if err := rs.Scan(&src, &acct, &n, &last); err != nil {
			return err
		}
		r := set.row(src, acct)
		r.CoveredDays, r.LastVerifiedAt = n, parseTime(last)
		return nil
	})
}

// sourceZones lists the zone names no IANA id was found for. A name is a zone label (a Teams short
// name or an Outlook numeric code), not content.
func (s *Store) sourceZones(ctx context.Context, set *sourceSet) error {
	return s.each(ctx, `select source, account_id, time_zone from calendar_source_events
	  where time_zone <> '' and time_zone_iana = '' group by source, account_id, time_zone order by source, account_id, time_zone`, func(rs *sql.Rows) error {
		var src, acct, zone string
		if err := rs.Scan(&src, &acct, &zone); err != nil {
			return err
		}
		if r := set.row(src, acct); len(r.UnknownZones) < maxUnknownZones {
			r.UnknownZones = append(r.UnknownZones, zone)
		}
		return nil
	})
}

// sourceRecaps counts a Teams account's recaps. A placeholder (a call with no recap) creates no
// recap row, and a row with neither text nor a live item does not count as content.
func (s *Store) sourceRecaps(ctx context.Context, set *sourceSet) error {
	err := s.each(ctx, `select account_id, count(*),
	    coalesce(sum(headline <> '' or short_summary <> '' or outline <> '' or summary_sections_json <> '' or exists (
	      select 1 from calendar_recap_items i where i.account_id=r.account_id and i.call_id=r.call_id and i.superseded_at is null)), 0),
	    coalesce(sum(ical_uid <> ''), 0)
	  from calendar_recaps r group by account_id`, func(rs *sql.Rows) error {
		var acct string
		var total, content, linked int
		if err := rs.Scan(&acct, &total, &content, &linked); err != nil {
			return err
		}
		r := set.row(string(calendar.SourceTeams), acct)
		r.RecapsTotal, r.RecapsContent, r.RecapsLinked = total, content, linked
		return nil
	})
	if err != nil {
		return err
	}
	return s.each(ctx, `select r.account_id, count(*) from calendar_recap_items i
	  join calendar_recaps r on r.account_id=i.account_id and r.call_id=i.call_id
	  where i.kind='action_item' and `+preferredItems+` group by r.account_id`, func(rs *sql.Rows) error {
		var acct string
		var n int
		if err := rs.Scan(&acct, &n); err != nil {
			return err
		}
		set.row(string(calendar.SourceTeams), acct).RecapActions = n
		return nil
	})
}

func (s *Store) sourceLinks(ctx context.Context, set *sourceSet) error {
	return s.each(ctx, `select source, account_id, method from calendar_account_links where unlinked_at is null and source <> ?`, func(rs *sql.Rows) error {
		var src, acct, method string
		if err := rs.Scan(&src, &acct, &method); err != nil {
			return err
		}
		set.row(src, acct).Link = method
		return nil
	}, string(calendar.SourceTeams))
}

// sourceOutlook adds what only Outlook has. A profile whose first read failed has no calendar
// row yet; it still gets one, so the failure is not invisible.
func (s *Store) sourceOutlook(ctx context.Context, f CalendarSourcesFilter, set *sourceSet) error {
	const src = string(calendar.SourceOutlook)
	meta := map[string]map[string]string{} // account -> kind -> value
	err := s.each(ctx, `select key, value from meta where key like 'outlook\_%:%' escape '\'`, func(rs *sql.Rows) error {
		var key, value string
		if err := rs.Scan(&key, &value); err != nil {
			return err
		}
		kind, name, _ := strings.Cut(key, ":")
		account := name
		if slices.Contains([]string{outlookAttemptKey, outlookSkippedKey, outlookCheckedKey}, kind+":") {
			account = "outlook/" + name // these are kept by profile name
		}
		if meta[account] == nil {
			meta[account] = map[string]string{}
		}
		meta[account][kind] = value
		return nil
	})
	if err != nil {
		return err
	}
	// An Outlook row exists wherever the archive holds Outlook events or remembers a read.
	for account := range meta {
		set.row(src, account)
	}
	for k, r := range set.rows {
		if k.source == src {
			r.Outlook = outlookOf(r, meta[r.AccountID], f)
		}
	}
	return nil
}

// outlookOf builds the Outlook part of a row from the profile's remembered state: the last attempt,
// the failure that ended it, whether the last sync skipped the read, and the last good read's census.
func outlookOf(r *CalendarSource, m map[string]string, f CalendarSourcesFilter) *OutlookSource {
	o := &OutlookSource{LastReadAt: r.SyncedAt, IntervalSecond: int(f.ReadInterval / time.Second), Status: SourceOK}
	if t, err := time.Parse(timeLayout, m[strings.TrimSuffix(outlookAttemptKey, ":")]); err == nil {
		o.LastAttemptAt = t
		if next := t.Add(f.ReadInterval); f.Now.Before(next) {
			o.NextReadAfter = next
		}
	}
	if raw := m[strings.TrimSuffix(outlookFailureKey, ":")]; raw != "" {
		o.Failure = new(OutlookFailure)
		if json.Unmarshal([]byte(raw), o.Failure) != nil {
			o.Failure = &OutlookFailure{Code: "unreadable", Message: "the recorded failure cannot be read"}
		}
		o.Status = statusOfFailure(o.Failure.Code)
	} else if m[strings.TrimSuffix(outlookSkippedKey, ":")] != "" && !o.NextReadAfter.IsZero() {
		o.Status = SourceSkippedInterval
	}
	if t, err := time.Parse(timeLayout, m[strings.TrimSuffix(outlookCheckedKey, ":")]); err == nil {
		o.LastCheckedAt = t
	}
	// The census is the last good read's, also on a row that shows a failure.
	var read OutlookRead
	if json.Unmarshal([]byte(m[strings.TrimSuffix(outlookReadKey, ":")]), &read) == nil {
		o.UnknownLayouts, o.UnmappedValues, o.CensusAsOf = read.UnknownLayouts, read.UnmappedValues, read.At
		ratio := 0.0
		if read.BlocksFound > 0 {
			ratio = float64(read.BlocksInvalid) / float64(read.BlocksFound)
		}
		o.BlocksRatio = &ratio
	}
	return o
}

// statusOfFailure names a failed read by what it says about the store.
func statusOfFailure(code string) string {
	switch code {
	case "outlook_store_version":
		return SourceUnsupportedVersion
	case "outlook_layout_unsupported":
		return SourceUnsupportedLayout
	}
	return SourceUnreadable
}
