package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func seedMailForFaults(t *testing.T, s *Store) {
	t.Helper()
	pending := mailMsg(3, fInbox, "<3@x>", "Pending", 3)
	pending.Body = outlookmail.Body{State: outlookmail.BodyNotRead}
	pending.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt"}}
	mustCommitMail(t, s, mailBatch(mailT0, faultMail(1)[0], faultMail(1)[1], pending, mailMsg(4, fInbox, "<4@x>", "Gone later", 10), mailMsg(5, fInbox, "<5@x>", "Rekeyed", 8), mailMsg(6, fInbox, "<6@x>", "Replaced", 7), mailMsg(7, fInbox, "<7@x>", "Stays missing", 9), mailMsg(8, fSent, "<8@x>", "Only in Sent", 6)))
	if _, err := s.db.Exec(`insert into mail_absent(account, detail_key, first_missed_at, misses, fresh_at) values(?,4,'x',1,'2026-09-10T08:00:00.000Z'),(?,7,'x',1,'2026-09-10T08:00:00.000Z'),(?,1,'x',1,'')`, mailAcct, mailAcct, mailAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update mail_messages set gone_at='2026-09-10T08:00:00.000Z' where detail_key=2`); err != nil {
		t.Fatal(err)
	}
}

func faultMail(int) []outlookmail.Message {
	one := mailMsg(1, fInbox, "<1@x>", "One", 30)
	one.Attachments = []outlookmail.Attachment{{Key: 1, Name: "one.txt"}}
	two := mailMsg(2, fInbox, "<2@x>", "Two", 12)
	return []outlookmail.Message{one, two}
}

func TestCommitMailFailuresRollBack(t *testing.T) {
	t.Run("insert", func(t *testing.T) {
		sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
			m := faultMail(0)
			m[0].Recipients = append(m[0].Recipients, outlookmail.Recipient{Name: "No address"})
			_, err := s.CommitMail(ctx, mailBatch(hr(1), m...))
			return err
		})
	})
	t.Run("update every kind", func(t *testing.T) {
		sweepFaults(t, seedMailForFaults, func(ctx context.Context, s *Store) error {
			read := false
			one, two := faultMail(0)[0], faultMail(0)[1]
			one.Attachments = []outlookmail.Attachment{{Key: 1, Name: "one.txt", Downloaded: true}, {Key: 2, Name: "two.txt"}}
			one.Unread, one.ReadState = &read, "read"
			pending := mailMsg(3, fInbox, "<3@x>", "Pending", 3)
			pending.Body = outlookmail.Body{State: outlookmail.BodyFile, HTML: []byte("<p>filled</p>")}
			pending.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt"}}
			moved := mailMsg(55, fInbox, "<5@x>", "Rekeyed", 8)
			replaced := mailMsg(6, fInbox, "<new6@x>", "Replaced twice", 7)
			replaced.Recipients = append(replaced.Recipients, outlookmail.Recipient{Name: "No address"})
			newMsg := mailMsg(8, fInbox, "<8@x>", "Added", 2)
			_, err := s.CommitMail(ctx, mailBatch(hr(2), one, two, pending, moved, replaced, newMsg))
			return err
		})
	})
	t.Run("judge missing", func(t *testing.T) {
		sweepFaults(t, seedMailForFaults, func(ctx context.Context, s *Store) error {
			keep := faultMail(0)
			old := mailMsg(9, fInbox, "<9@x>", "Older than range", 90)
			_ = old
			// 4 is confirmed (gone), 7 is confirmed but older than the range (evicted), 5 and 6 are first misses.
			_, err := s.CommitMail(ctx, mailBatch(hr(2), keep...))
			return err
		})
	})
	t.Run("judge evicted", func(t *testing.T) {
		sweepFaults(t, func(t *testing.T, s *Store) {
			seedMailForFaults(t, s)
			if _, err := s.db.Exec(`update mail_messages set received_at='2000-01-01T00:00:00.000Z' where detail_key=7`); err != nil {
				t.Fatal(err)
			}
		}, func(ctx context.Context, s *Store) error {
			_, err := s.CommitMail(ctx, mailBatch(hr(2), faultMail(0)...))
			return err
		})
	})
}

func TestMailQueriesFail(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		a := mailMsg(1, fInbox, "<1@x>", "Alpha", 5)
		b := mailMsg(2, fInbox, "<2@x>", "Re: Alpha", 4)
		b.InReplyTo = "<1@x>"
		c := mailMsg(3, fSent, "<3@x>", "Lone", 3)
		d := mailMsg(4, fSent, "<4@x>", "Lone", 2)
		d.Attachments = []outlookmail.Attachment{{Key: 1, Name: "d.txt"}}
		lost := mailMsg(5, 999, "<5@x>", "No folder row", 1)
		mustCommitMail(t, s, mailBatch(mailT0, a, b, c, d, lost))
	}
	run := func(name string, op func(ctx context.Context, s *Store) error) {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, setup, op) })
	}
	run("list", func(ctx context.Context, s *Store) error {
		_, err := s.MailList(ctx, MailFilter{Folder: "inbox", Query: "alpha", From: "ann", Since: mailT0.AddDate(0, 0, -9), Until: mailT0, Unread: true, Flagged: false})
		return err
	})
	run("list all", func(ctx context.Context, s *Store) error {
		_, err := s.MailList(ctx, MailFilter{HasAttachments: true, Flagged: true, IncludeGone: true, IncludeEvicted: true})
		return err
	})
	run("get", func(ctx context.Context, s *Store) error {
		_, err := s.MailGet(ctx, mailAcct, 4)
		return err
	})
	run("thread chain", func(ctx context.Context, s *Store) error {
		_, _, _, err := s.MailThread(ctx, mailAcct, 2)
		return err
	})
	run("thread subject", func(ctx context.Context, s *Store) error {
		_, _, _, err := s.MailThread(ctx, mailAcct, 3)
		return err
	})
	run("folders", func(ctx context.Context, s *Store) error {
		_, err := s.MailFolders(ctx)
		return err
	})
	run("coverage", func(ctx context.Context, s *Store) error {
		_, err := s.MailCoverage(ctx)
		return err
	})
	run("need body", func(ctx context.Context, s *Store) error {
		_, err := s.MailNeedBody(ctx, mailAcct)
		return err
	})
	run("holds", func(ctx context.Context, s *Store) error {
		_, err := s.MailHolds(ctx, mailAcct)
		return err
	})
}

func TestMailRederiveFailuresRollBack(t *testing.T) {
	sweepFaults(t, func(t *testing.T, s *Store) {
		seedMailForRederive(t, s)
	}, func(ctx context.Context, s *Store) error {
		_, err := s.Rederive(ctx)
		return err
	})
}

func seedMailForRederive(t *testing.T, s *Store) {
	t.Helper()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Re: One", 3), mailMsg(2, fInbox, "<2@x>", "Two", 2)))
	if _, err := s.db.Exec(`update mail_messages set body_text_version=0, body_text='stale', subject_norm='stale'; delete from meta where key='mail_text_version'`); err != nil {
		t.Fatal(err)
	}
}

func TestRederiveMail(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if m, err := s.Rederive(ctx); err != nil || m != nil {
		t.Fatalf("new archive: %+v %v", m, err)
	}
	if n := rowCount(t, s, `select count(*) from meta where key='mail_text_version' and value='1'`); n != 1 {
		t.Fatal("mail_text_version not recorded")
	}
	none := mailMsg(3, fInbox, "<3@x>", "Re: No html", 1)
	none.Body = outlookmail.Body{State: outlookmail.BodyNone}
	broken := mailMsg(4, fInbox, "<4@x>", "Broken gzip", 1)
	same := mailMsg(5, fInbox, "<5@x>", "Already right", 1)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Re: One", 3), mailMsg(2, fInbox, "<2@x>", "Two", 2), none, broken, same))
	if _, err := s.db.Exec(`update mail_messages set body_text_version=0, body_text='stale', subject_norm='stale' where detail_key in (1,2,3);
update mail_messages set body_text_version=0, body_html_gz=x'1f8b00' where detail_key=4;
update mail_messages set body_text_version=0 where detail_key=5;
update meta set value='0' where key='mail_text_version'`); err != nil {
		t.Fatal(err)
	}
	m, err := s.Rederive(ctx)
	if err != nil || m == nil || m.MailRows != 3 || m.From != DerivationVersion || m.To != DerivationVersion || m.Rows != 0 {
		t.Fatalf("Rederive = %+v, %v", m, err)
	}
	one, _ := s.MailGet(ctx, mailAcct, 1)
	nh, _ := s.MailGet(ctx, mailAcct, 3)
	bk, _ := s.MailGet(ctx, mailAcct, 4)
	if one.BodyText != "body of Re: One" || one.SubjectNorm != "One" || nh.SubjectNorm != "No html" || nh.BodyText != "stale" || bk.BodyText != "body of Broken gzip" {
		t.Fatalf("rederived: %q %q | %q %q | %q", one.BodyText, one.SubjectNorm, nh.SubjectNorm, nh.BodyText, bk.BodyText)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages where body_text_version<>1`); n != 0 {
		t.Fatalf("rows below the version: %d", n)
	}
	if rows, _ := s.MailList(ctx, MailFilter{Query: "one"}); len(rows) != 1 {
		t.Fatalf("index after rederive: %d rows for the rederived text", len(rows))
	}
	if m, err := s.Rederive(ctx); err != nil || m != nil {
		t.Fatalf("second Rederive: %+v %v", m, err)
	}
	// A truncated gzip, and a body that inflates past the cap, both keep their text.
	if _, ok := inflateMailHTML([]byte{0x1f}); ok {
		t.Fatal("inflated a stub")
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(bytes.Repeat([]byte("<p>x</p>"), 500))
	_ = zw.Close()
	if _, ok := inflateMailHTML(buf.Bytes()[:buf.Len()/2]); ok {
		t.Fatal("inflated a truncated body")
	}
	if _, ok := inflateMailHTML(nil); ok {
		t.Fatal("inflated nothing")
	}
}

func TestRederiveMailAndTeamsTogether(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedAlpha1(t, s)
	seedMailForRederive(t, s)
	m, err := s.Rederive(ctx)
	if err != nil || m == nil || m.Rows != 2 || m.MailRows != 2 || m.From != 1 {
		t.Fatalf("Rederive = %+v, %v", m, err)
	}
	// Only mail changed: the Teams derivation is current.
	if _, err := s.db.Exec(`update mail_messages set body_text_version=0, subject_norm='stale'; update meta set value='0' where key='mail_text_version'`); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Rederive(ctx); err != nil || m == nil || m.Rows != 0 || m.MailRows != 2 {
		t.Fatalf("mail only: %+v %v", m, err)
	}
}

func TestRederiveMailRefusesNewerAndBadVersions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, err := s.db.Exec(`insert into meta(key, value) values('mail_text_version','2') on conflict(key) do update set value='2'`); err != nil {
		t.Fatal(err)
	}
	var coded *errs.Coded
	if _, err := s.Rederive(ctx); !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer {
		t.Fatalf("newer mail version: %v", err)
	}
	if _, err := s.db.Exec(`update meta set value='x' where key='mail_text_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rederive(ctx); err == nil {
		t.Fatal("a non-numeric version was accepted")
	}
}

func TestRederiveMailBatches(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	msgs := manyMail(mailRederiveBatch + 5)
	mustCommitMail(t, s, mailBatch(mailT0, msgs...))
	if _, err := s.db.Exec(`update mail_messages set body_text_version=0, subject_norm='stale'; delete from meta where key='mail_text_version'`); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Rederive(ctx); err != nil || m == nil || m.MailRows != len(msgs) {
		t.Fatalf("Rederive = %+v, %v", m, err)
	}
}
