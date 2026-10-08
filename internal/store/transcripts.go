package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

// transcriptMapperKey is the meta row that says which transcripts.PartsMapper derived
// transcript_parts.
const transcriptMapperKey = "transcript_parts_mapper"

// TranscriptCounts is what the transcript parts of one or more accounts add up to after a
// derivation. Parts leaves out the rows that stand for an unresolved call, which Unresolved
// counts; Unparsable counts the recording notices that could not be read.
type TranscriptCounts struct {
	Calls      int `json:"calls"`
	Parts      int `json:"parts"`
	DriveItem  int `json:"drive_item"`
	ShareOnly  int `json:"share_only"`
	AMSOnly    int `json:"ams_only"`
	Unresolved int `json:"unresolved"`
	Unparsable int `json:"unparsable"`
}

// Add adds b to c.
func (c *TranscriptCounts) Add(b TranscriptCounts) {
	c.Calls += b.Calls
	c.Parts += b.Parts
	c.DriveItem += b.DriveItem
	c.ShareOnly += b.ShareOnly
	c.AMSOnly += b.AMSOnly
	c.Unresolved += b.Unresolved
	c.Unparsable += b.Unparsable
}

// DeriveTranscriptParts rebuilds transcript_parts for the given accounts ("<tenantId>/<userId>")
// from the recording and transcript notices among their messages, inside the session. It returns
// each account's counts. It never touches transcript_fetches or transcript_entries: what a fetch
// stored outlives the part row it was fetched for.
func (x *Session) DeriveTranscriptParts(ctx context.Context, accounts []string) (map[string]TranscriptCounts, error) {
	return deriveTranscriptParts(ctx, x.tx, accounts)
}

