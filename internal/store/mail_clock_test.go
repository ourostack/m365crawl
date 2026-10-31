package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// timed sets a message's received and send times.
func timed(m outlookmail.Message, received, sent string) outlookmail.Message {
	m.Received, m.Sent = utc(received), utc(sent)
	return m
}

func listRows(t *testing.T, s *Store, f MailFilter) map[uint32]MailRow {
	t.Helper()
	rows, err := s.MailList(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uint32]MailRow{}
	for _, r := range rows {
		out[r.DetailKey] = r
	}
	return out
}

// Pacific local days as UTC instants: Aug 31 and Sep 1, 2026 (PDT, UTC-7).
var (
	aug31, sep1, sep2 = utc("2026-08-31T07:00:00Z"), utc("2026-09-01T07:00:00Z"), utc("2026-09-02T07:00:00Z")
)

// Sent Items is dated by when the message was sent: Outlook can store a received time on a sent
// message that is the next UTC day, or weeks before it was sent (when the draft was started).
// Every other folder is dated by when the message was received, so the two rows of a message
// sent to oneself each sit on their own folder's clock.
func TestMailSentItemsAreDatedBySentTime(t *testing.T) {
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(hr(0),
		// Sent at 16:05 on Aug 31 Pacific (23:05Z); its stored received time is Sep 1.
		timed(mailMsg(1, fSent, "<late@t>", "s", 0), "2026-09-01T17:19:31Z", "2026-08-31T23:05:56Z"),
		// Sent on Oct 8, but its stored received time is Sep 1, when the draft was started.
		timed(mailMsg(2, fSent, "<draft@t>", "s", 0), "2026-09-01T23:12:28Z", "2026-10-08T19:28:45Z"),
		// Sent to oneself at 23:59:30 Pacific on Aug 31; the Inbox copy arrives after midnight.
		timed(mailMsg(3, fSent, "<self@t>", "s", 0), "2026-09-01T06:59:30Z", "2026-09-01T06:59:30Z"),
		timed(mailMsg(4, fInbox, "<self@t>", "s", 0), "2026-09-01T07:00:20Z", "2026-09-01T06:59:30Z"),
		// Received mail: its send time is never the clock.
		timed(mailMsg(5, fInbox, "<in@t>", "s", 0), "2026-09-01T10:00:00Z", "2026-08-31T22:00:00Z"),
	))
	if got := listKeys(t, s, MailFilter{Since: aug31, Until: sep1}); !sameKeys(got, 3, 1) {
		t.Fatalf("Aug 31: %v, want the two messages sent that day, newest first", got)
	}
	if got := listKeys(t, s, MailFilter{Since: sep1, Until: sep2}); !sameKeys(got, 5, 4) {
		t.Fatalf("Sep 1: %v, want only what was received that day", got)
	}
	if got := listKeys(t, s, MailFilter{Folder: "sent", Since: sep1, Until: sep2}); len(got) != 0 {
		t.Fatalf("Sep 1 Sent Items: %v, want none (nothing was sent that day)", got)
	}
	if got := listKeys(t, s, MailFilter{Folder: "sent", Since: utc("2026-10-08T07:00:00Z"), Until: utc("2026-10-09T07:00:00Z")}); !sameKeys(got, 2) {
		t.Fatalf("Oct 8 Sent Items: %v", got)
	}
	if got := listKeys(t, s, MailFilter{}); !sameKeys(got, 2, 5, 4, 3, 1) {
		t.Fatalf("newest first by each folder's clock: %v", got)
	}
	rows := listRows(t, s, MailFilter{})
	if r := rows[1]; !r.DatedAt.Equal(utc("2026-08-31T23:05:56Z")) || !r.ReceivedAt.Equal(utc("2026-09-01T17:19:31Z")) || r.ReceivedAtDisagrees {
		t.Fatalf("a sent row is dated by sent_at and keeps its own received_at: %+v", r)
	}
	if r := rows[4]; !r.DatedAt.Equal(r.ReceivedAt) {
		t.Fatalf("an Inbox row is dated by received_at: %+v", r)
	}
}

