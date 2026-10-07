package store

import (
	"context"
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
// organizer address, sent or received by at least one of them).
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
	seen := map[int64]bool{}
	if uid := strings.ToLower(ev.ICalUID); uid != "" {
		rows, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.ical_uid=? and m.gone_at is null order by m.received_at desc, m.rowid desc limit ?`, []any{uid, limit})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			seen[r.Rowid] = true
			out = append(out, MailRelated{r, "invite"})
		}
	}
	norm := NormalizeSubject(ev.Subject)
	if norm == "" || ev.Start.IsZero() || len(out) >= limit {
		return out, nil
	}
	rows, err := s.mailRows(ctx, false, ` from mail_messages m`+mailJoin+` where m.subject_norm=? and m.received_at>=? and m.received_at<=? and m.gone_at is null order by m.received_at desc, m.rowid desc limit ?`,
		[]any{norm, fmtTime(ev.Start.Add(-eventMailBefore)), fmtTime(ev.Start.Add(eventMailAfter)), mailThreadCap})
	if err != nil {
		return nil, err
	}
	people := eventAddresses(ev)
	for _, r := range rows {
		if len(out) >= limit {
			break
		}
		if seen[r.Rowid] || !sharesAddress(people, r) {
			continue
		}
		out = append(out, MailRelated{r, "subject"})
	}
	return out, nil
}

// eventAddresses is the lower-case addresses of the event's attendees and organizer.
func eventAddresses(ev calendar.Event) map[string]bool {
	set := map[string]bool{}
	for _, a := range append(parseAttendees(ev.AttendeesJSON), CalendarAttendee{Address: ev.OrganizerAddress}) {
		if addr := strings.ToLower(strings.TrimSpace(a.Address)); addr != "" {
			set[addr] = true
		}
	}
	return set
}

// sharesAddress says a message was sent or received by one of people; an empty set lets every
// message through.
func sharesAddress(people map[string]bool, m MailRow) bool {
	if len(people) == 0 {
		return true
	}
	for a := range addressSet(m) {
		if people[a] {
			return true
		}
	}
	return false
}

// hasMail says the archive has the mail tables: an archive an older version wrote, opened
// read-only, has none until a sync migrates it.
func (s *Store) hasMail(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRowContext(ctx, `select count(*)=1 from sqlite_master where type='table' and name='mail_messages'`).Scan(&ok)
	return ok, err
}
