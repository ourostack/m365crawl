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
// ical_uid is the event's iCalUId) or "subject" (the same normalised subject, received from 14 days
// before to 7 days after the occurrence's start, and, when the event names any attendee or
// organizer address, sent or received by at least one of them other than the mailbox owner; see
// eventParticipants).
type MailRelated struct {
	MailRow
	Match string
}

// EventMail lists the mail related to one occurrence: invite matches first, then subject matches,
// each newest first, at most limit (EventMailLimit when limit is 0 or less). Gone messages are left
// out. An archive without mail tables has no related mail.
func (s *Store) EventMail(ctx context.Context, ev calendar.Event, limit int) ([]MailRelated, error) {
	if limit <= 0 {
		limit = EventMailLimit
	}
	out := []MailRelated{}
	if ok, err := s.hasMail(ctx); err != nil || !ok {
		return out, err
	}
	uid := strings.ToLower(ev.ICalUID)
	if uid != "" {
		rows, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.ical_uid=? and m.gone_at is null order by m.received_at desc, m.rowid desc limit ?`, []any{uid, limit})
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
	w.add(`m.subject_norm=? and m.received_at>=? and m.received_at<=? and m.gone_at is null and (?='' or m.ical_uid<>?)`, norm, fmtTime(ev.Start.Add(-eventMailBefore)), fmtTime(ev.Start.Add(eventMailAfter)), uid, uid)
	if len(people) > 0 {
		in := inList(len(people))
		args := stringArgs(people)
		w.add(`(lower(m.sender_address) in `+in+` or exists(select 1 from mail_recipients r where r.message_rowid=m.rowid and lower(r.address) in `+in+`))`, append(args, args...)...)
	}
	rows, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+w.sql()+` order by m.received_at desc, m.rowid desc limit ?`, append(w.args, limit-len(out))) //nolint:gosec // G202: fragments are package constants; values are placeholders
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
