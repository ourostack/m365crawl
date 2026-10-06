package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/teamscrawl/internal/calendar"
	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// EventID is the short id of an event: "ev_" and the first 10 hex characters of the sha256 of
// "<principal>|<event key>". The principal is the Teams account an event is grouped under (the
// account itself when nothing links it), so linking another source to a principal never changes
// the id a Teams-only event already had.
func EventID(principal, key string) string { return eventIDOf(principal + "|" + key) }

func eventIDOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "ev_" + hex.EncodeToString(sum[:])[:10]
}

// ErrNoCalendarTables is returned by CalendarEvent for an archive from before the calendar tables.
var ErrNoCalendarTables = errors.New("the archive has no calendar tables yet")

// CalendarFilter selects the agenda: the events overlapping [From, To).
type CalendarFilter struct {
	Account                                                           *teamsdesktop.Account // nil: every account
	From, To                                                          time.Time
	Query                                                             string
	IncludeCancelled, IncludeDeclined, IncludeMasters, IncludeRemoved bool
	Limit                                                             int // 0 means DefaultLimit
}

// CalendarRow is one merged event as the agenda shows it.
type CalendarRow struct {
	calendar.AgendaItem
	// EventID is the short id of the event; see EventID.
	EventID string
	Rooms   []calendar.Room
	// DetailLevel is basic, stale or full; see calendar.DetailLevel.
	DetailLevel string
	HasRecap    bool
	// ActionItems counts the recap action items of the event; Recordings the recordings,
	// transcripts and call events of its chat that match this occurrence.
	ActionItems int
	Recordings  int
}

// CalendarResult is an agenda and what the archive can say about its completeness.
type CalendarAgenda struct {
	Rows      []CalendarRow
	Total     int
	Truncated bool
	// Gap and AsOf are calendar.AgendaResult's: Gap says some day of the range is not covered, so an
	// absent event is not evidence.
	Gap  bool
	AsOf time.Time
	// Unlinked lists the accounts of other sources that no link joins to a Teams account.
	Unlinked []string
	// UncoveredDays and Accounts are calendar.AgendaResult's: the days some account does not cover,
	// and each account's own freshness.
	UncoveredDays []string
	Accounts      []calendar.AccountCoverage
	// UnlinkedRecaps are the recaps whose meeting started in the range and that no event holds: a
	// recap with no iCalUID, or one whose event the archive does not have (an impromptu meeting,
	// an evicted event). They are listed so their content is not hidden behind an event that is
	// not there. UnlinkedRecapsTotal counts them before the limit.
	UnlinkedRecaps      []CalendarUnlinkedRecap
	UnlinkedRecapsTotal int
	// NoTables is true for an archive from before the calendar tables: the result is empty and
	// says nothing about the calendar.
	NoTables bool
}

