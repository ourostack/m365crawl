package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// prepEvent is a meeting at mailT0 with Ann (the usual sender of mailMsg) invited.
func prepEvent() calendar.Event {
	return calendar.Event{
		ICalUID: "040000008200E00074C5B7101A82E008ABCDEF", Subject: "Budget review", Start: mailT0, End: mailT0.Add(time.Hour),
		OrganizerAddress: "Org@Example.test",
		AttendeesJSON:    `[{"name":"Ann Sender","address":"ANN@example.test"},{"name":"Room","address":""}]`,
	}
}

// at moves a message's received time to t.
func receivedAt(m outlookmail.Message, t time.Time) outlookmail.Message {
	m.Received, m.Sent = t, t.Add(-time.Minute)
	return m
}

func relatedKeys(rows []MailRelated) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Subject+"/"+r.Match)
	}
	return strings.Join(out, ", ")
}

func TestEventMailMatchesTheInviteAndTheSubject(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	invite := mailMsg(1, fInbox, "<i1@x>", "Budget review", 3)
	invite.ICalUID = "040000008200e00074c5b7101a82e008abcdef" // stored lower-case; the event's id is upper-case
	reply := receivedAt(mailMsg(2, fInbox, "<r1@x>", "RE: Budget review", 0), mailT0.Add(-2*24*time.Hour))
	late := receivedAt(mailMsg(3, fInbox, "<r2@x>", "Fw: budget REVIEW", 0), mailT0.Add(6*24*time.Hour))
	tooEarly := receivedAt(mailMsg(4, fInbox, "<r3@x>", "Budget review", 0), mailT0.Add(-15*24*time.Hour))
	tooLate := receivedAt(mailMsg(5, fInbox, "<r4@x>", "Budget review", 0), mailT0.Add(8*24*time.Hour))
	stranger := receivedAt(mailMsg(6, fInbox, "<r5@x>", "Budget review", 0), mailT0.Add(-time.Hour))
	stranger.SenderAddress, stranger.Recipients = "zed@example.test", []outlookmail.Recipient{{Name: "Yan", Address: "yan@example.test"}}
	viaRecipient := receivedAt(mailMsg(7, fSent, "<r6@x>", "Budget review", 0), mailT0.Add(-3*time.Hour))
	viaRecipient.SenderAddress, viaRecipient.Recipients = "me@example.test", []outlookmail.Recipient{{Name: "Org", Address: "org@EXAMPLE.test"}}
	other := receivedAt(mailMsg(8, fInbox, "<r7@x>", "Lunch", 0), mailT0)
	gone := receivedAt(mailMsg(9, fInbox, "<r8@x>", "Budget review", 0), mailT0.Add(-4*time.Hour))
	mustCommitMail(t, s, mailBatch(mailT0, invite, reply, late, tooEarly, tooLate, stranger, viaRecipient, other, gone))
	if _, err := s.db.ExecContext(ctx, `update mail_messages set gone_at=? where detail_key=9`, fmtTime(mailT0)); err != nil {
		t.Fatal(err)
	}
	got, err := s.EventMail(ctx, prepEvent(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// The invite first, then subject matches newest first; outside -14d..+7d, without a shared
	// participant, gone, or with another subject → left out. The invite itself is not repeated.
	want := "Budget review/invite, Fw: budget REVIEW/subject, Budget review/subject, RE: Budget review/subject"
	if relatedKeys(got) != want {
		t.Fatalf("got  %s\nwant %s", relatedKeys(got), want)
	}
	if got[0].Recipients == nil || got[0].ID() != mailAcct+":1" {
		t.Fatalf("a related message is a full mail row: %+v", got[0].MailRow)
	}
	// The limit cuts the list.
	if got, _ := s.EventMail(ctx, prepEvent(), 2); relatedKeys(got) != "Budget review/invite, Fw: budget REVIEW/subject" {
		t.Fatalf("limit 2: %s", relatedKeys(got))
	}
	// An event with no participant addresses needs none shared.
	open := prepEvent()
	open.AttendeesJSON, open.OrganizerAddress = "", ""
	open.ICalUID = ""
	if got, _ := s.EventMail(ctx, open, 0); relatedKeys(got) != "Fw: budget REVIEW/subject, Budget review/subject, Budget review/subject, RE: Budget review/subject, Budget review/subject" {
		t.Fatalf("no participants: %s", relatedKeys(got))
	}
}

// Each occurrence of a series looks only around its own start.
func TestEventMailStaysInsideTheOccurrenceWindow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	week1 := receivedAt(mailMsg(1, fInbox, "<a@x>", "Weekly sync", 0), mailT0.Add(-24*time.Hour))
	week5 := receivedAt(mailMsg(2, fInbox, "<b@x>", "RE: Weekly sync", 0), mailT0.Add(27*24*time.Hour))
	mustCommitMail(t, s, mailBatch(mailT0, week1, week5))
	occ := calendar.Event{Subject: "Weekly sync", Start: mailT0}
	got := must(s.EventMail(ctx, occ, 0))
	if relatedKeys(got) != "Weekly sync/subject" {
		t.Fatalf("first occurrence: %s", relatedKeys(got))
	}
	occ.Start = mailT0.Add(28 * 24 * time.Hour)
	if got := must(s.EventMail(ctx, occ, 0)); relatedKeys(got) != "RE: Weekly sync/subject" {
		t.Fatalf("fifth occurrence: %s", relatedKeys(got))
	}
}

func TestEventMailWithNothingToMatch(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x>", "Budget review", 0)))
	// No iCalUId and a subject that normalises to nothing; then a subject with no start.
	for _, ev := range []calendar.Event{{Subject: "RE: ", Start: mailT0}, {Subject: "Budget review"}} {
		got, err := s.EventMail(ctx, ev, 0)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("%+v: %v %v", ev, got, err)
		}
	}
	// An invite id no message carries.
	if got := must(s.EventMail(ctx, calendar.Event{ICalUID: "nope"}, 0)); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestEventMailOnAnArchiveWithoutMail(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.ExecContext(ctx, `drop table mail_messages`); err != nil {
		t.Fatal(err)
	}
	got, err := s.EventMail(ctx, prepEvent(), 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
}

func TestEventMailReportsQueryErrors(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	invite := mailMsg(1, fInbox, "<a@x>", "Budget review", 0)
	invite.ICalUID = prepEvent().ICalUID
	mustCommitMail(t, s, mailBatch(mailT0, invite))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.EventMail(cctx, prepEvent(), 0); err == nil {
		t.Fatal("a cancelled read succeeded")
	}
	// The subject query fails after the invite query worked.
	if _, err := s.db.ExecContext(ctx, `drop table mail_recipients`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EventMail(ctx, calendar.Event{Subject: "Budget review", Start: mailT0}, 0); err == nil {
		t.Fatal("a broken archive read succeeded")
	}
	if _, err := s.EventMail(ctx, prepEvent(), 0); err == nil {
		t.Fatal("a broken archive read succeeded")
	}
}

// The meeting chat carries its newest live messages, newest first, at most chatRecentShown.
func TestCalendarEventChatHasRecentMessages(t *testing.T) {
	s := qArchive(t)
	ctx := context.Background()
	d, err := s.CalendarEvent(ctx, nil, EventID(qTeams, calendar.Key(qEvent("o1", 3))))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range d.Chat.Recent {
		got = append(got, m.ID)
	}
	// m-del is deleted; the rest newest first.
	if strings.Join(got, ",") != "m-late,m-rec2,m-tr,m-rec,m-other,m-call" || d.Chat.Recent[0].ConversationDisplayName != "Q meeting" {
		t.Fatalf("recent %v", got)
	}
	for i := 0; i < chatRecentShown; i++ {
		s.qMessage(t, "m-more-"+strings.Repeat("x", i+1), "RichText/Html", time.Date(2026, 12, 1, 0, i, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000Z"), "more")
	}
	d, err = s.CalendarEvent(ctx, nil, EventID(qTeams, calendar.Key(qEvent("o1", 3))))
	if err != nil || len(d.Chat.Recent) != chatRecentShown || d.Chat.Recent[0].ID != "m-more-"+strings.Repeat("x", chatRecentShown) {
		t.Fatalf("%d %v", len(d.Chat.Recent), err)
	}
}

func TestCalendarEventReportsAChatReadError(t *testing.T) {
	s := qArchive(t)
	s.qExec(t, `drop table activity`) // only the message columns of the recent messages read it
	if _, err := s.CalendarEvent(context.Background(), nil, EventID(qTeams, calendar.Key(qEvent("o1", 3)))); err == nil || !strings.Contains(err.Error(), "activity") {
		t.Fatalf("%v", err)
	}
}

// A match older than many newer same-subject messages between other people is still found: the
// participant check runs in the query, before the limit.
func TestEventMailFindsAMatchBehindManyStrangers(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	msgs := []outlookmail.Message{receivedAt(mailMsg(1, fInbox, "<ann@x>", "Standup", 0), mailT0.Add(-3*24*time.Hour))}
	for i := 0; i < 205; i++ {
		m := receivedAt(mailMsg(uint32(100+i), fInbox, fmt.Sprintf("<s%d@x>", i), "Standup", 0), mailT0.Add(-time.Duration(i+1)*time.Minute))
		m.SenderAddress, m.Recipients = "zed@example.test", []outlookmail.Recipient{{Name: "Yan", Address: "yan@example.test"}}
		msgs = append(msgs, m)
	}
	mustCommitMail(t, s, mailBatch(mailT0, msgs...))
	ev := calendar.Event{Subject: "Standup", Start: mailT0, AttendeesJSON: `[{"address":"ann@example.test"}]`}
	got := must(s.EventMail(ctx, ev, 0))
	if len(got) != 1 || got[0].DetailKey != 1 {
		t.Fatalf("%s", relatedKeys(got))
	}
}

// The mailbox owner is in nearly every meeting, so their own address does not make a message
// related; an event whose only participant is the owner keeps the owner's address.
func TestEventMailLeavesTheOwnerOutOfTheOverlap(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.ExecContext(ctx, `insert into meta(key, value) values(?, ?)`, outlookIdentityKey+mailAcct, "me@example.test\nme2@example.test"); err != nil {
		t.Fatal(err)
	}
	fromAnn := receivedAt(mailMsg(1, fInbox, "<a@x>", "Standup", 0), mailT0.Add(-time.Hour))
	other := receivedAt(mailMsg(2, fInbox, "<b@x>", "Standup", 0), mailT0.Add(-2*time.Hour))
	other.SenderAddress, other.Recipients = "zed@example.test", []outlookmail.Recipient{{Name: "Me", Address: "ME@example.test"}}
	mustCommitMail(t, s, mailBatch(mailT0, fromAnn, other))
	ev := calendar.Event{Subject: "Standup", Start: mailT0, OrganizerAddress: "Me@Example.test", AttendeesJSON: `[{"address":"ann@example.test"}]`}
	if got := must(s.EventMail(ctx, ev, 0)); len(got) != 1 || got[0].DetailKey != 1 {
		t.Fatalf("with others: %s", relatedKeys(got))
	}
	ev.AttendeesJSON = `[{"address":"me2@example.test"}]`
	if got := must(s.EventMail(ctx, ev, 0)); len(got) != 1 || got[0].DetailKey != 2 {
		t.Fatalf("owner only: %s", relatedKeys(got))
	}
	// An unreadable identity row, then no meta table at all, fail the read.
	for _, q := range []string{
		`create table meta2 as select * from meta`, `drop table meta`, `create table meta(key text primary key, value text)`,
		`insert into meta select * from meta2`, `insert into meta(key, value) values('outlook_identity:outlook/x', null)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EventMail(ctx, ev, 0); err == nil {
		t.Fatal("a NULL identity was read")
	}
	if _, err := s.db.ExecContext(ctx, `drop table meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EventMail(ctx, ev, 0); err == nil {
		t.Fatal("a read without meta succeeded")
	}
}
