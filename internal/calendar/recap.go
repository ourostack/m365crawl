package calendar

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// RecordingLag is how long after a meeting ends a recording or transcript may still post to the
// meeting chat. It is an assumption (A11 in the plan): the real-cache acceptance reports the
// observed gaps. Callers that match chat messages to occurrences use it as the window's tail.
const RecordingLag = 4 * time.Hour

// DefaultRecapTolerance is how far a recap's meeting start and end may differ from an event's and
// still link by time. It is a guess until the real-cache acceptance reports the distribution; the
// source adapter owns the tuned value and passes it in ApplyOptions.
const DefaultRecapTolerance = 60 * time.Second

// Link methods recorded on a recap.
const (
	LinkICalUID = "ical_uid"
	LinkTime    = "time"
)

// Recap item kinds and origins.
const (
	ItemActionItem = "action_item"
	ItemMention    = "mention"
	OriginCatchUp  = "catchup" // the meeting catch-up store
	OriginRecap    = "recap"   // the meeting recap store
)

// Recap is what a source keeps about one call of a meeting: its summary, speakers, recording and
// status. Both Teams stores fill the same row, each its own columns.
type Recap struct {
	AccountID string
	CallID    string
	// ICalUID joins the recap to calendar_source_events.ical_uid.
	ICalUID    string
	RecapID    string
	LinkMethod string
	// HasCatchUp and HasRecap say which store contributed.
	HasCatchUp, HasRecap bool

	Headline, ShortSummary, Outline, SummarySectionsJSON string
	SpeakersJSON, TopicsJSON                             string

	RecordingURL                     string
	RecordingStartAt, RecordingEndAt *time.Time
	DurationSeconds                  int
	ExpiresAt                        *time.Time

	MeetingStartAt, MeetingEndAt *time.Time
	IsMissed                     bool
	AttendanceStatus             string
	AttendeesCount               int
	OrganizerID                  string
	HasConfRoomConnected         bool

	// Bookkeeping, maintained by the store.
	FirstSeenAt, UpdatedAt time.Time
}

// RecapItem is one action item or mention of a call.
type RecapItem struct {
	AccountID, CallID string
	// ItemKey is ItemKey(...) of the item; ApplyBatch fills it when empty.
	ItemKey        string
	Kind, Origin   string
	Title, Text    string
	OwnerName      string
	SpeakerName    string
	At             *time.Time
	MentionedBy    string
	HighlightsJSON string
	Ordinal        int

	// Bookkeeping, maintained by the store. SupersededAt is a sticky tombstone: a newer read of the
	// same set that no longer holds the item sets it, and nothing deletes the row.
	FirstSeenAt, UpdatedAt time.Time
	SupersededAt           *time.Time
}

// ItemKey is the stable id of an item within its call: the first 16 hex characters of the sha256
// of kind, origin, ordinal and the whitespace-normalized, lower-cased title and text.
func ItemKey(kind, origin string, ordinal int, title, text string) string {
	norm := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%s", kind, origin, ordinal, norm(title), norm(text))))
	return hex.EncodeToString(sum[:])[:16]
}

// CaptureRecap is the recap half of the append-only rule. Recaps carry no modification time, so
// the rule works per field group: a non-empty incoming group replaces the stored group, and an
// empty one never does. The groups are the summary (headline, short summary, outline, summary
// sections), the people (speakers, topics), the recording (url, start, end, duration, expiry) and
// the status (meeting times, missed flag, attendance, counts, organizer, conference room). The
// two stores therefore fill only their own columns and neither erases the other. An incoming
// iCalUID or recap id replaces the stored one only when non-empty; the store flags are sticky.
func CaptureRecap(old *Recap, in Recap) Recap {
	var out Recap
	if old != nil {
		out = *old
	}
	out.AccountID, out.CallID = in.AccountID, in.CallID
	if in.ICalUID != "" {
		out.ICalUID, out.LinkMethod = in.ICalUID, in.LinkMethod
		if out.LinkMethod == "" {
			out.LinkMethod = LinkICalUID
		}
	}
	if in.RecapID != "" {
		out.RecapID = in.RecapID
	}
	out.HasCatchUp = out.HasCatchUp || in.HasCatchUp
	out.HasRecap = out.HasRecap || in.HasRecap
	if !summaryEmpty(in) {
		out.Headline, out.ShortSummary, out.Outline, out.SummarySectionsJSON = in.Headline, in.ShortSummary, in.Outline, in.SummarySectionsJSON
	}
	if in.SpeakersJSON != "" || in.TopicsJSON != "" {
		out.SpeakersJSON, out.TopicsJSON = in.SpeakersJSON, in.TopicsJSON
	}
	if in.RecordingURL != "" || in.RecordingStartAt != nil || in.RecordingEndAt != nil || in.DurationSeconds != 0 || in.ExpiresAt != nil {
		out.RecordingURL, out.RecordingStartAt, out.RecordingEndAt = in.RecordingURL, in.RecordingStartAt, in.RecordingEndAt
		out.DurationSeconds, out.ExpiresAt = in.DurationSeconds, in.ExpiresAt
	}
	if in.MeetingStartAt != nil || in.MeetingEndAt != nil || in.IsMissed || in.AttendanceStatus != "" ||
		in.AttendeesCount != 0 || in.OrganizerID != "" || in.HasConfRoomConnected {
		out.MeetingStartAt, out.MeetingEndAt, out.IsMissed = in.MeetingStartAt, in.MeetingEndAt, in.IsMissed
		out.AttendanceStatus, out.AttendeesCount, out.OrganizerID = in.AttendanceStatus, in.AttendeesCount, in.OrganizerID
		out.HasConfRoomConnected = in.HasConfRoomConnected
	}
	return out
}