const insertPart = `insert into transcript_parts(account_id, call_id, part_key, thread_id, message_id, ordinal, starts_at, duration_seconds,
  content_types, chunk_index, transcribe_only, host, site_root, storage_kind, drive_id, item_id, transcript_id, share_url, ref_quality,
  meeting_ical_uid, original_name, sent_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func deriveTranscriptParts(ctx context.Context, tx *sql.Tx, accounts []string) (map[string]TranscriptCounts, error) {
	out := map[string]TranscriptCounts{}
	for _, account := range accounts {
		tenant, user, _ := strings.Cut(account, "/")
		var parts []transcripts.Part
		notices := map[string]transcripts.Notice{}
		var n TranscriptCounts
		// The system pseudo-conversations mirror real messages, so a notice is read once, from its chat.
		err := eachRow(ctx, tx, `select conversation_id, id, sent_at, message_type, content_html from messages
		  where tenant_id=? and user_id=? and message_type in (?,?) and deleted_at is null and `+notSystemCond(`conversation_id`)+`
		  order by conversation_id, id`, []any{tenant, user, transcripts.TypeRecording, transcripts.TypeTranscript}, func(rows *sql.Rows) error {
			var thread, id, sent, typ, content string
			if err := rows.Scan(&thread, &id, &sent, &typ, &content); err != nil {
				return err
			}
			at := parseTime(sql.NullString{String: sent, Valid: true})
			if typ == transcripts.TypeTranscript {
				if call, ok := transcripts.ParseTranscriptNotice(content); ok {
					if old, seen := notices[call]; !seen || at.After(old.SentAt) {
						notices[call] = transcripts.Notice{ThreadID: thread, MessageID: id, SentAt: at}
					}
				}
				return nil
			}
			p, ok, err := transcripts.ParseRecording(id, thread, content, at)
			if err != nil {
				n.Unparsable++ // counted, never fatal: one bad notice does not cost the others
				return nil
			}
			if ok {
				parts = append(parts, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `delete from transcript_parts where account_id=?`, account); err != nil {
			return nil, err
		}
		calls := map[string]bool{}
		for _, p := range transcripts.Assemble(parts, notices) {
			if _, err := tx.ExecContext(ctx, insertPart, account, p.CallID, p.PartKey, p.ThreadID, p.MessageID, p.Ordinal, fmtTime(p.StartsAt), p.DurationSeconds,
				p.ContentTypes, p.ChunkIndex, p.TranscribeOnly, p.Host, p.SiteRoot, p.StorageKind, p.DriveID, p.ItemID, p.TranscriptID, p.ShareURL, p.RefQuality,
				p.MeetingICalUID, p.OriginalName, fmtTime(p.SentAt)); err != nil {
				return nil, err
			}
			calls[p.CallID] = true
			switch p.RefQuality {
			case transcripts.RefUnresolved:
				n.Unresolved++
				continue
			case transcripts.RefDriveItem:
				n.DriveItem++
			case transcripts.RefShareOnly:
				n.ShareOnly++
			default:
				n.AMSOnly++
			}
			n.Parts++
		}
		n.Calls = len(calls)
		out[account] = n
	}
	return out, nil
}

// EnsureTranscriptParts rebuilds the transcript parts of every account when they were derived by
// an older transcripts.PartsMapper, or never (an archive from before the transcript tables). It
// needs nothing but the messages the archive already holds. It returns each account's counts, or
// nil when nothing was due.
func (s *Store) EnsureTranscriptParts(ctx context.Context) (map[string]TranscriptCounts, error) {
	var out map[string]TranscriptCounts
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var stored string
		switch err := tx.QueryRowContext(ctx, `select value from meta where key=?`, transcriptMapperKey).Scan(&stored); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		}
		if have, _ := strconv.Atoi(stored); have >= transcripts.PartsMapper {
			return nil
		}
		var accounts []string
		err := eachRow(ctx, tx, `select tenant_id||'/'||user_id from messages where message_type in (?,?)
		  union select account_id from transcript_parts order by 1`, []any{transcripts.TypeRecording, transcripts.TypeTranscript}, func(rows *sql.Rows) error {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			accounts = append(accounts, a)
			return nil
		})
		if err != nil {
			return err
		}
		if out, err = deriveTranscriptParts(ctx, tx, accounts); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, transcriptMapperKey, strconv.Itoa(transcripts.PartsMapper))
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SaveTranscript stores the outcome of fetching one part, in one transaction. A fetch that got the
// text replaces the part's entries and their index rows. Any other outcome records the state and
// the time of the attempt and keeps what an earlier fetch stored: losing access later never erases
// text the archive already holds.
func (s *Store) SaveTranscript(ctx context.Context, account, partKey string, r transcripts.FetchResult) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		at := fmtTime(r.At)
		if r.State != transcripts.StateOK {
			_, err := tx.ExecContext(ctx, `insert into transcript_fetches(account_id, part_key, state, http_status, browser, attempted_at) values(?,?,?,?,?,?)
			  on conflict(account_id, part_key) do update set state=excluded.state, http_status=excluded.http_status, browser=excluded.browser, attempted_at=excluded.attempted_at`,
				account, partKey, r.State, r.HTTPStatus, r.Browser, at)
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into transcript_fetches(account_id, part_key, state, fetched_at, http_status, entry_count, browser, attempted_at) values(?,?,?,?,?,?,?,?)
		  on conflict(account_id, part_key) do update set state=excluded.state, fetched_at=excluded.fetched_at, http_status=excluded.http_status,
		  entry_count=excluded.entry_count, browser=excluded.browser, attempted_at=excluded.attempted_at`,
			account, partKey, r.State, at, r.HTTPStatus, len(r.Entries), r.Browser, at); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from transcript_fts where rowid in (select rowid from transcript_entries where account_id=? and part_key=?)`, account, partKey); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from transcript_entries where account_id=? and part_key=?`, account, partKey); err != nil {
			return err
		}
		ins, err := tx.PrepareContext(ctx, `insert into transcript_entries(account_id, part_key, ord, speaker, start_ms, end_ms, text) values(?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer func() { _ = ins.Close() }()
		for i, e := range r.Entries {
			if _, err := ins.ExecContext(ctx, account, partKey, i, e.Speaker, e.StartMS, e.EndMS, e.Text); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `insert into transcript_fts(rowid, speaker, text) select rowid, speaker, text from transcript_entries where account_id=? and part_key=?`, account, partKey)
		return err
	})
}

// TranscriptEntries returns the stored entries of one part, in order.
func (s *Store) TranscriptEntries(ctx context.Context, account, partKey string) ([]transcripts.Entry, error) {
	rows, err := s.query(ctx, `select speaker, start_ms, end_ms, text from transcript_entries where account_id=? and part_key=? order by ord`, account, partKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []transcripts.Entry
	for rows.Next() {
		var e transcripts.Entry
		var start, end sql.NullInt64
		if err := rows.Scan(&e.Speaker, &start, &end, &e.Text); err != nil {
			return nil, err
		}
		if start.Valid {
			e.StartMS = &start.Int64
		}
		if end.Valid {
			e.EndMS = &end.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HasTranscriptTables reports whether the archive has the transcript tables: an archive written
// before them gains them on its next sync.
func (s *Store) HasTranscriptTables(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name in ('transcript_parts','transcript_fetches','transcript_entries')`).Scan(&n)
	return n == 3, err
}

