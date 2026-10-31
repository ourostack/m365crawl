package store

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

const (
	// EventMailLimit is how many messages EventMail returns when no limit is given.
	EventMailLimit = 20
	// eventMailBefore and eventMailAfter bound a subject match around the occurrence's start.
	eventMailBefore = 14 * 24 * time.Hour
	eventMailAfter  = 7 * 24 * time.Hour
)

// MailRelated is a message related to a calendar event. Match says how: "invite" (the message's
// ical_uid is the event's iCalUId) or "subject" (the same normalised subject, dated (DatedAt: sent
// for Sent Items, received otherwise) from 14 days before to 7 days after the occurrence's start, and, when the event names any attendee or
// organizer address, sent or received by at least one of them other than the mailbox owner; see
// eventParticipants).
type MailRelated struct {
	MailRow
	Match string
}

// EventMail lists the mail related to one occurrence: invite matches first, then subject matches,
// each newest first by DatedAt, at most limit (EventMailLimit when limit is 0 or less). Gone messages are left
// out. An archive without mail tables has no related mail.
func (s *Store) EventMail(ctx context.Context, ev calendar.Event, limit int) ([]MailRelated, error) {
	if limit <= 0 {
		limit = EventMailLimit
	}
	out := []MailRelated{}
	if ok, err := s.hasMail(ctx); err != nil || !ok {
		return out, err
	}
	// A copy that a newer folder set also holds is listed once, through it.
	copies, err := loadMailCopies(ctx, s.db)
	if err != nil {
		return nil, err
	}
	notCopy := ""
	if cond := copies.notCopy(`m.rowid`); cond != "" {
		notCopy = ` and ` + cond
	}
	// Each match is read through datedRows, newest first by the time mail list dates a message by
	// (mail_clock.go): a reply in Sent Items by when it was sent.
	dated := func(cond string, args []any, since, until time.Time, n int) ([]MailRow, error) {
		rows, rowArgs := copies.datedRows(cond+notCopy, args, since, until)
		return s.mailRowsMarked(ctx, copies, false, ` from (`+rows+` order by 2 desc, 1 desc limit ?) p join mail_messages m on m.rowid=p.rid`+mailJoin+` order by p.at desc, m.rowid desc`, append(rowArgs, n)) //nolint:gosec // G202: fragments are package constants and integers; values are placeholders
	}
	uid := strings.ToLower(ev.ICalUID)
	if uid != "" {
		rows, err := dated(`m.ical_uid=? and m.gone_at is null`, []any{uid}, time.Time{}, time.Time{}, limit)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, MailRelated{r, "invite"})
		}
	}
	norm := NormalizeSubject(ev.Subject)
	if norm == "" || ev.Start.IsZero() || len(out) >= limit {
		return out, nil
	}
	people, err := s.eventParticipants(ctx, ev)
	if err != nil {
		return nil, err
	}
	// The invite matches are already listed; the participant check runs here, before the limit, so
	// a match is never lost behind newer same-subject mail between other people.
	w := where{}
	w.add(`m.subject_norm=? and m.gone_at is null and (?='' or m.ical_uid<>?)`, norm, uid, uid)
	if len(people) > 0 {
		in := inList(len(people))
		args := stringArgs(people)
		w.add(`(lower(m.sender_address) in `+in+` or exists(select 1 from mail_recipients r where r.message_rowid=m.rowid and lower(r.address) in `+in+`))`, append(args, args...)...)
	}
	// The window includes its end; stored times have millisecond precision, so one millisecond
	// past it is the half-open bound datedRows takes.
	rows, err := dated(strings.Join(w.conds, " and "), w.args, ev.Start.Add(-eventMailBefore), ev.Start.Add(eventMailAfter+time.Millisecond), limit-len(out))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, MailRelated{r, "subject"})
	}
	return out, nil
}

// eventParticipants is the lower-case addresses of the event's attendees and organizer, sorted,
// without the mailbox owner's: the addresses signed in to any Outlook profile, which are in nearly
// every meeting and so tell nothing. When the owner is the only participant, the owner's
// addresses are kept.
func (s *Store) eventParticipants(ctx context.Context, ev calendar.Event) ([]string, error) {
	owner := map[string]bool{}
	err := mailEach(ctx, s.db, `select value from meta where key like 'outlook\_identity:%' escape '\'`, nil, func(r *sql.Rows) error {
		var v string
		if err := r.Scan(&v); err != nil {
			return err
		}
		for _, a := range strings.Split(v, "\n") {
			owner[a] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var all, others []string
	seen := map[string]bool{}
	for _, a := range append(parseAttendees(ev.AttendeesJSON), CalendarAttendee{Address: ev.OrganizerAddress}) {
		addr := strings.ToLower(strings.TrimSpace(a.Address))
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		all = append(all, addr)
		if !owner[addr] {
			others = append(others, addr)
		}
	}
	if len(others) == 0 {
		others = all
	}
	sort.Strings(others)
	return others, nil
}

// hasMail says the archive has the mail tables: an archive an older version wrote, opened
// read-only, has none until a sync migrates it.
func (s *Store) hasMail(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `select count(*)=1 from sqlite_master where type='table' and name='mail_messages'`).Scan(&ok)
	return ok, err
}
