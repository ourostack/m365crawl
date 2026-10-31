package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func span(from, to time.Time) TimeSpan { return TimeSpan{from, to} }

func TestMergeSpansJoinsOverlapsAndTouchesAndKeepsHoles(t *testing.T) {
	d := func(n int) time.Time { return mailT0.AddDate(0, 0, n) }
	got := MergeSpans([]TimeSpan{span(d(5), d(7)), span(d(0), d(2)), span(d(2), d(3)), span(d(1), d(2)), span(d(9), d(9)), span(d(6), d(8)), span(d(4), d(3))})
	want := []TimeSpan{span(d(0), d(3)), span(d(5), d(8))}
	if !slices.Equal(got, want) {
		t.Fatalf("merged %v, want %v", got, want)
	}
	if MergeSpans(nil) != nil {
		t.Fatal("no spans merged into some")
	}
}

func spansOf(t *testing.T, s *Store, account string) MailSpans {
	t.Helper()
	return must(s.MailCoverageSpans(context.Background(), account))
}

// sentMsg is a Sent Items message received (its draft started) draftDays before it was sent.
func sentMsg(dk uint32, msgID string, sentDaysAgo, draftDays int) outlookmail.Message {
	m := mailMsg(dk, fSent, msgID, "sent "+msgID, sentDaysAgo)
	m.Sent = m.Received
	m.Received = m.Sent.AddDate(0, 0, -draftDays)
	return m
}

// A complete read records, per role, the span from the oldest message of the role's anchor folder
// to the newest message outside Drafts (or the store's write time when that is earlier), and a
// later read whose cache reaches less far back never shrinks it.
func TestCommitMailRecordsSpansThatNeverShrink(t *testing.T) {
	s := newStore(t)
	inbox := mailMsg(1, fInbox, "<a@x.test>", "a", 20)
	toMe := mailMsg(2, fToMe, "<b@x.test>", "b", 30) // To Me reaching further back does not move Inbox's start
	sent := sentMsg(3, "<c@x.test>", 10, 2)
	newest := mailMsg(4, fInbox, "<d@x.test>", "d", 1)
	local := mailMsg(5, fDeleted, "<e@x.test>", "e", 0) // a discarded draft proves nothing about Outlook's sync
	project := mailMsg(8, fProject, "<h@x.test>", "h", 0)
	b := mailBatch(mailT0, inbox, toMe, sent, newest, local, project)
	mustCommitMail(t, s, b)
	got := spansOf(t, s, mailAcct)
	if !got.Recorded || got.Legacy || !got.FreshAt.Equal(mailT0) {
		t.Fatalf("spans %+v", got)
	}
	end := newest.Received
	if !slices.Equal(got.Spans[MailRoleInbox], []TimeSpan{span(inbox.Received, end)}) {
		t.Fatalf("inbox %v", got.Spans[MailRoleInbox])
	}
	if !slices.Equal(got.Spans[MailRoleSent], []TimeSpan{span(sent.Received, end)}) {
		t.Fatalf("sent %v", got.Spans[MailRoleSent])
	}
	if got.SentShift != 48*time.Hour {
		t.Fatalf("sent shift %v", got.SentShift)
	}

	// The next read: Outlook evicted the old messages and a new one arrived after the store write.
	later := mailT0.AddDate(0, 0, 2)
	fresh := mailMsg(6, fInbox, "<f@x.test>", "f", -1)
	b = mailBatch(later, mailMsg(7, fInbox, "<g@x.test>", "g", 5), fresh)
	b.FreshAt = mailT0.Add(12 * time.Hour) // the store was written before the newest message's time
	mustCommitMail(t, s, b)
	got = spansOf(t, s, mailAcct)
	if fresh.Received.Before(b.FreshAt) || !slices.Equal(got.Spans[MailRoleInbox], []TimeSpan{span(inbox.Received, b.FreshAt)}) {
		t.Fatalf("inbox after eviction %v", got.Spans[MailRoleInbox])
	}
	if !slices.Equal(got.Spans[MailRoleSent], []TimeSpan{span(sent.Received, end)}) {
		t.Fatalf("sent after a read without Sent mail %v", got.Spans[MailRoleSent])
	}
}