// A sent message with no stored send time is dated by its received time, and Drafts (never sent)
// keep the received clock.
func TestMailSentWithoutSendTimeFallsBackToReceived(t *testing.T) {
	s := newStore(t)
	m := timed(mailMsg(1, fSent, "<x@t>", "s", 0), "2026-09-01T10:00:00Z", "2026-09-01T10:00:00Z")
	m.Sent = time.Time{}
	mustCommitMail(t, s, mailBatch(hr(0), m))
	if got := listKeys(t, s, MailFilter{Since: sep1, Until: sep2}); !sameKeys(got, 1) {
		t.Fatalf("%v", got)
	}
	if r := listRows(t, s, MailFilter{})[1]; !r.DatedAt.Equal(r.ReceivedAt) || !r.SentAt.IsZero() {
		t.Fatalf("%+v", r)
	}
	for kind, clock := range map[string]string{"sent": "sent_at", "drafts": "received_at", "inbox": "received_at", "to_me": "received_at", "deleted": "received_at", "other": "received_at", "unknown": "received_at"} {
		if got := MailClock(kind); got != clock {
			t.Fatalf("%s: %s, want %s", kind, got, clock)
		}
	}
}

// Copies of one message in different folder sets can disagree on received time by hours or weeks.
// The message is listed once, through the row that stands for it, and dated by the received time
// of the copy that agrees with when the message was sent: the earliest one at or after the send
// time (15 minutes of clock skew allowed). The majority of copies does not decide: a wrong time
// is often repeated. received_at_disagrees says the copies disagree; each row keeps its own
// received_at.
func TestMailCopiesThatDisagreeOnReceivedTime(t *testing.T) {
	s := newStore(t)
	sent := "2026-10-15T16:50:39Z"
	right, wrong := "2026-10-15T16:50:48Z", "2026-10-12T23:34:55Z"
	seedTwoSets(t, s,
		[]outlookmail.Message{
			timed(setMsg(1001, aInbox, "<moved@t>", 0), right, sent),
			timed(setMsg(1002, aInbox, "<none-plausible@t>", 0), "2026-10-01T00:00:00Z", sent),
			timed(setMsg(1003, aInbox, "<agree@t>", 0), "2026-10-15T16:50:48Z", sent),
			timed(setMsg(1004, aSent, "<sent@t>", 0), "2026-08-28T07:18:13Z", "2026-08-31T23:05:56Z"),
		},
		[]outlookmail.Message{
			// The current set holds the first message in Deleted Items, with a received time three
			// days before it was sent.
			timed(setMsg(2001, bDeleted, "<moved@t>", 0), wrong, sent),
			timed(setMsg(2002, bInbox, "<none-plausible@t>", 0), "2026-10-02T00:00:00Z", sent),
			timed(setMsg(2003, bInbox, "<agree@t>", 0), "2026-10-15T16:51:20Z", sent),
			timed(setMsg(2004, bSent, "<sent@t>", 0), "2026-09-01T17:19:31Z", "2026-08-31T23:05:56Z"),
		})
	oct15, oct16, oct12, oct13 := utc("2026-10-15T07:00:00Z"), utc("2026-10-16T07:00:00Z"), utc("2026-10-12T07:00:00Z"), utc("2026-10-13T07:00:00Z")
	if got := listKeys(t, s, MailFilter{Since: oct15, Until: oct16}); !sameKeys(got, 2003, 2001) {
		t.Fatalf("Oct 15: %v, want the moved message on the day it was received", got)
	}
	if got := listKeys(t, s, MailFilter{Since: oct12, Until: oct13}); len(got) != 0 {
		t.Fatalf("Oct 12: %v, want nothing (that received time is before the message was sent)", got)
	}
	rows := listRows(t, s, MailFilter{IncludeCopies: true, IncludeGone: true, IncludeEvicted: true})
	if r := rows[2001]; !r.DatedAt.Equal(utc(right)) || !r.ReceivedAt.Equal(utc(wrong)) || !r.ReceivedAtDisagrees {
		t.Fatalf("the standing row is dated by the copy that agrees with the send time: %+v", r)
	}
	if r := rows[1001]; !r.ReceivedAtDisagrees || r.CopyOf != mailAcct+":2001" || !r.DatedAt.Equal(utc(right)) {
		t.Fatalf("the copy is flagged too and keeps its own time: %+v", r)
	}
	// No copy is at or after the send time: the standing row's own time stays, flagged.
	if r := rows[2002]; !r.DatedAt.Equal(utc("2026-10-02T00:00:00Z")) || !r.ReceivedAtDisagrees {
		t.Fatalf("%+v", r)
	}
	// Copies within a minute of each other agree.
	if r := rows[2003]; r.ReceivedAtDisagrees || !r.DatedAt.Equal(r.ReceivedAt) {
		t.Fatalf("%+v", r)
	}
	// Sent Items copies days apart: dated by the send time they share, still flagged.
	if r := rows[2004]; !r.DatedAt.Equal(utc("2026-08-31T23:05:56Z")) || !r.ReceivedAtDisagrees {
		t.Fatalf("%+v", r)
	}
	if got := listKeys(t, s, MailFilter{Folder: "sent", Since: aug31, Until: sep1}); !sameKeys(got, 2004) {
		t.Fatalf("Aug 31 Sent Items: %v", got)
	}
	got, err := s.MailGet(context.Background(), mailAcct, 2001)
	if err != nil || !got.DatedAt.Equal(utc(right)) || !got.ReceivedAtDisagrees {
		t.Fatalf("mail show agrees with the list: %+v %v", got, err)
	}
}