// hasCalendarTables reports whether the archive has the calendar tables.
func (s *Store) hasCalendarTables(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in ('calendar_source_events','calendar_sources','calendar_covered_days','calendar_account_links','calendar_recaps','calendar_recap_items')`).Scan(&n)
	return n == 6, err
}

func accountString(a *teamsdesktop.Account) string {
	if a == nil {
		return ""
	}
	return a.TenantID + "/" + a.UserID
}

// CalendarAgenda lists the events overlapping the range, merged across sources by principal.
func (s *Store) CalendarAgenda(ctx context.Context, f CalendarFilter) (CalendarAgenda, error) {
	var out CalendarAgenda
	ok, err := s.hasCalendarTables(ctx)
	if err != nil {
		return out, err
	}
	if !ok {
		out.NoTables = true
		return out, nil
	}
	res, err := calendar.Agenda(ctx, s.db, calendar.AgendaQuery{
		AccountID: accountString(f.Account), From: f.From, To: f.To, Query: f.Query,
		IncludeCancelled: f.IncludeCancelled, IncludeDeclined: f.IncludeDeclined,
		IncludeMasters: f.IncludeMasters, IncludeRemoved: f.IncludeRemoved,
	})
	if err != nil {
		return out, err
	}
	out.Gap, out.AsOf, out.Unlinked, out.Total = res.Gap, res.AsOf, res.Unlinked, len(res.Items)
	out.UncoveredDays, out.Accounts = res.UncoveredDays, res.Accounts
	items := res.Items
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if len(items) > limit {
		items, out.Truncated = items[:limit], true
	}
	principals, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return out, err
	}
	rec := &recordingCache{s: s, principals: principals, byChat: map[string]*chatRecordings{}}
	for _, it := range items {
		row, err := s.rowOf(ctx, it, principals, rec)
		if err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, row)
	}
	if err := s.unlinkedRecaps(ctx, f, principals, limit, &out); err != nil {
		return out, err
	}
	return out, nil
}

// unlinkedRecaps fills out with the recaps that started in the range and have no event.
func (s *Store) unlinkedRecaps(ctx context.Context, f CalendarFilter, p calendar.Principals, limit int, out *CalendarAgenda) error {
	start := `coalesce(meeting_start_at, recording_start_at, '')`
	cond := start + ` >= ? and ` + start + ` < ? and (ical_uid='' or not exists (
	  select 1 from calendar_source_events e where e.ical_uid=calendar_recaps.ical_uid and e.account_id=calendar_recaps.account_id))`
	args := []any{f.From.UTC().Format(timeLayout), f.To.UTC().Format(timeLayout)}
	if f.Account != nil {
		scope := p.Resolve(accountString(f.Account))
		cond, args = cond+` and account_id in `+inList(len(scope)), append(args, stringArgs(scope)...)
	}
	recaps, accts, err := s.recapRows(ctx, cond, args)
	if err != nil {
		return err
	}
	out.UnlinkedRecapsTotal = len(recaps)
	for i, r := range recaps {
		if i == limit {
			break
		}
		out.UnlinkedRecaps = append(out.UnlinkedRecaps, CalendarUnlinkedRecap{Principal: p.Of(accts[i]), CalendarRecap: r})
	}
	return nil
}

// rowOf adds what is derived at read time to a merged event.
func (s *Store) rowOf(ctx context.Context, it calendar.AgendaItem, p calendar.Principals, rec *recordingCache) (CalendarRow, error) {
	row := CalendarRow{AgendaItem: it, EventID: EventID(it.Principal, it.Key), Rooms: calendar.MergedRooms(it.Merged), DetailLevel: calendar.DetailLevel(it.Event)}
	accounts := p.Accounts(it.Principal)
	if it.ICalUID != "" {
		recaps, items, err := s.recapCounts(ctx, accounts, it)
		if err != nil {
			return row, err
		}
		row.HasRecap, row.ActionItems = recaps > 0, items
	}
	if it.TeamsThreadID != "" {
		cr, err := rec.of(ctx, it.Principal, it.TeamsThreadID)
		if err != nil {
			return row, err
		}
		row.Recordings = len(cr.matched[it.Key])
		for _, k := range it.JoinedKeys {
			row.Recordings += len(cr.matched[k])
		}
	}
	return row, nil
}

// query runs q, which is built from constants and inList placeholders only: every value travels
// as an argument.
func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, q, args...) //nolint:gosec // G202: q holds only constants and placeholders
}

func inList(n int) string { return "(" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")" }

func stringArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// preferredItems is the condition that keeps, for each call and kind, the live items of the recap
// store when it has any and the catch-up store's otherwise. Both stay stored; readers choose.
const preferredItems = `i.superseded_at is null and (i.origin='recap' or not exists (
  select 1 from calendar_recap_items j where j.account_id=i.account_id and j.call_id=i.call_id and j.kind=i.kind and j.superseded_at is null and j.origin='recap'))`

// CalendarUnlinkedRecap is a recap that belongs to no event of the archive.
type CalendarUnlinkedRecap struct {
	// Principal is the account the recap is grouped under (a Teams account).
	Principal string
	CalendarRecap
}

// CalendarAttendee is one attendee as stored.
type CalendarAttendee struct {
	Name, Address, Type, Role, Response string
}

// CalendarRecapItem is an action item or mention of a call.
type CalendarRecapItem struct {
	Title, Text, Owner, Speaker, MentionedBy string
	At                                       time.Time
	Origin                                   string
	Highlights                               json.RawMessage
}

// CalendarRecap is what is known about one call of a meeting.
type CalendarRecap struct {
	CallID, RecapID, LinkMethod, Headline, ShortSummary, Outline string
	SummarySections, Speakers, Topics                            json.RawMessage
	RecordingURL                                                 string
	RecordingStart, RecordingEnd                                 time.Time
	DurationSeconds                                              int
	IsMissed                                                     bool
	MeetingStart, MeetingEnd, ExpiresAt                          time.Time
	AttendanceStatus                                             string
	AttendeesCount                                               int
	HasConfRoomConnected, HasCatchUp, HasRecap                   bool
	// SeriesLevel is true on a recap read for an event when the recap is linked to the series id
	// several occurrences share and no occurrence's start matches its meeting start: it appears on
	// every occurrence.
	SeriesLevel           bool
	ActionItems, Mentions []CalendarRecapItem
}

// CalendarRecording is a recording, transcript or call event of the event's meeting chat.
type CalendarRecording struct {
	MessageID, ConversationID string
	SentAt                    time.Time
	Kind, Text, Link          string
	// MatchedBy is recap or window for a recording matched to an occurrence; empty for a series-level one.
	MatchedBy string
}

// CalendarChat is the meeting chat shared by every occurrence of the event's series.
type CalendarChat struct {
	ConversationID, DisplayName string
	MessageCount                int
}

// CalendarSeries places an occurrence in its series.
type CalendarSeries struct {
	Key          string
	Rule         json.RawMessage
	MasterKey    string
	MasterID     string
	Occurrences  int // distinct live non-master events of the series in the archive
	HasMasterRow bool
}

// CalendarDetail is one event with everything the archive holds about it.
type CalendarDetail struct {
	CalendarRow
	Attendees             []CalendarAttendee
	Recaps                []CalendarRecap
	Chat                  *CalendarChat
	Series                CalendarSeries
	Recordings            []CalendarRecording
	SeriesRecordings      []CalendarRecording
	SeriesRecordingsTotal int
}

// seriesRecordingsShown is how many series-level recordings an event lists.
const seriesRecordingsShown = 20

// refCandidate is one (principal, key) a reference names.
type refCandidate struct{ principal, key string }

// CalendarEvent resolves ref (an event key, an event id, a legacy id, or an unambiguous prefix of
// any of them) to one event and loads everything about it. An unknown or ambiguous reference is a
// usage error.
func (s *Store) CalendarEvent(ctx context.Context, acct *teamsdesktop.Account, ref string) (CalendarDetail, error) {
	var d CalendarDetail
	ok, err := s.hasCalendarTables(ctx)
	if err != nil {
		return d, err
	}
	if !ok {
		return d, ErrNoCalendarTables
	}
	principals, err := calendar.LoadPrincipals(ctx, s.db)
	if err != nil {
		return d, err
	}
	cands, err := s.matchRef(ctx, principals, accountString(acct), ref)
	if err != nil {
		return d, err
	}
	// Find loads each candidate's group; two candidates that are one joined group are one event.
	seen := map[refCandidate]bool{}
	var items []calendar.AgendaItem
	for _, c := range cands {
		it, ok, err := calendar.Find(ctx, s.db, c.principal, c.key)
		if err != nil {
			return d, err
		}
		g := refCandidate{it.Principal, it.Key}
		if ok && !seen[g] {
			seen[g] = true
			items = append(items, it)
		}
	}
	switch len(items) {
	case 0:
		return d, noEvent(ref)
	case 1:
	default:
		return d, ambiguousEvent(ref, items)
	}
	it := items[0]
	rec := &recordingCache{s: s, principals: principals, byChat: map[string]*chatRecordings{}}
	if d.CalendarRow, err = s.rowOf(ctx, it, principals, rec); err != nil {
		return d, err
	}
	d.Attendees = parseAttendees(it.AttendeesJSON)
	accounts := principals.Accounts(it.Principal)
	if it.ICalUID != "" {
		if d.Recaps, err = s.recaps(ctx, accounts, it); err != nil {
			return d, err
		}
	}
	if d.Series, err = s.series(ctx, accounts, it); err != nil {
		return d, err
	}
	if it.TeamsThreadID != "" {
		cr := rec.cached(it.Principal, it.TeamsThreadID) // rowOf loaded it
		keys := append([]string{it.Key}, it.JoinedKeys...)
		for _, k := range keys {
			d.Recordings = append(d.Recordings, cr.matched[k]...)
		}
		sort.SliceStable(d.Recordings, func(i, j int) bool { return d.Recordings[i].SentAt.Before(d.Recordings[j].SentAt) })
		d.SeriesRecordingsTotal = len(cr.series)
		d.SeriesRecordings = cr.series
		if len(d.SeriesRecordings) > seriesRecordingsShown {
			d.SeriesRecordings = d.SeriesRecordings[:seriesRecordingsShown]
		}
		d.Chat = cr.chat
	}
	return d, nil
}

func noEvent(ref string) *errs.Coded {
	c := errs.Usage(fmt.Sprintf("no calendar event matches %q. Events on days Teams never cached are not in the archive, so this does not show that the event does not exist", ref))
	c.Fix = "List events with `teamscrawl calendar --from <date> --days 7` and pass an event_id (or event_key) from the result."
	return c
}

func ambiguousEvent(ref string, items []calendar.AgendaItem) *errs.Coded {
	sort.Slice(items, func(i, j int) bool { return items[i].Start.Before(items[j].Start) })
	var lines []string
	for i, it := range items {
		if i == 10 {
			lines = append(lines, fmt.Sprintf("... and %d more", len(items)-10))
			break
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", EventID(it.Principal, it.Key), it.Start.UTC().Format("2006-01-02T15:04Z"), it.Subject))
	}
	c := errs.Usage(fmt.Sprintf("%q matches %d calendar events: %s", ref, len(items), strings.Join(lines, "; ")))
	c.Fix = "Pass the full event_id of one of them."
	return c
}

// matchRef finds the (principal, key) pairs ref names. A reference that is exactly a key, an event
// id or a legacy id (the id computed from an account of the principal and the key, as an earlier
// state printed it) wins over prefixes.
func (s *Store) matchRef(ctx context.Context, p calendar.Principals, account, ref string) ([]refCandidate, error) {
	where, args := "1", []any(nil)
	if account != "" {
		scope := p.Resolve(account)
		where, args = "account_id in "+inList(len(scope)), stringArgs(scope)
	}
	history, err := s.linkHistory(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.query(ctx, `select distinct account_id, event_key from calendar_source_events where `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var exact, past, prefix []refCandidate
	seen := map[refCandidate]bool{}
	for rows.Next() {
		var acct, key string
		if err := rows.Scan(&acct, &key); err != nil {
			return nil, err
		}
		c := refCandidate{p.Of(acct), key}
		if seen[c] {
			continue
		}
		seen[c] = true
		ids := []string{EventID(c.principal, key)}
		for _, a := range p.Accounts(c.principal) {
			ids = append(ids, eventIDOf(a+"|"+key))
		}
		var before []string // ids printed while an account was linked to a principal it no longer is
		for _, a := range p.Accounts(c.principal) {
			for _, hp := range history[a] {
				before = append(before, eventIDOf(hp+"|"+key))
			}
		}
		switch {
		case ref == key || contains(ids, ref):
			exact = append(exact, c)
		case contains(before, ref):
			past = append(past, c)
		case strings.HasPrefix(key, ref) || (strings.HasPrefix(ref, "ev_") && hasPrefixIn(ids, ref)):
			prefix = append(prefix, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(exact) > 0 {
		return exact, nil
	}
	if len(past) > 0 { // an id of an earlier link names its event only when no event has it today
		return past, nil
	}
	return prefix, nil
}

// linkHistory maps each account of a source other than Teams to every principal it was ever linked
// to, ended links included, so an id printed while it was linked still resolves after the link
// ends.
func (s *Store) linkHistory(ctx context.Context) (map[string][]string, error) {
	rows, err := s.query(ctx, `select account_id, principal_id from calendar_account_links where source<>? order by account_id, principal_id`, string(calendar.SourceTeams))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var account, principal string
		if err := rows.Scan(&account, &principal); err != nil {
			return nil, err
		}
		out[account] = append(out[account], principal)
	}
	return out, rows.Err()
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasPrefixIn(list []string, prefix string) bool {
	for _, x := range list {
		if strings.HasPrefix(x, prefix) {
			return true
		}
	}
	return false
}

type attendeeJSON struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Type     string `json:"type"`
	Role     string `json:"role"`
	Response string `json:"response"`
}

func parseAttendees(raw string) []CalendarAttendee {
	if raw == "" {
		return nil
	}
	var in []attendeeJSON
	if json.Unmarshal([]byte(raw), &in) != nil {
		return nil
	}
	out := make([]CalendarAttendee, len(in))
	for i, a := range in {
		out[i] = CalendarAttendee(a)
	}
	return out
}

func nullTime(ns sql.NullString) time.Time { return parseTime(ns) }

func rawJSON(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

// recapRows loads the recaps that match cond (a condition over calendar_recaps) with their
// preferred items, oldest meeting first, and the account each belongs to.
func (s *Store) recapRows(ctx context.Context, cond string, args []any) ([]CalendarRecap, []string, error) {
	rows, err := s.query(ctx, `select account_id, call_id, recap_id, link_method, has_catchup, has_recap, headline, short_summary, outline,
		  summary_sections_json, speakers_json, topics_json, recording_url, recording_start_at, recording_end_at, duration_seconds, is_missed,
		  meeting_start_at, meeting_end_at, expires_at, attendance_status, attendees_count, has_conf_room_connected
		  from calendar_recaps where `+cond+` order by coalesce(meeting_start_at, recording_start_at, ''), call_id`, args...)
	if err != nil {
		return nil, nil, err
	}
	var out []CalendarRecap
	var accts []string
	for rows.Next() {
		var r CalendarRecap
		var acct, sections, speakers, topics string
		var rs, re, ms, me, ex sql.NullString
		if err := rows.Scan(&acct, &r.CallID, &r.RecapID, &r.LinkMethod, &r.HasCatchUp, &r.HasRecap, &r.Headline, &r.ShortSummary, &r.Outline,
			&sections, &speakers, &topics, &r.RecordingURL, &rs, &re, &r.DurationSeconds, &r.IsMissed, &ms, &me, &ex, &r.AttendanceStatus, &r.AttendeesCount, &r.HasConfRoomConnected); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		r.SummarySections, r.Speakers, r.Topics = rawJSON(sections), rawJSON(speakers), rawJSON(topics)
		r.RecordingStart, r.RecordingEnd, r.MeetingStart, r.MeetingEnd, r.ExpiresAt = nullTime(rs), nullTime(re), nullTime(ms), nullTime(me), nullTime(ex)
		out = append(out, r)
		accts = append(accts, acct)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	_ = rows.Close()
	for i := range out {
		items, err := s.recapItems(ctx, accts[i], out[i].CallID)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range items {
			if it.kind == calendar.ItemActionItem {
				out[i].ActionItems = append(out[i].ActionItems, it.CalendarRecapItem)
			} else {
				out[i].Mentions = append(out[i].Mentions, it.CalendarRecapItem)
			}
		}
	}
	return out, accts, nil
}

type kindedItem struct {
	kind string
	CalendarRecapItem
}

func (s *Store) recapItems(ctx context.Context, account, call string) ([]kindedItem, error) {
	rows, err := s.db.QueryContext(ctx, `select i.kind, i.origin, i.title, i.text, i.owner_name, i.speaker_name, i.at, i.mentioned_by, i.highlights_json
	  from calendar_recap_items i where i.account_id=? and i.call_id=? and `+preferredItems+` order by i.kind, i.ordinal, i.item_key`, account, call)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []kindedItem
	for rows.Next() {
		var k kindedItem
		var at sql.NullString
		var highlights string
		if err := rows.Scan(&k.kind, &k.Origin, &k.Title, &k.Text, &k.Owner, &k.Speaker, &at, &k.MentionedBy, &highlights); err != nil {
			return nil, err
		}
		k.At, k.Highlights = nullTime(at), rawJSON(highlights)
		out = append(out, k)
	}
	return out, rows.Err()
}

// series places an event in its series: the recurrence rule, the master row, and how many
// occurrences the archive holds.
func (s *Store) series(ctx context.Context, accounts []string, it calendar.AgendaItem) (CalendarSeries, error) {
	out := CalendarSeries{Key: it.SeriesKey}
	if it.RecurrenceJSON != "" {
		out.Rule = rawJSON(it.RecurrenceJSON)
	}
	if it.SeriesKey == "" {
		return out, nil
	}
	args := append(stringArgs(accounts), it.SeriesKey)
	in := inList(len(accounts))
	if err := s.db.QueryRowContext(ctx, `select count(distinct event_key) from calendar_source_events
	  where account_id in `+in+` and series_key=? and event_type<>'master' and removed_at is null`, args...).Scan(&out.Occurrences); err != nil {
		return out, err
	}
	var rule string
	err := s.db.QueryRowContext(ctx, `select event_key, recurrence_json from calendar_source_events
	  where account_id in `+in+` and series_key=? and event_type='master' order by removed_at is not null, event_key limit 1`, args...).Scan(&out.MasterKey, &rule)
	switch {
	case err == sql.ErrNoRows:
		return out, nil
	case err != nil:
		return out, err
	}
	out.HasMasterRow = true
	out.MasterID = EventID(it.Principal, out.MasterKey)
	if out.Rule == nil {
		out.Rule = rawJSON(rule)
	}
	return out, nil
}

// recordingCache holds the recordings of each meeting chat, computed once per read.
type recordingCache struct {
	s          *Store
	principals calendar.Principals
	byChat     map[string]*chatRecordings
}

// chatRecordings are a chat's recordings split by the occurrence they match (by event key).
type chatRecordings struct {
	matched map[string][]CalendarRecording
	series  []CalendarRecording // newest first
	chat    *CalendarChat
}

// cached is the recordings of a chat that of has already loaded.
func (c *recordingCache) cached(principal, chat string) *chatRecordings {
	return c.byChat[principal+"\x00"+chat]
}

func (c *recordingCache) of(ctx context.Context, principal, chat string) (*chatRecordings, error) {
	id := principal + "\x00" + chat
	if r, ok := c.byChat[id]; ok {
		return r, nil
	}
	r, err := c.s.matchRecordings(ctx, c.principals.Accounts(principal), chat)
	if err != nil {
		return nil, err
	}
	c.byChat[id] = r
	return r, nil
}

// recordingKinds maps the chat message types of a meeting's media to the kind printed.
var recordingKinds = map[string]string{
	"RichText/Media_CallRecording":  "recording",
	"RichText/Media_CallTranscript": "transcript",
	"Event/Call":                    "call_event",
}

type occurrence struct {
	key        string
	start, end time.Time
	windows    [][2]time.Time // recap recording windows, lag included
}

// matchRecordings splits a meeting chat's recordings by the occurrence each belongs to. A chat is
// shared by every occurrence of its series, so a message never matches by thread alone: it goes to
// the occurrence whose recap recording window (plus the lag) holds its time, else the occurrence
// whose own window (plus the lag) does; among several the latest start not after the message wins.
// A message that matches none is series-level.
func (s *Store) matchRecordings(ctx context.Context, accounts []string, chat string) (*chatRecordings, error) {
	out := &chatRecordings{matched: map[string][]CalendarRecording{}}
	args := append(stringArgs(accounts), chat)
	rows, err := s.query(ctx, `select event_key, min(start_at), max(end_at), max(ical_uid) from calendar_source_events
	  where account_id in `+inList(len(accounts))+` and teams_thread_id=? and event_type<>'master' and removed_at is null group by event_key`, args...)
	if err != nil {
		return nil, err
	}
	var occs []occurrence
	var uids []string
	for rows.Next() {
		var o occurrence
		var st, en, uid string
		if err := rows.Scan(&o.key, &st, &en, &uid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		o.start, o.end = parseTime(sql.NullString{String: st, Valid: true}), parseTime(sql.NullString{String: en, Valid: true})
		occs = append(occs, o)
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	for i := range occs {
		if uids[i] == "" {
			continue
		}
		wr, err := s.query(ctx, `select recording_start_at, recording_end_at from calendar_recaps
		  where account_id in `+inList(len(accounts))+` and ical_uid=? and recording_start_at is not null and recording_end_at is not null`, append(stringArgs(accounts), uids[i])...)
		if err != nil {
			return nil, err
		}
		for wr.Next() {
			var a, b string
			if err := wr.Scan(&a, &b); err != nil {
				_ = wr.Close()
				return nil, err
			}
			occs[i].windows = append(occs[i].windows, [2]time.Time{parseTime(sql.NullString{String: a, Valid: true}), parseTime(sql.NullString{String: b, Valid: true}).Add(calendar.RecordingLag)})
		}
		if err := wr.Err(); err != nil {
			_ = wr.Close()
			return nil, err
		}
		_ = wr.Close()
	}
	msgs, err := s.chatMessages(ctx, accounts, chat)
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if key, by := matchOccurrence(occs, m.SentAt); key != "" {
			m.MatchedBy = by
			out.matched[key] = append(out.matched[key], m)
		} else {
			out.series = append(out.series, m)
		}
	}
	sort.SliceStable(out.series, func(i, j int) bool { return out.series[i].SentAt.After(out.series[j].SentAt) })
	if out.chat, err = s.chatOf(ctx, accounts, chat); err != nil {
		return nil, err
	}
	return out, nil
}

// matchOccurrence picks the occurrence a message sent at t belongs to, and how it matched.
func matchOccurrence(occs []occurrence, t time.Time) (key, by string) {
	pick := func(match func(occurrence) bool) *occurrence {
		var best *occurrence
		for i := range occs {
			o := &occs[i]
			if !match(*o) {
				continue
			}
			// The latest start not after the message wins; an occurrence starting after it only
			// when none starts before; the key breaks a tie so the choice never depends on row order.
			switch {
			case best == nil:
			case o.start.After(t) != best.start.After(t):
				if o.start.After(t) {
					continue
				}
			case !o.start.Equal(best.start):
				if o.start.Before(best.start) == !o.start.After(t) {
					continue
				}
			case o.key > best.key:
				continue
			}
			best = o
		}
		return best
	}
	if o := pick(func(o occurrence) bool {
		for _, w := range o.windows {
			if !t.Before(w[0]) && !t.After(w[1]) {
				return true
			}
		}
		return false
	}); o != nil {
		return o.key, "recap"
	}
	if o := pick(func(o occurrence) bool {
		return !o.start.IsZero() && !t.Before(o.start) && !t.After(o.end.Add(calendar.RecordingLag))
	}); o != nil {
		return o.key, "window"
	}
	return "", ""
}

func (s *Store) chatMessages(ctx context.Context, accounts []string, chat string) ([]CalendarRecording, error) {
	args := append([]any{chat}, stringArgs(accounts)...)
	rows, err := s.query(ctx, `select id, conversation_id, sent_at, message_type, content_text, link from messages
	  where conversation_id=? and tenant_id||'/'||user_id in `+inList(len(accounts))+` and deleted_at is null
	  and message_type in ('RichText/Media_CallRecording','RichText/Media_CallTranscript','Event/Call') order by sent_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CalendarRecording
	for rows.Next() {
		var m CalendarRecording
		var sent, typ string
		if err := rows.Scan(&m.MessageID, &m.ConversationID, &sent, &typ, &m.Text, &m.Link); err != nil {
			return nil, err
		}
		m.SentAt, m.Kind = parseTime(sql.NullString{String: sent, Valid: true}), recordingKinds[typ]
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) chatOf(ctx context.Context, accounts []string, chat string) (*CalendarChat, error) {
	args := append([]any{chat}, stringArgs(accounts)...)
	var c CalendarChat
	var title, display string
	var tenant, user string
	err := s.db.QueryRowContext(ctx, `select tenant_id, user_id, title, display_name from conversations
	  where id=? and tenant_id||'/'||user_id in `+inList(len(accounts))+` limit 1`, args...).Scan(&tenant, &user, &title, &display)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, err
	}
	c.ConversationID, c.DisplayName = chat, display
	if c.DisplayName == "" {
		c.DisplayName = title
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from messages where tenant_id=? and user_id=? and conversation_id=?`, tenant, user, chat).Scan(&c.MessageCount); err != nil {
		return nil, err
	}
	return &c, nil
}