// A read that may have missed a message records the key but no span; a missing detail object
// blocks only the roles of the folders its headers name, and a folder the read does not hold
// blocks every role.
func TestIncompleteMailReadsRecordNoSpan(t *testing.T) {
	msgs := func() []outlookmail.Message {
		return []outlookmail.Message{mailMsg(1, fInbox, "<a@x.test>", "a", 5), sentMsg(2, "<b@x.test>", 4, 0), mailMsg(3, fInbox, "<c@x.test>", "c", 1)}
	}
	for name, change := range map[string]func(*MailBatch){
		"doubtful": func(b *MailBatch) { b.Result.Doubtful = true },
		"unstable": func(b *MailBatch) { b.Unstable = true },
		"loss": func(b *MailBatch) {
			b.Result.Losses = []outlookmail.Loss{{Code: outlookmail.CodeMailUnmapped, Count: 1}}
		},
		"missing folder": func(b *MailBatch) { b.Result.Notes.MissingFolder = 1 },
		"unknown folder": func(b *MailBatch) { b.Result.MissingDetailFolders = map[uint32]int{999: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			b := mailBatch(mailT0, msgs()...)
			change(&b)
			mustCommitMail(t, s, b)
			got := spansOf(t, s, mailAcct)
			if !got.Recorded || len(got.Spans[MailRoleInbox]) != 0 || len(got.Spans[MailRoleSent]) != 0 {
				t.Fatalf("spans %+v", got)
			}
		})
	}
	t.Run("layout partial only", func(t *testing.T) {
		s := newStore(t)
		b := mailBatch(mailT0, msgs()...)
		b.Result.Losses = []outlookmail.Loss{{Code: outlookmail.CodeMailLayoutPartial, Count: 7}}
		b.Trusted = false
		b.Result.MissingDetailFolders = map[uint32]int{fSent: 1}
		mustCommitMail(t, s, b)
		got := spansOf(t, s, mailAcct)
		if len(got.Spans[MailRoleInbox]) != 1 || len(got.Spans[MailRoleSent]) != 0 {
			t.Fatalf("spans %+v", got)
		}
	})
	t.Run("withheld", func(t *testing.T) {
		s := newStore(t)
		many := []outlookmail.Message{mailMsg(99, fInbox, "<new@x.test>", "n", 1)}
		for i := range 30 {
			many = append(many, mailMsg(uint32(100+i), fInbox, "", "m", 3))
		}
		mustCommitMail(t, s, mailBatch(mailT0, many...))
		want := []TimeSpan{span(mailT0.AddDate(0, 0, -3), mailT0.AddDate(0, 0, -1))}
		if got := spansOf(t, s, mailAcct); !slices.Equal(got.Spans[MailRoleInbox], want) {
			t.Fatalf("first read %+v", got)
		}
		b := mailBatch(mailT0.Add(time.Hour), mailMsg(99, fInbox, "<new@x.test>", "n", 1))
		b.FreshAt = mailT0.Add(time.Hour)
		b.Result.Coverage[0].Oldest = mailT0.AddDate(0, 0, -10)
		if r := mustCommitMail(t, s, b); r.Withheld == 0 {
			t.Fatalf("result %+v", r)
		}
		if got := spansOf(t, s, mailAcct); !slices.Equal(got.Spans[MailRoleInbox], want) {
			t.Fatalf("the withheld read added its span: %+v", got)
		}
	})
	t.Run("no write time", func(t *testing.T) {
		s := newStore(t)
		b := mailBatch(mailT0, msgs()...)
		b.FreshAt = time.Time{}
		mustCommitMail(t, s, b)
		if got := spansOf(t, s, mailAcct); got.Recorded {
			t.Fatalf("spans %+v", got)
		}
	})
}