// Two of three copies holding the same wrong received time do not outvote the one that agrees with
// the send time.
func TestMailCopiesSendTimeBeatsTheMajority(t *testing.T) {
	s := newStore(t)
	sent, right, wrong := "2026-10-08T19:28:45Z", "2026-10-08T19:28:51Z", "2026-09-01T23:12:28Z"
	cInbox := uint32(rootC + 1)
	mustCommitMail(t, s, setBatch(hr(1), rootA, timed(setMsg(1001, aInbox, "<m@t>", 0), wrong, sent)))
	commitUnmappedThenMapped(t, s, 2, rootC, timed(setMsg(3001, cInbox, "<m@t>", 0), right, sent))
	commitUnmappedThenMapped(t, s, 4, rootB, timed(setMsg(2001, bInbox, "<m@t>", 0), wrong, sent))
	r := listRows(t, s, MailFilter{})[2001]
	if !r.DatedAt.Equal(utc(right)) || !r.ReceivedAtDisagrees {
		t.Fatalf("%+v", r)
	}
	if got := listKeys(t, s, MailFilter{Since: utc("2026-10-08T07:00:00Z"), Until: utc("2026-10-09T07:00:00Z")}); !sameKeys(got, 2001) {
		t.Fatalf("%v", got)
	}
}

// The folder-copy check dates Sent Items by sent time, like the list: an older copy's message sent
// on the day counts even when its stored received time is another day, and one received on the
// day but sent on another does not. Whether it lies inside the current copy's range is judged by
// received time, the clock of that range.
func TestMailCopyCheckDatesSentItemsBySentTime(t *testing.T) {
	s := newStore(t)
	seedTwoSets(t, s,
		[]outlookmail.Message{
			timed(setMsg(1001, aSent, "<lost@t>", 0), "2026-09-01T17:19:31Z", "2026-08-31T23:05:56Z"),
			timed(setMsg(1002, aSent, "<other-day@t>", 0), "2026-08-31T20:00:00Z", "2026-10-08T19:28:45Z"),
		},
		[]outlookmail.Message{
			timed(setMsg(2001, bSent, "<anchor@t>", 0), "2026-08-20T10:00:00Z", "2026-08-20T10:00:00Z"),
			timed(setMsg(2002, bSent, "<kept@t>", 0), "2026-08-31T18:00:00Z", "2026-08-31T18:00:00Z"),
		})
	c := checkOf(t, s, "sent", aug31, sep1)
	if c.Messages != 2 || c.InCurrentCopy != 1 || c.LackingInCurrentRange != 1 || c.State != "partial" || c.DatedBy != "sent_at" {
		t.Fatalf("%+v", c)
	}
	if got := listKeys(t, s, MailFilter{Folder: "sent", Since: aug31, Until: sep1}); !sameKeys(got, 1001, 2002) {
		t.Fatalf("the list counts what the check counts: %v", got)
	}
	if c := checkOf(t, s, "inbox", aug31, sep1); c.DatedBy != "received_at" {
		t.Fatalf("%+v", c)
	}
}