// The kinds of reference ResolveMeeting tells apart.
const (
	MeetingByCall   = "call"
	MeetingByThread = "thread"
	MeetingByEvent  = "event"
)

// ResolveMeeting finds the recorded calls a reference names, newest first, and says what kind of
// reference it was. It tries, in order: a call id; the id of a meeting chat, which names every
// call recorded in it; and a calendar event (an event id, an event key or an unambiguous prefix),
// which names the calls whose recording notices belong to that occurrence and the calls of its
// recaps. A reference that names no recorded call is errs.UnknownMeeting; one that matches several
// events is the calendar's own usage error.
func (s *Store) ResolveMeeting(ctx context.Context, acct *teamsdesktop.Account, ref string) (calls []string, kind string, err error) {
	if ref == "" {
		return nil, "", errs.UnknownMeeting(ref) // the calendar reads an empty reference as a prefix of every event
	}
	scope, args := "", []any(nil)
	if acct != nil {
		scope, args = ` and account_id=?`, []any{accountString(acct)}
	}
	const newestFirst = ` group by call_id order by max(coalesce(starts_at, sent_at)) desc, call_id`
	for _, by := range []struct{ kind, col string }{{MeetingByCall, "call_id"}, {MeetingByThread, "thread_id"}} {
		if calls, err = s.callIDs(ctx, `select call_id from transcript_parts where `+by.col+`=?`+scope+newestFirst, append([]any{ref}, args...)...); err != nil || len(calls) > 0 {
			return calls, by.kind, err
		}
	}
	d, found, err := s.calendarEvent(ctx, acct, ref)
	switch {
	case errors.Is(err, ErrNoCalendarTables):
		return nil, "", errs.UnknownMeeting(ref)
	case err != nil:
		return nil, "", err
	case !found || d.TeamsThreadID == "":
		return nil, "", errs.UnknownMeeting(ref)
	}
	// The occurrence names its calls through the notices that fell in its window and its recaps.
	// A part keeps only the newest notice of its file, which Teams may re-post days later, so the
	// parts are matched by call id, never by the message id they kept.
	var ids []any
	if ids, err = s.recordingCalls(ctx, acct, d.TeamsThreadID, d.Recordings); err != nil {
		return nil, "", err
	}
	for _, r := range d.Recaps {
		if !r.SeriesLevel { // a recap of the whole series is no call of this occurrence
			ids = append(ids, r.CallID)
		}
	}
	if len(ids) == 0 {
		return nil, "", errs.UnknownMeeting(ref)
	}
	q := `select call_id from transcript_parts where thread_id=? and call_id in ` + inList(len(ids)) + scope + newestFirst
	if calls, err = s.callIDs(ctx, q, append(append([]any{d.TeamsThreadID}, ids...), args...)...); err != nil {
		return nil, "", err
	}
	if len(calls) == 0 {
		return nil, "", errs.UnknownMeeting(ref)
	}
	return calls, MeetingByEvent, nil
}