// A stored value that does not parse holds no span; a span with an unreadable time is left out.
func TestDamagedMailSpansCoverNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x.test>", "a", 5), mailMsg(2, fInbox, "<b@x.test>", "b", 1)))
	must(s.db.ExecContext(ctx, `update meta set value='not json' where key=?`, mailSpanKey+mailAcct))
	if got := spansOf(t, s, mailAcct); !got.Recorded || len(got.Spans[MailRoleInbox]) != 0 {
		t.Fatalf("spans %+v", got)
	}
	must(s.db.ExecContext(ctx, `update meta set value='{"inbox":[{"from":"bad","to":"2026-09-10T08:00:00.000Z"},{"from":"2026-09-01T08:00:00.000Z","to":"2026-09-02T08:00:00.000Z"}]}' where key=?`, mailSpanKey+mailAcct))
	if got := spansOf(t, s, mailAcct); len(got.Spans[MailRoleInbox]) != 1 || got.Spans[MailRoleInbox][0].From.Day() != 1 {
		t.Fatalf("spans %+v", got)
	}
	// The next complete read merges into what parsed.
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), mailMsg(1, fInbox, "<a@x.test>", "a", 5), mailMsg(2, fInbox, "<b@x.test>", "b", 1)))
	if got := spansOf(t, s, mailAcct); len(got.Spans[MailRoleInbox]) != 2 {
		t.Fatalf("spans %+v", got)
	}
}

// Without recorded spans (an archive no sync of this version read), the last read's ranges stand
// in, only when that read's run could have recorded them.
func TestLegacyMailSpansNeedAQualifyingLastRead(t *testing.T) {
	ctx := context.Background()
	key := "outlook|profile-1|mail"
	setup := func(t *testing.T) *Store {
		s := newStore(t)
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x.test>", "a", 5), sentMsg(2, "<b@x.test>", 4, 1), mailMsg(3, fInbox, "<c@x.test>", "c", 1), mailMsg(4, fProject, "<d@x.test>", "d", 0)))
		other := mailBatch(mailT0, mailMsg(9, fInbox, "<o@x.test>", "o", 30)) // another account's ranges are not this one's
		other.Account = "outlook/Other"
		other.Result.Messages[0].Account = other.Account
		mustCommitMail(t, s, other)
		must(s.db.ExecContext(ctx, `delete from meta where key=?`, mailSpanKey+mailAcct))
		return s
	}
	run := func(t *testing.T, s *Store, status string, omissions map[string]int) {
		t.Helper()
		must0(s.RecordRun(ctx, Run{StartedAt: mailT0, FinishedAt: mailT0, Source: key, Status: status, Omissions: omissions}))
	}
	s := setup(t)
	if got := spansOf(t, s, mailAcct); got.Recorded || got.Legacy || len(got.Spans) != 0 {
		t.Fatalf("without a run %+v", got)
	}
	run(t, s, "ok", nil)
	run(t, s, "unchanged", nil)
	run(t, s, "skipped_interval", nil)
	got := spansOf(t, s, mailAcct)
	newest := mailT0.AddDate(0, 0, -1)
	if !got.Legacy || !slices.Equal(got.Spans[MailRoleInbox], []TimeSpan{span(mailT0.AddDate(0, 0, -5), newest)}) ||
		!slices.Equal(got.Spans[MailRoleSent], []TimeSpan{span(mailT0.AddDate(0, 0, -5), newest)}) {
		t.Fatalf("legacy %+v", got)
	}
	run(t, s, "ok_with_omissions", map[string]int{outlookmail.CodeMailLayoutPartial: 3})
	if got := spansOf(t, s, mailAcct); !got.Legacy {
		t.Fatalf("layout partial %+v", got)
	}
	run(t, s, "ok_with_omissions", map[string]int{outlookmail.CodeMailUnmapped: 1})
	if got := spansOf(t, s, mailAcct); got.Legacy {
		t.Fatalf("unmapped %+v", got)
	}
	run(t, s, "failed", nil)
	if got := spansOf(t, s, mailAcct); got.Legacy {
		t.Fatalf("failed %+v", got)
	}
	run(t, s, "ok_with_omissions", nil)
	must(s.db.ExecContext(ctx, `update sync_runs set omissions_json='[' where id=(select max(id) from sync_runs)`))
	if got := spansOf(t, s, mailAcct); got.Legacy {
		t.Fatalf("unreadable omissions %+v", got)
	}

	s = setup(t)
	run(t, s, "ok", nil)
	must0(s.SetMailFailure(ctx, mailAcct, OutlookFailure{Code: "outlook_profile_unreadable"}))
	if got := spansOf(t, s, mailAcct); got.Legacy {
		t.Fatalf("with a failure %+v", got)
	}

	// Without a write time the legacy span ends at the newest message.
	s = setup(t)
	run(t, s, "ok", nil)
	must(s.db.ExecContext(ctx, `delete from meta where key=?`, mailFreshKey+mailAcct))
	if got := spansOf(t, s, mailAcct); !slices.Equal(got.Spans[MailRoleInbox], []TimeSpan{span(mailT0.AddDate(0, 0, -5), newest)}) {
		t.Fatalf("no write time %+v", got)
	}
}