// The check dates an Inbox message by the received time chosen across its copies, as the list does.
func TestMailCopyCheckUsesTheChosenReceivedTime(t *testing.T) {
	s := newStore(t)
	sent := "2026-10-15T16:50:39Z"
	seedTwoSets(t, s,
		[]outlookmail.Message{
			timed(setMsg(1001, aInbox, "<m@t>", 0), "2026-10-15T16:50:48Z", sent),
			timed(setMsg(1002, aInbox, "<n@t>", 0), "2026-10-15T16:50:48Z", sent),
			timed(setMsg(1003, aInbox, "<n2@t>", 0), "2026-10-15T16:50:48Z", sent),
		},
		[]outlookmail.Message{
			timed(setMsg(2001, bInbox, "<m@t>", 0), "2026-10-12T23:34:55Z", sent),
			timed(setMsg(2002, bInbox, "<n@t>", 0), "2026-10-15T16:50:48Z", sent),
			timed(setMsg(2010, bInbox, "<anchor@t>", 0), "2026-10-01T00:00:00Z", "2026-10-01T00:00:00Z"),
		})
	c := checkOf(t, s, "inbox", utc("2026-10-15T07:00:00Z"), utc("2026-10-16T07:00:00Z"))
	if c.Messages != 3 || c.InCurrentCopy != 2 || c.LackingInCurrentRange != 1 || c.State != "partial" {
		t.Fatalf("%+v", c)
	}
	if c := checkOf(t, s, "inbox", utc("2026-10-12T07:00:00Z"), utc("2026-10-13T07:00:00Z")); c.Messages != 0 {
		t.Fatalf("%+v", c)
	}
}

// A thread is oldest first by the time each message is dated by, as its table shows it: a Sent
// reply whose stored received time is the draft's start, before the message it answers, still
// comes after it and after what arrived before it was sent.
func TestMailThreadOrdersByDatedTime(t *testing.T) {
	s := newStore(t)
	parent := timed(mailMsg(1, fInbox, "<p@t>", "s", 0), "2026-09-01T10:00:00Z", "2026-09-01T09:59:00Z")
	between := timed(mailMsg(2, fInbox, "<b@t>", "s", 0), "2026-09-03T10:00:00Z", "2026-09-03T09:59:00Z")
	between.InReplyTo = "<p@t>"
	reply := timed(mailMsg(3, fSent, "<r@t>", "s", 0), "2026-09-01T09:00:00Z", "2026-10-08T19:28:45Z")
	reply.InReplyTo = "<p@t>"
	mustCommitMail(t, s, mailBatch(hr(0), parent, between, reply))
	rows, grouping, _, err := s.MailThread(context.Background(), mailAcct, 3)
	if err != nil || grouping != "reply_chain" {
		t.Fatalf("%v %v", grouping, err)
	}
	var got []uint32
	for _, r := range rows {
		got = append(got, r.DetailKey)
	}
	if !sameKeys(got, 1, 2, 3) {
		t.Fatalf("thread order %v", got)
	}
}