func summaryEmpty(r Recap) bool {
	return r.Headline == "" && r.ShortSummary == "" && r.Outline == "" && r.SummarySectionsJSON == ""
}

// recapValues renders the stored columns of r (bookkeeping excluded) for comparison and insert.
func recapValues(r Recap) []any {
	return []any{
		r.AccountID, r.CallID, r.ICalUID, r.RecapID, r.LinkMethod, r.HasCatchUp, r.HasRecap,
		r.Headline, r.ShortSummary, r.Outline, r.SummarySectionsJSON, r.SpeakersJSON, r.TopicsJSON,
		r.RecordingURL, formatTimePtr(r.RecordingStartAt), formatTimePtr(r.RecordingEndAt), r.DurationSeconds, r.IsMissed,
		formatTimePtr(r.MeetingStartAt), formatTimePtr(r.MeetingEndAt), formatTimePtr(r.ExpiresAt),
		r.AttendanceStatus, r.AttendeesCount, r.OrganizerID, r.HasConfRoomConnected,
	}
}

const recapColumns = `account_id, call_id, ical_uid, recap_id, link_method, has_catchup, has_recap, headline, short_summary, outline,
  summary_sections_json, speakers_json, topics_json, recording_url, recording_start_at, recording_end_at, duration_seconds,
  is_missed, meeting_start_at, meeting_end_at, expires_at, attendance_status, attendees_count, organizer_id, has_conf_room_connected`

const upsertRecapSQL = `INSERT INTO calendar_recaps (` + recapColumns + `, first_seen_at, updated_at) VALUES (` +
	`?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
  ON CONFLICT(account_id, call_id) DO UPDATE SET ical_uid=excluded.ical_uid, recap_id=excluded.recap_id,
  link_method=excluded.link_method, has_catchup=excluded.has_catchup, has_recap=excluded.has_recap,
  headline=excluded.headline, short_summary=excluded.short_summary, outline=excluded.outline,
  summary_sections_json=excluded.summary_sections_json, speakers_json=excluded.speakers_json,
  topics_json=excluded.topics_json, recording_url=excluded.recording_url,
  recording_start_at=excluded.recording_start_at, recording_end_at=excluded.recording_end_at,
  duration_seconds=excluded.duration_seconds, is_missed=excluded.is_missed,
  meeting_start_at=excluded.meeting_start_at, meeting_end_at=excluded.meeting_end_at,
  expires_at=excluded.expires_at, attendance_status=excluded.attendance_status,
  attendees_count=excluded.attendees_count, organizer_id=excluded.organizer_id,
  has_conf_room_connected=excluded.has_conf_room_connected, updated_at=excluded.updated_at`