func TestMailCoverageSpansOnOlderAndBrokenArchives(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must(s.db.ExecContext(ctx, `drop table meta`))
	if got := spansOf(t, s, mailAcct); got.Recorded || len(got.Spans) != 0 {
		t.Fatalf("no meta %+v", got)
	}
	must0(s.Close())
	if _, err := s.MailCoverageSpans(ctx, mailAcct); err == nil {
		t.Fatal("a closed archive answered")
	}
}

func TestMailServerThroughIsTheNewestCompleteWorkIQMailSync(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if !must(s.MailServerThrough(ctx, mailAcct)).IsZero() {
		t.Fatal("an archive without WorkIQ mail had a server copy")
	}
	attempt := seedCompletedWorkIQMail(t, s)
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0.Add(time.Hour), MailProof: &proof}))
	through := must(s.MailServerThrough(ctx, mailAcct))
	if through.IsZero() {
		t.Fatal("a complete sync gave no server time")
	}
	if !must(s.MailServerThrough(ctx, "outlook/Other")).IsZero() {
		t.Fatal("one account's sync covered another")
	}
	must(s.db.ExecContext(ctx, `update workiq_mail_receipts set source_data_at='bad'`))
	if !must(s.MailServerThrough(ctx, mailAcct)).IsZero() {
		t.Fatal("an unreadable source time counted")
	}
	must0(s.Close())
	if _, err := s.MailServerThrough(ctx, mailAcct); err == nil {
		t.Fatal("a closed archive answered")
	}
}

// A role's count holds every message once, marked ones included, whether or not the account has
// older folder copies; an account without a folder of the role holds nothing.
func TestMailRoleCountCountsMarkedMessages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x.test>", "a", 2), mailMsg(2, fToMe, "<b@x.test>", "b", 2), sentMsg(3, "<c@x.test>", 2, 0), mailMsg(4, fInbox, "<d@x.test>", "d", 9)))
	must(s.db.ExecContext(ctx, `update mail_messages set gone_at=? where detail_key=2`, mailT0.Format(timeLayout)))
	since, until := mailT0.AddDate(0, 0, -3), mailT0.AddDate(0, 0, -1)
	c := must(s.MailRoleCount(ctx, mailAcct, MailRoleInbox, since, until))
	if c.Messages != 2 || c.Marked != 1 || c.State != "consistent" || c.DatedBy != "received_at" {
		t.Fatalf("inbox %+v", c)
	}
	if c := must(s.MailRoleCount(ctx, mailAcct, MailRoleSent, since, until)); c.Messages != 1 || c.Marked != 0 || c.DatedBy != "sent_at" {
		t.Fatalf("sent %+v", c)
	}
	if c := must(s.MailRoleCount(ctx, "outlook/Other", MailRoleSent, since, until)); c.Messages != 0 || c.State != "consistent" || c.Kind != MailRoleSent {
		t.Fatalf("other account %+v", c)
	}
	must0(s.Close())
	if _, err := s.MailRoleCount(ctx, mailAcct, MailRoleSent, since, until); err == nil {
		t.Fatal("a closed archive answered")
	}
}