// noticeCall is the call a recording notice (with status Success) or a transcript notice names;
// empty for any other message.
func noticeCall(typ, html string) string {
	switch typ {
	case transcripts.TypeRecording:
		if p, ok, _ := transcripts.ParseRecording("", "", html, time.Time{}); ok {
			return p.CallID
		}
	case transcripts.TypeTranscript:
		call, _ := transcripts.ParseTranscriptNotice(html)
		return call
	}
	return ""
}

// recordingCalls is the distinct calls the notices among recs name, read from their messages.
func (s *Store) recordingCalls(ctx context.Context, acct *teamsdesktop.Account, thread string, recs []CalendarRecording) ([]any, error) {
	if len(recs) == 0 {
		return nil, nil
	}
	args := []any{thread}
	for _, r := range recs {
		args = append(args, r.MessageID)
	}
	q := `select message_type, content_html from messages where conversation_id=? and id in ` + inList(len(recs)) + ` and deleted_at is null`
	if acct != nil {
		q, args = q+` and tenant_id=? and user_id=?`, append(args, acct.TenantID, acct.UserID)
	}
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []any
	seen := map[string]bool{}
	for rows.Next() {
		var typ, html string
		if err := rows.Scan(&typ, &html); err != nil {
			return nil, err
		}
		if call := noticeCall(typ, html); call != "" && !seen[call] {
			seen[call] = true
			out = append(out, call)
		}
	}
	return out, rows.Err()
}