// queryPlan is the EXPLAIN QUERY PLAN detail lines of the statement MailList runs for f.
func queryPlan(t *testing.T, s *Store, f MailFilter) []string {
	t.Helper()
	ctx := context.Background()
	q, args, _, err := s.mailListQuery(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.QueryContext(ctx, `explain query plan select m.rowid`+q, args...) //nolint:gosec // G202: the statement MailList builds, values are placeholders
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The list reads received-time folders through the received-time index in order and stops at
// the limit, reads Sent Items through the folder index, and looks the messages whose received time
// was chosen across copies up by rowid: no read scans every message or matches every message
// against the chosen times.
func TestMailListUsesIndexes(t *testing.T) {
	s := newStore(t)
	sent := "2026-10-15T16:50:39Z"
	seedTwoSets(t, s,
		[]outlookmail.Message{timed(setMsg(1001, aInbox, "<m@t>", 0), "2026-10-15T16:50:48Z", sent), timed(setMsg(1002, aSent, "<s@t>", 0), sent, sent)},
		[]outlookmail.Message{timed(setMsg(2001, bInbox, "<m@t>", 0), "2026-10-12T23:34:55Z", sent), timed(setMsg(2002, bSent, "<s@t>", 0), sent, sent)})
	if got := listRows(t, s, MailFilter{})[2001]; !got.ReceivedAtDisagrees {
		t.Fatalf("the fixture needs a chosen received time: %+v", got)
	}
	for name, f := range map[string]MailFilter{
		"plain":   {},
		"window":  {Since: utc("2026-10-01T00:00:00Z"), Until: utc("2026-10-20T00:00:00Z")},
		"account": {Account: mailAcct, Since: utc("2026-10-01T00:00:00Z")},
	} {
		plan := queryPlan(t, s, f)
		joined := strings.Join(plan, "\n")
		// With --account, the account's prefix of mail_messages_folder or of the (account,
		// detail_key) key reads the same rows; either is a search, not a scan.
		if !strings.Contains(joined, "USING INDEX mail_messages_received") && !strings.Contains(joined, "USING INDEX mail_messages_folder") &&
			(f.Account == "" || !strings.Contains(joined, "USING INDEX sqlite_autoindex_mail_messages_1 (account=?)")) {
			t.Fatalf("%s: no index on received time:\n%s", name, joined)
		}
		if !strings.Contains(joined, "INTEGER PRIMARY KEY") {
			t.Fatalf("%s: chosen times are not looked up by rowid:\n%s", name, joined)
		}
		for _, d := range plan {
			if d == "SCAN m" || d == "SCAN mail_messages" || strings.HasPrefix(d, "SCAN m ") && !strings.Contains(d, "INDEX") {
				t.Fatalf("%s: a full scan of the messages:\n%s", name, joined)
			}
		}
	}
	if plan := strings.Join(queryPlan(t, s, MailFilter{}), "\n"); !strings.Contains(plan, "USING INDEX mail_messages_received") || !strings.Contains(plan, "MERGE") {
		t.Fatalf("the plain list reads received time in index order and merges the parts:\n%s", plan)
	}
}

// BenchmarkMailListChosenTimes lists a month from an archive where every message's copies disagree
// on received time, so each has a chosen time; the time per list should grow about linearly with
// the number of chosen times, not with their square.
func BenchmarkMailListChosenTimes(b *testing.B) {
	for _, n := range []int{25, 500, 4000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := newStore(b)
			var a, cur []outlookmail.Message
			for i := range n {
				id := fmt.Sprintf("<m%d@t>", i)
				sent := utc("2026-10-15T16:50:39Z").Add(-time.Duration(i) * time.Minute)
				ma := setMsg(uint32(10000+i), aInbox, id, 0)
				ma.Received, ma.Sent = sent.Add(9*time.Second), sent
				mb := setMsg(uint32(50000+i), bInbox, id, 0)
				mb.Received, mb.Sent = sent.Add(-72*time.Hour), sent
				a, cur = append(a, ma), append(cur, mb)
			}
			seedTwoSets(b, s, a, cur)
			f := MailFilter{Since: utc("2026-10-01T00:00:00Z"), Until: utc("2026-11-01T00:00:00Z"), Limit: 50}
			b.ResetTimer()
			for range b.N {
				if _, err := s.MailList(context.Background(), f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Without any Sent Items folder, the list still dates a message by its chosen received time.
func TestMailChosenTimeWithoutSentItems(t *testing.T) {
	s := newStore(t)
	sent := "2026-10-15T16:50:39Z"
	seedTwoSets(t, s,
		[]outlookmail.Message{timed(setMsg(1001, aInbox, "<m@t>", 0), "2026-10-15T16:50:48Z", sent)},
		[]outlookmail.Message{timed(setMsg(2001, bInbox, "<m@t>", 0), "2026-10-12T23:34:55Z", sent)})
	if _, err := s.db.Exec(`update mail_folders set kind='other' where kind='sent'`); err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, s, MailFilter{Since: utc("2026-10-15T07:00:00Z"), Until: utc("2026-10-16T07:00:00Z")}); !sameKeys(got, 2001) {
		t.Fatalf("%v", got)
	}
	if got := listKeys(t, s, MailFilter{Until: utc("2026-10-13T07:00:00Z")}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