// loadRecap reads the stored recap of a call, or nil when there is none.
func loadRecap(ctx context.Context, q querier, account, call string) (*Recap, error) {
	var r Recap
	var rs, re, ms, me, ex, first, upd timeText
	err := q.QueryRowContext(ctx, `SELECT `+recapColumns+`, first_seen_at, updated_at FROM calendar_recaps WHERE account_id=? AND call_id=?`,
		account, call).Scan(&r.AccountID, &r.CallID, &r.ICalUID, &r.RecapID, &r.LinkMethod, &r.HasCatchUp, &r.HasRecap,
		&r.Headline, &r.ShortSummary, &r.Outline, &r.SummarySectionsJSON, &r.SpeakersJSON, &r.TopicsJSON,
		&r.RecordingURL, &rs, &re, &r.DurationSeconds, &r.IsMissed, &ms, &me, &ex,
		&r.AttendanceStatus, &r.AttendeesCount, &r.OrganizerID, &r.HasConfRoomConnected, &first, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.RecordingStartAt, r.RecordingEndAt, r.MeetingStartAt, r.MeetingEndAt, r.ExpiresAt = rs.ptr(), re.ptr(), ms.ptr(), me.ptr(), ex.ptr()
	r.FirstSeenAt, r.UpdatedAt = first.t, upd.t
	return &r, nil
}

// querier is the read side shared by *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// applyRecap captures one recap into the table and reports whether it was new, changed or unchanged.
func applyRecap(ctx context.Context, tx *sql.Tx, in Recap, at time.Time) (outcome, error) {
	old, err := loadRecap(ctx, tx, in.AccountID, in.CallID)
	if err != nil {
		return 0, err
	}
	got := CaptureRecap(old, in)
	got.FirstSeenAt, got.UpdatedAt = at, at
	result := outcomeNew
	if old != nil {
		got.FirstSeenAt = old.FirstSeenAt
		if reflect.DeepEqual(recapValues(*old), recapValues(got)) {
			return outcomeUnchanged, nil
		}
		result = outcomeChanged
	}
	args := append(recapValues(got), formatTime(got.FirstSeenAt), formatTime(got.UpdatedAt))
	_, err = tx.ExecContext(ctx, upsertRecapSQL, args...)
	return result, err
}

// setKey identifies one set of items of a call: the unit the supersede rule works on.
type setKey struct{ account, call, kind, origin string }

// applyItems applies a batch's items. For each (call, kind, origin) set with at least one
// incoming item, the items are upserted (clearing superseded_at) and stored items of that set
// missing from the incoming set get superseded_at. A set that is absent changes nothing.
func applyItems(ctx context.Context, tx *sql.Tx, items []RecapItem, at time.Time) (Counts, error) {
	var counts Counts
	sets := map[setKey][]RecapItem{}
	for _, it := range items {
		if it.ItemKey == "" {
			it.ItemKey = ItemKey(it.Kind, it.Origin, it.Ordinal, it.Title, it.Text)
		}
		k := setKey{it.AccountID, it.CallID, it.Kind, it.Origin}
		sets[k] = append(sets[k], it)
	}
	keys := make([]setKey, 0, len(sets))
	for k := range sets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	for _, k := range keys {
		stored, err := loadItems(ctx, tx, k)
		if err != nil {
			return counts, err
		}
		incoming := map[string]bool{}
		for _, it := range sets[k] {
			incoming[it.ItemKey] = true
			old, had := stored[it.ItemKey]
			res, err := upsertItem(ctx, tx, it, old, had, at)
			if err != nil {
				return counts, err
			}
			counts.add(res)
		}
		var missing []string
		for key, it := range stored {
			if !incoming[key] && it.SupersededAt == nil {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		for _, key := range missing {
			if _, err := tx.ExecContext(ctx,
				`UPDATE calendar_recap_items SET superseded_at=?, updated_at=? WHERE account_id=? AND call_id=? AND item_key=?`,
				formatTime(at), formatTime(at), k.account, k.call, key); err != nil {
				return counts, err
			}
			counts.Changed++
		}
	}
	return counts, nil
}

func itemValues(it RecapItem) []any {
	return []any{it.Kind, it.Origin, it.Title, it.Text, it.OwnerName, it.SpeakerName, formatTimePtr(it.At),
		it.MentionedBy, it.HighlightsJSON, it.Ordinal}
}

func upsertItem(ctx context.Context, tx *sql.Tx, it, old RecapItem, had bool, at time.Time) (outcome, error) {
	res := outcomeNew
	first := at
	if had {
		first = old.FirstSeenAt
		if old.SupersededAt == nil && reflect.DeepEqual(itemValues(old), itemValues(it)) {
			return outcomeUnchanged, nil
		}
		res = outcomeChanged
	}
	args := append([]any{it.AccountID, it.CallID, it.ItemKey}, itemValues(it)...)
	args = append(args, formatTime(first), formatTime(at))
	_, err := tx.ExecContext(ctx, `INSERT INTO calendar_recap_items
	  (account_id, call_id, item_key, kind, origin, title, text, owner_name, speaker_name, at, mentioned_by, highlights_json, ordinal, first_seen_at, updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(account_id, call_id, item_key) DO UPDATE SET kind=excluded.kind, origin=excluded.origin, title=excluded.title,
	  text=excluded.text, owner_name=excluded.owner_name, speaker_name=excluded.speaker_name, at=excluded.at,
	  mentioned_by=excluded.mentioned_by, highlights_json=excluded.highlights_json, ordinal=excluded.ordinal,
	  updated_at=excluded.updated_at, superseded_at=NULL`, args...)
	return res, err
}

// loadItems reads the stored items of one set, by item key.
func loadItems(ctx context.Context, tx *sql.Tx, k setKey) (map[string]RecapItem, error) {
	rows, err := tx.QueryContext(ctx, `SELECT item_key, kind, origin, title, text, owner_name, speaker_name, at, mentioned_by,
	  highlights_json, ordinal, first_seen_at, superseded_at FROM calendar_recap_items
	  WHERE account_id=? AND call_id=? AND kind=? AND origin=?`, k.account, k.call, k.kind, k.origin)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]RecapItem{}
	for rows.Next() {
		var it RecapItem
		var when, first, sup timeText
		if err := scanRow(rows, &it.ItemKey, &it.Kind, &it.Origin, &it.Title, &it.Text, &it.OwnerName, &it.SpeakerName, &when,
			&it.MentionedBy, &it.HighlightsJSON, &it.Ordinal, &first, &sup); err != nil {
			return nil, err
		}
		it.At, it.FirstSeenAt, it.SupersededAt = when.ptr(), first.t, sup.ptr()
		out[it.ItemKey] = it
	}
	return out, rowsErr(rows)
}

// LinkRecaps links recaps that have no iCalUID to events by time. A recap in accountID whose
// meeting start and end are each within tolerance of the start and end of exactly one live,
// non-master, timed event that has an iCalUID takes that event's iCalUID with link method "time".
// Zero or several candidates leave the recap unlinked, never guessed. A recap that already has an
// iCalUID is never relinked. It returns the number of recaps linked. A recap links later, when
// its event appears.
func LinkRecaps(ctx context.Context, tx *sql.Tx, accountID string, tolerance time.Duration, at time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT call_id, meeting_start_at, meeting_end_at FROM calendar_recaps
	  WHERE account_id=? AND ical_uid='' AND meeting_start_at IS NOT NULL AND meeting_end_at IS NOT NULL ORDER BY call_id`, accountID)
	if err != nil {
		return 0, err
	}
	type pending struct {
		call       string
		start, end time.Time
	}
	var todo []pending
	for rows.Next() {
		var p pending
		var s, e timeText
		if err := scanRow(rows, &p.call, &s, &e); err != nil {
			_ = rows.Close()
			return 0, err
		}
		p.start, p.end = s.t, e.t
		todo = append(todo, p)
	}
	if err := rowsErr(rows); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	if len(todo) == 0 {
		return 0, nil
	}
	cands, err := timedEvents(ctx, tx, accountID)
	if err != nil {
		return 0, err
	}
	linked := 0
	for _, p := range todo {
		var match []string
		for _, c := range cands {
			if within(c.start, p.start, tolerance) && within(c.end, p.end, tolerance) {
				match = append(match, c.uid)
			}
		}
		if match = distinct(match); len(match) != 1 || match[0] == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE calendar_recaps SET ical_uid=?, link_method=?, updated_at=? WHERE account_id=? AND call_id=? AND ical_uid=''`,
			match[0], LinkTime, formatTime(at), accountID, p.call); err != nil {
			return linked, err
		}
		linked++
	}
	return linked, nil
}

// distinct de-duplicates uids: one event held by two sources is still one event. An event with no
// uid keeps its empty entry, which makes the match unusable.
func distinct(uids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, u := range uids {
		if u == "" || !seen[u] {
			out = append(out, u)
			seen[u] = true
		}
	}
	return out
}

func within(a, b time.Time, tol time.Duration) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d <= tol
}

type timedEvent struct {
	uid        string
	start, end time.Time
}

// timedEvents lists the live, non-master, timed events of an account.
func timedEvents(ctx context.Context, tx *sql.Tx, accountID string) ([]timedEvent, error) {
	rows, err := tx.QueryContext(ctx, `SELECT ical_uid, start_at, end_at FROM calendar_source_events
	  WHERE account_id=? AND removed_at IS NULL AND all_day=0 AND event_type<>? AND start_at<>'' ORDER BY source, event_key`,
		accountID, EventMaster)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []timedEvent
	for rows.Next() {
		var c timedEvent
		var s, e timeText
		if err := scanRow(rows, &c.uid, &s, &e); err != nil {
			return nil, err
		}
		c.start, c.end = s.t, e.t
		out = append(out, c)
	}
	return out, rowsErr(rows)
}
