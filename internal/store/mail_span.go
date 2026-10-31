package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// The mail roles whose coverage is a range the cache holds: Sent Items, and Inbox with To Me.
// Drafts, Archive, Deleted Items and custom folders hold only part of their mail locally, so no
// range of them is ever called complete.
const (
	MailRoleSent  = "sent"
	MailRoleInbox = "inbox"
)

// mailSpanKey is the meta key prefix of an account's covered mail spans: one key per account
// (outlook_mail_span:<account>), so every scan that reads the account after the first colon, as
// calendar sources does for outlook_mail_fresh, still finds the real account.
const mailSpanKey = "outlook_mail_span:"

// TimeSpan is the span of time [From, To).
type TimeSpan struct{ From, To time.Time }

// MergeSpans sorts spans and joins the ones that overlap or touch. Empty spans are dropped.
func MergeSpans(spans []TimeSpan) []TimeSpan {
	var in []TimeSpan
	for _, s := range spans {
		if s.To.After(s.From) {
			in = append(in, s)
		}
	}
	slices.SortFunc(in, func(a, b TimeSpan) int { return a.From.Compare(b.From) })
	var out []TimeSpan
	for _, s := range in {
		if n := len(out); n > 0 && !s.From.After(out[n-1].To) {
			if s.To.After(out[n-1].To) {
				out[n-1].To = s.To
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// mailSpanJSON is one stored span, in the archive's time layout.
type mailSpanJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// mailSpanRecord is the stored value of an account's spans, by role.
type mailSpanRecord struct {
	Sent  []mailSpanJSON `json:"sent"`
	Inbox []mailSpanJSON `json:"inbox"`
}

func spansToJSON(spans []TimeSpan) []mailSpanJSON {
	out := make([]mailSpanJSON, len(spans))
	for i, s := range spans {
		out[i] = mailSpanJSON{s.From.UTC().Format(timeLayout), s.To.UTC().Format(timeLayout)}
	}
	return out
}

// spansFromJSON reads stored spans; a span with a time that does not parse is left out, so a
// damaged value can only cover less, never more.
func spansFromJSON(in []mailSpanJSON) []TimeSpan {
	var out []TimeSpan
	for _, s := range in {
		from := parseTime(sql.NullString{String: s.From, Valid: true})
		to := parseTime(sql.NullString{String: s.To, Valid: true})
		if !from.IsZero() && !to.IsZero() {
			out = append(out, TimeSpan{from, to})
		}
	}
	return MergeSpans(out)
}

// decodeMailSpans reads a stored value; one that is not JSON holds no span.
func decodeMailSpans(value string) map[string][]TimeSpan {
	var rec mailSpanRecord
	if json.Unmarshal([]byte(value), &rec) != nil {
		return map[string][]TimeSpan{}
	}
	return map[string][]TimeSpan{MailRoleSent: spansFromJSON(rec.Sent), MailRoleInbox: spansFromJSON(rec.Inbox)}
}

// rowQuerier is a database or a transaction.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// mailSpanValue reads an account's stored spans and whether any are stored; a test seam. Neither
// column is nullable, so a row that comes back empty is an error, not "none stored".
var mailSpanValue = func(ctx context.Context, q rowQuerier, account string) (value string, stored bool, err error) {
	err = q.QueryRowContext(ctx, `select exists(select 1 from meta where key=?1), coalesce((select value from meta where key=?1),'')`, mailSpanKey+account).Scan(&stored, &value)
	return value, stored, err
}

// mailCoverageComplete says whether a committed read may add covered spans: it lost no message
// header (only attachment, folder and recipient objects with an unknown tag, whose effect on a
// message shows up as a missing folder), it is not doubtful, its snapshot was stable, no message
// was kept without a folder and no absence was withheld. A missing detail object blocks only the
// roles of the folders its header copies name (mailRoleBlocked).
func mailCoverageComplete(b MailBatch, withheld int) bool {
	if b.Result.Doubtful || b.Unstable || withheld > 0 || b.Result.Notes.MissingFolder > 0 || b.FreshAt.IsZero() {
		return false
	}
	for _, l := range b.Result.Losses {
		if l.Code != outlookmail.CodeMailLayoutPartial {
			return false
		}
	}
	return true
}

// mailRoleBlocked says whether a message of the role may be missing from the read: a header
// whose detail object is missing names a folder of the role, or a folder the read does not hold.
func mailRoleBlocked(b MailBatch, role string) bool {
	kinds := map[uint32]string{}
	for _, f := range b.Result.Folders {
		kinds[f.Key] = f.Kind
	}
	for key := range b.Result.MissingDetailFolders {
		kind, known := kinds[key]
		if !known || mailCopyRole(kind) == role {
			return true
		}
	}
	return false
}

// mailReadSpan is the span of the role a complete read covers. It starts at the oldest message
// of the role's anchor folders (Inbox for inbox: To Me holds copies of Inbox mail and may reach
// less far back; Sent Items for sent), the latest such start when there are several, so every
// anchor folder reaches back that far. It ends at the store's write time, or earlier at the
// newest message the read holds outside Drafts: Outlook keeps writing its store while offline, so
// only a message that arrived proves it had synced up to then. Sent Items count by sent time,
// because their received time is often when a draft was started.
func mailReadSpan(b MailBatch, role string) (TimeSpan, bool) {
	kinds := map[uint32]string{}
	for _, f := range b.Result.Folders {
		kinds[f.Key] = f.Kind
	}
	var from time.Time
	for _, c := range b.Result.Coverage {
		if kinds[c.FolderKey] == role && !c.Oldest.IsZero() && c.Oldest.After(from) {
			from = c.Oldest
		}
	}
	var newest time.Time
	for _, m := range b.Result.Messages {
		at := m.Received
		switch m.Folder.Kind {
		case "drafts", outlookmail.KindUnknown:
			continue
		case MailRoleSent:
			at = m.Sent
		}
		if at.After(newest) {
			newest = at
		}
	}
	to := b.FreshAt
	if newest.Before(to) {
		to = newest
	}
	return TimeSpan{from, to}, !from.IsZero() && to.After(from)
}

// recordMailSpans adds the spans a complete read covers to the account's stored spans. Any read
// with a store write time records the key, with or without a new span, so the archive knows a
// sync of this version has judged the account and no longer needs the last read's ranges.
func recordMailSpans(ctx context.Context, tx *sql.Tx, b MailBatch, withheld int) error {
	if b.FreshAt.IsZero() {
		return nil
	}
	value, _, err := mailSpanValue(ctx, tx, b.Account)
	if err != nil {
		return err
	}
	spans := decodeMailSpans(value)
	if mailCoverageComplete(b, withheld) {
		for _, role := range []string{MailRoleSent, MailRoleInbox} {
			if span, ok := mailReadSpan(b, role); ok && !mailRoleBlocked(b, role) {
				spans[role] = MergeSpans(append(spans[role], span))
			}
		}
	}
	record, _ := json.Marshal(mailSpanRecord{Sent: spansToJSON(spans[MailRoleSent]), Inbox: spansToJSON(spans[MailRoleInbox])}) // strings only
	_, err = tx.ExecContext(ctx, `insert into meta(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, mailSpanKey+b.Account, string(record))
	return err
}

// MailSpans is what the archive can say about when the cache of one account held every message
// of a role.
type MailSpans struct {
	// Recorded says a sync of this version judged the account, so Spans are the recorded ones.
	// Otherwise Spans come from the last read's ranges (Legacy), or are empty when that read does
	// not qualify.
	Recorded, Legacy bool
	Spans            map[string][]TimeSpan
	// SentShift is how much later than its received-time range a Sent span starts: the largest
	// delay between a Sent message's received time (often when its draft was started) and its
	// send time, so a message drafted before the cache's reach and sent after it is not assumed.
	SentShift time.Duration
	// FreshAt is the store write time behind the last read (zero when unknown).
	FreshAt time.Time
}

// MailCoverageSpans reads the account's covered spans. Without recorded ones it falls back to the
// last read's ranges when that read could have recorded them: its sync run was ok (or unchanged,
// or had only unknown attachment, folder and recipient tags) and no failure is kept. The fallback
// cannot see a missing detail object; SPEC.md says so.
func (s *Store) MailCoverageSpans(ctx context.Context, account string) (MailSpans, error) {
	out := MailSpans{Spans: map[string][]TimeSpan{}}
	meta, sources, err := s.outlookCacheTables(ctx)
	if err != nil || !meta {
		return out, err
	}
	if out.FreshAt, err = s.mailFreshAt(ctx, account, meta, sources); err != nil {
		return out, err
	}
	if out.SentShift, err = s.mailSentShift(ctx, account); err != nil {
		return out, err
	}
	value, stored, err := mailSpanValue(ctx, s.db, account)
	if err != nil {
		return out, err
	}
	if stored {
		out.Recorded, out.Spans = true, decodeMailSpans(value)
		return out, nil
	}
	ok, err := s.mailLegacyQualifies(ctx, account)
	if err != nil || !ok {
		return out, err
	}
	out.Legacy = true
	return out, s.mailLegacySpans(ctx, account, &out)
}

// mailSentShift is the largest delay, never negative, between an archived Sent message's
// received time and its send time.
func (s *Store) mailSentShift(ctx context.Context, account string) (time.Duration, error) {
	var seconds sql.NullFloat64
	err := s.db.QueryRowContext(ctx, `select max((julianday(m.sent_at)-julianday(m.received_at))*86400) from mail_messages m
join mail_folders f on f.account=m.account and f.folder_key=m.folder_key where m.account=? and f.kind='sent'`, account).Scan(&seconds)
	if err != nil || !seconds.Valid || seconds.Float64 <= 0 {
		return 0, err
	}
	return time.Duration(seconds.Float64 * float64(time.Second)).Round(time.Millisecond), nil
}

// mailLegacyQualifies says whether the last read of the account's mail (the latest sync run that
// read it) could have recorded spans, judged by what the archive kept of it.
func (s *Store) mailLegacyQualifies(ctx context.Context, account string) (bool, error) {
	failures, err := s.MailFailures(ctx, account)
	if err != nil || len(failures) > 0 {
		return false, err
	}
	key := "outlook|" + strings.TrimPrefix(account, "outlook/") + "|mail"
	var status string
	var omissions sql.NullString
	err = s.db.QueryRowContext(ctx, `select status, omissions_json from sync_runs where source=? and status not in ('skipped_interval','unchanged') order by id desc limit 1`, key).Scan(&status, &omissions)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	switch status {
	case "ok":
		return true, nil
	case "ok_with_omissions":
		var codes map[string]int
		if json.Unmarshal([]byte(omissions.String), &codes) != nil {
			return false, nil
		}
		for code := range codes {
			if code != outlookmail.CodeMailLayoutPartial {
				return false, nil
			}
		}
		return true, nil
	}
	return false, nil
}

// mailLegacySpans builds each role's span from the last read's ranges of the account's current
// folders, as mailReadSpan builds it from a read.
func (s *Store) mailLegacySpans(ctx context.Context, account string, out *MailSpans) error {
	rows, err := s.MailCoverage(ctx)
	if err != nil {
		return err
	}
	from := map[string]time.Time{}
	var newest time.Time
	for _, c := range rows {
		if c.Account != account || !c.Current {
			continue
		}
		if (c.Kind == MailRoleSent || c.Kind == MailRoleInbox) && c.OldestAt.After(from[c.Kind]) {
			from[c.Kind] = c.OldestAt
		}
		// A Sent row's newest received time can be a draft's start or a later guess, so only
		// other folders prove how far Outlook had synced.
		if c.Kind != MailRoleSent && c.Kind != "drafts" && c.Kind != outlookmail.KindUnknown && c.NewestAt.After(newest) {
			newest = c.NewestAt
		}
	}
	to := out.FreshAt
	if to.IsZero() || newest.Before(to) {
		to = newest
	}
	for role, start := range from {
		out.Spans[role] = MergeSpans([]TimeSpan{{start, to}})
	}
	return nil
}

// MailServerThrough is the newest source time of a recorded whole-account, all-folder, gap-free
// WorkIQ mail sync of the account (the rule of WorkIQMailComplete): the server copy it retained
// holds the account's mail up to then. Zero when there is none.
func (s *Store) MailServerThrough(ctx context.Context, account string) (time.Time, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return time.Time{}, err
	}
	var out time.Time
	err = s.each(ctx, `select r.source_data_at from workiq_mail_receipts r join workiq_attempts a on a.id=r.attempt_id
where r.account_id=? and a.account_id=r.account_id and a.family='mail' and a.kind='sync'
and a.status='ok' and a.all_folders=1 and a.gap_count=0 and r.enumeration_key<>''`, func(rs *sql.Rows) error {
		// A source time that cannot be read extends nothing.
		var at sql.NullString
		if rs.Scan(&at) == nil {
			if t, err := workIQStoredTime(at); err == nil && t != nil && t.After(out) {
				out = *t
			}
		}
		return nil
	}, account)
	return out, err
}