func (s *Store) callIDs(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// The states of a call's transcript in the archive.
const (
	CallFetched     = "fetched"     // the text of every fetchable part is in the archive
	CallPartial     = "partial"     // the text of some fetchable parts is
	CallNotFetched  = "not_fetched" // no text yet
	CallUnfetchable = "unfetchable" // no part a fetch can ask for
)

// TranscriptFilter selects recorded calls.
type TranscriptFilter struct {
	Account *teamsdesktop.Account // nil: every account
	// Calls keeps only these call ids; empty keeps every call.
	Calls        []string
	Since, Until time.Time // by the call's start: at or after Since, before Until
	State        string    // one of the Call states; empty keeps every state
	Limit        int       // 0 or less: no limit
	// Speaker keeps only entries whose speaker contains this text, ignoring case. Only
	// TranscriptSearch reads it, and for TranscriptSearch Since and Until bound the time of each
	// entry, not the start of its call.
	Speaker string
}

// FetchRow is what the archive holds about the fetch of one part. State is the outcome of the
// last attempt; FetchedAt and EntryCount describe the text in the archive, which an earlier
// attempt may have stored.
type FetchRow struct {
	State                  string
	FetchedAt, AttemptedAt *time.Time
	HTTPStatus, EntryCount int
	Browser                string
}

// TranscriptPart is one part of a call with its fetch state. Reason says why the part has no text,
// empty when it has.
type TranscriptPart struct {
	transcripts.Part
	Fetchable bool
	Reason    string
	Fetch     *FetchRow
}

// HasText reports whether the archive holds the part's text.
func (p TranscriptPart) HasText() bool { return p.Fetch != nil && p.Fetch.FetchedAt != nil }

// State is the part's state as transcripts.PartState defines it.
func (p TranscriptPart) State() string {
	last := ""
	if p.Fetch != nil {
		last = p.Fetch.State
	}
	return transcripts.PartState(p.Part, last, p.HasText())
}

// TranscriptCall is one recorded call with its parts in order. Title is the name of its meeting
// chat and EventKey the calendar occurrence its recording notices belong to; both are empty when
// the archive cannot say.
type TranscriptCall struct {
	AccountID, CallID, ThreadID, EventKey, Title string
	StartedAt                                    time.Time
	Parts                                        []TranscriptPart
	State                                        string
}

// Fetchable counts the parts a fetch can ask for; Fetched counts those whose text is in the archive.
func (c TranscriptCall) Fetchable() (fetchable, fetched int) {
	for _, p := range c.Parts {
		if p.Fetchable {
			fetchable++
			if p.HasText() {
				fetched++
			}
		}
	}
	return fetchable, fetched
}

const partCols = `p.account_id, p.call_id, p.part_key, p.thread_id, p.message_id, p.ordinal, p.starts_at, p.duration_seconds, p.content_types,
  p.chunk_index, p.transcribe_only, p.host, p.site_root, p.storage_kind, p.drive_id, p.item_id, p.transcript_id, p.share_url, p.ref_quality,
  p.meeting_ical_uid, p.original_name, p.sent_at, f.state, f.fetched_at, f.attempted_at, f.http_status, f.entry_count, f.browser`

// TranscriptCalls lists the recorded calls the filter selects, newest first, each with its parts
// and what is fetched. truncated says the limit cut the list.
func (s *Store) TranscriptCalls(ctx context.Context, f TranscriptFilter) (calls []TranscriptCall, truncated bool, err error) {
	var w where
	if f.Account != nil {
		w.add(`p.account_id=?`, accountString(f.Account))
	}
	if len(f.Calls) > 0 {
		w.add(`p.call_id in `+inList(len(f.Calls)), stringArgs(f.Calls)...)
	}
	rows, err := s.query(ctx, `select `+partCols+` from transcript_parts p
	  left join transcript_fetches f on f.account_id=p.account_id and f.part_key=p.part_key`+w.sql()+` order by p.account_id, p.call_id, p.ordinal`, w.args...)
	if err != nil {
		return nil, false, err
	}
	all, err := scanParts(rows)
	if err != nil {
		return nil, false, err
	}
	for _, c := range all {
		if (f.State != "" && c.State != f.State) || (!f.Since.IsZero() && c.StartedAt.Before(f.Since)) || (!f.Until.IsZero() && !c.StartedAt.Before(f.Until)) {
			continue
		}
		calls = append(calls, c)
	}
	sort.SliceStable(calls, func(i, j int) bool {
		if !calls[i].StartedAt.Equal(calls[j].StartedAt) {
			return calls[i].StartedAt.After(calls[j].StartedAt)
		}
		return calls[i].CallID < calls[j].CallID
	})
	if f.Limit > 0 && len(calls) > f.Limit {
		calls, truncated = calls[:f.Limit], true
	}
	if err := s.nameCalls(ctx, calls); err != nil {
		return nil, false, err
	}
	return calls, truncated, nil
}

// scanParts groups the part rows, which come ordered by account, call and ordinal, into calls.
func scanParts(rows *sql.Rows) ([]TranscriptCall, error) {
	defer func() { _ = rows.Close() }()
	var out []TranscriptCall
	for rows.Next() {
		var p transcripts.Part
		var starts, sent, state, fetched, attempted, browser sql.NullString
		var status, entries sql.NullInt64
		if err := rows.Scan(&p.AccountID, &p.CallID, &p.PartKey, &p.ThreadID, &p.MessageID, &p.Ordinal, &starts, &p.DurationSeconds, &p.ContentTypes,
			&p.ChunkIndex, &p.TranscribeOnly, &p.Host, &p.SiteRoot, &p.StorageKind, &p.DriveID, &p.ItemID, &p.TranscriptID, &p.ShareURL, &p.RefQuality,
			&p.MeetingICalUID, &p.OriginalName, &sent, &state, &fetched, &attempted, &status, &entries, &browser); err != nil {
			return nil, err
		}
		p.StartsAt, p.SentAt = parseTime(starts), parseTime(sent)
		tp := TranscriptPart{Part: p, Fetchable: p.Fetchable()}
		if state.Valid {
			tp.Fetch = &FetchRow{State: state.String, FetchedAt: timePtr(fetched), AttemptedAt: timePtr(attempted),
				HTTPStatus: int(status.Int64), EntryCount: int(entries.Int64), Browser: browser.String}
		}
		tp.Reason = transcripts.Reason(p, tp.State())
		if n := len(out); n == 0 || out[n-1].AccountID != p.AccountID || out[n-1].CallID != p.CallID {
			out = append(out, TranscriptCall{AccountID: p.AccountID, CallID: p.CallID})
		}
		c := &out[len(out)-1]
		c.Parts = append(c.Parts, tp)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		for _, p := range c.Parts {
			if c.ThreadID == "" {
				c.ThreadID = p.ThreadID
			}
			// A call starts when its first part does; a call with no dated part, when its notice was sent.
			at := p.StartsAt
			if at.IsZero() {
				at = p.SentAt
			}
			if c.StartedAt.IsZero() || (!at.IsZero() && at.Before(c.StartedAt)) {
				c.StartedAt = at
			}
		}
		switch fetchable, fetched := c.Fetchable(); {
		case fetchable == 0:
			c.State = CallUnfetchable
		case fetched == fetchable:
			c.State = CallFetched
		case fetched == 0:
			c.State = CallNotFetched
		default:
			c.State = CallPartial
		}
	}
	return out, nil
}

func timePtr(s sql.NullString) *time.Time {
	t := parseTime(s)
	if t.IsZero() {
		return nil
	}
	return &t
}

// nameCalls fills each call's title from its meeting chat and its event key from the calendar
// occurrence its recording notices belong to. An archive without calendar tables gives no event.
func (s *Store) nameCalls(ctx context.Context, calls []TranscriptCall) error {
	if len(calls) == 0 {
		return nil
	}
	hasCalendar, err := s.hasCalendarTables(ctx)
	if err != nil {
		return err
	}
	var principals calendar.Principals
	if hasCalendar {
		if principals, err = calendar.LoadPrincipals(ctx, s.db); err != nil {
			return err
		}
	}
	rec := &recordingCache{s: s, principals: principals, byChat: map[string]*chatRecordings{}}
	titles := map[string]string{}
	for i := range calls {
		c := &calls[i]
		id := c.AccountID + "\x00" + c.ThreadID
		title, seen := titles[id]
		if !seen {
			if title, err = s.chatTitle(ctx, c.AccountID, c.ThreadID); err != nil {
				return err
			}
			titles[id] = title
		}
		c.Title = title
		if !hasCalendar {
			continue
		}
		cr, err := rec.of(ctx, principals.Of(c.AccountID), c.ThreadID)
		if err != nil {
			return err
		}
		c.EventKey = eventOfCall(cr, *c)
	}
	return nil
}

// eventOfCall is the key of the occurrence that holds one of the call's notices; the smallest key
// when its notices fell to more than one occurrence. Any notice of the call counts, not only the
// one its parts kept: Teams may re-post a notice outside the occurrence's window.
func eventOfCall(cr *chatRecordings, c TranscriptCall) string {
	best := ""
	for key, msgs := range cr.matched {
		for _, m := range msgs {
			if cr.calls[m.MessageID] == c.CallID && (best == "" || key < best) {
				best = key
			}
		}
	}
	return best
}

func (s *Store) chatTitle(ctx context.Context, account, thread string) (string, error) {
	tenant, user, _ := strings.Cut(account, "/")
	var title, display string
	err := s.db.QueryRowContext(ctx, `select title, display_name from conversations where tenant_id=? and user_id=? and id=?`, tenant, user, thread).Scan(&title, &display)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	}
	return firstNonEmpty(display, title), nil
}
