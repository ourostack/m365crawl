package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

const mailAcct = "outlook/profile-1"

var mailT0 = time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)

// Folder keys of the synthetic mailbox.
const (
	fInbox   = 10
	fSent    = 11
	fDeleted = 12
	fJunk    = 13
	fToMe    = 14
)

func mailFolders() []outlookmail.Folder {
	return []outlookmail.Folder{
		{Key: fInbox, Parent: 1, Name: "Inbox", Kind: "inbox"},
		{Key: fSent, Parent: 1, Name: "Sent Items", Kind: "sent"},
		{Key: fDeleted, Parent: 1, Name: "Deleted Items", Kind: "deleted"},
		{Key: fJunk, Parent: 1, Name: "Junk Email", Kind: "junk"},
		{Key: fToMe, Parent: 1, Name: "To Me", Kind: "to_me"},
	}
}

func folderKind(key uint32) string {
	for _, f := range mailFolders() {
		if f.Key == key {
			return f.Kind
		}
	}
	return "other"
}

// mailMsg builds a synthetic message received days before mailT0.
func mailMsg(dk, folder uint32, msgID, subject string, daysAgo int) outlookmail.Message {
	unread := true
	recv := mailT0.AddDate(0, 0, -daysAgo)
	m := outlookmail.Message{Account: mailAcct, Folder: outlookmail.Folder{Key: folder, Kind: folderKind(folder)}, Copies: 1}
	m.DetailKey, m.FolderKey = dk, folder
	m.MessageID, m.Subject, m.Received, m.Sent = msgID, subject, recv, recv.Add(-time.Minute)
	m.SenderName, m.SenderAddress = "Ann Sender", "ann@example.test"
	m.Unread, m.ReadState, m.Flag, m.Importance = &unread, "unread", "none", "normal"
	m.Recipients = []outlookmail.Recipient{{Name: "Bob Reader", Address: "bob@example.test", KindRaw: 1}}
	m.Class = "IPM.Note"
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>body of " + subject + "</p>")}
	return m
}

// mailBatch builds a trusted batch whose coverage is what the messages hold.
func mailBatch(read time.Time, msgs ...outlookmail.Message) MailBatch {
	cov := map[uint32]*outlookmail.Coverage{}
	for _, m := range msgs {
		c := cov[m.FolderKey]
		if c == nil {
			c = &outlookmail.Coverage{FolderKey: m.FolderKey, Oldest: m.Received, Newest: m.Received}
			cov[m.FolderKey] = c
		}
		c.Count++
		if m.Received.Before(c.Oldest) {
			c.Oldest = m.Received
		}
		if m.Received.After(c.Newest) {
			c.Newest = m.Received
		}
	}
	res := outlookmail.Result{Messages: msgs, Folders: mailFolders()}
	for _, f := range mailFolders() {
		if c := cov[f.Key]; c != nil {
			res.Coverage = append(res.Coverage, *c)
		}
	}
	return MailBatch{Account: mailAcct, ReadAt: read, FreshAt: read, Result: res, Trusted: true}
}

func mustCommitMail(t *testing.T, s *Store, b MailBatch) MailResult {
	t.Helper()
	r, err := s.CommitMail(context.Background(), b)
	if err != nil {
		t.Fatalf("CommitMail: %v", err)
	}
	return r
}

func TestSchemaMigratesV5ToV6(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"mail_folders", "mail_messages", "mail_recipients", "mail_attachments", "mail_coverage", "mail_absent", "mail_fts"} {
		if _, err := s.db.Exec(`drop table ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`update schema_migrations set version = 5`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open v5 archive: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := rowCount(t, s, `select max(version) from schema_migrations`); v != 6 || SchemaVersion != 6 {
		t.Fatalf("version %d, SchemaVersion %d", v, SchemaVersion)
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where name in ('mail_folders','mail_messages','mail_recipients','mail_attachments','mail_coverage','mail_absent','mail_fts')`); n != 7 {
		t.Fatalf("mail tables: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages`); n != 0 {
		t.Fatalf("mail rows: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where type='index' and name in ('mail_messages_message_id','mail_messages_in_reply_to','mail_messages_subject_norm')`); n != 3 {
		t.Fatalf("thread indexes: %d", n)
	}
}

func TestOpenRefusesAnArchiveFromANewerSchemaWithMail(t *testing.T) {
	path := writableArchivePath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update schema_migrations set version = 7`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	var coded *errs.Coded
	if _, err := Open(context.Background(), path); !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer {
		t.Fatalf("Open = %v, want archive_newer", err)
	}
}

func TestCommitMailInsertsThenIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m1 := mailMsg(100, fInbox, "<a@x>", "Re: Quarterly plan", 2)
	m1.Attachments = []outlookmail.Attachment{{Key: 1, MessageKey: 100, Name: "plan.pdf", Size: 10, ContentType: "application/pdf"}, {Key: 2, Name: "0a1b2c3d", Inline: true}}
	m1.ICalUID = "ABCDEF"
	m2 := mailMsg(101, fSent, "<b@x>", "Lunch", 1)
	m2.Recipients = []outlookmail.Recipient{{Name: "No Address"}}
	m2.Unread, m2.ReadState = nil, "unknown"
	b := mailBatch(mailT0, m1, m2)
	if r := mustCommitMail(t, s, b); r != (MailResult{Added: 2}) {
		t.Fatalf("first commit: %+v", r)
	}
	if r := mustCommitMail(t, s, b); r != (MailResult{}) {
		t.Fatalf("recommit unchanged: %+v", r)
	}
	got, err := s.MailGet(ctx, mailAcct, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != mailAcct+":100" || got.Subject != "Re: Quarterly plan" || got.SubjectNorm != "Quarterly plan" || got.Folder != "Inbox" || got.FolderKind != "inbox" ||
		got.IsRead == nil || *got.IsRead || got.ReadState != "unread" || !got.HasAttachments || len(got.Attachments) != 2 || !got.Attachments[1].Inline ||
		got.BodyState != "inline" || got.BodyText != "body of Re: Quarterly plan" || got.ICalUID != "abcdef" || !got.ReceivedAt.Equal(m1.Received) || !got.SentAt.Equal(m1.Sent) ||
		got.InternetMessageID != "<a@x>" || len(got.Recipients) != 1 || got.Recipients[0].Address != "bob@example.test" || !got.FirstSeenAt.Equal(mailT0) {
		t.Fatalf("MailGet = %+v", got)
	}
	g2, _ := s.MailGet(ctx, mailAcct, 101)
	if g2.IsRead != nil || g2.ReadState != "unknown" || len(g2.Recipients) != 1 || g2.Recipients[0].Address != "" || g2.HasAttachments {
		t.Fatalf("second message: %+v", g2)
	}
	if _, err := s.MailGet(ctx, mailAcct, 999); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("missing message: %v", err)
	}
	if held, err := s.MailHolds(ctx, mailAcct); err != nil || !held {
		t.Fatalf("MailHolds = %v, %v", held, err)
	}
	if held, _ := s.MailHolds(ctx, "outlook/other"); held {
		t.Fatal("MailHolds for another account")
	}
}

func TestCommitMailUpdatesStateInPlace(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := mailMsg(100, fInbox, "<a@x>", "Plan", 2)
	mustCommitMail(t, s, mailBatch(mailT0, m))
	t1 := mailT0.Add(time.Hour)
	changed := m
	read := false
	changed.Unread, changed.ReadState, changed.Flag, changed.Importance, changed.ToMe = &read, "read", "flagged", "high", true
	changed.FolderKey, changed.Folder = fDeleted, outlookmail.Folder{Key: fDeleted, Kind: "deleted"}
	// Content columns differ in the new read; they must not be taken.
	changed.Subject, changed.SenderName, changed.Preview = "Other", "Someone Else", "preview"
	changed.Recipients = nil
	if r := mustCommitMail(t, s, mailBatch(t1, changed)); r != (MailResult{Updated: 1}) {
		t.Fatalf("state change: %+v", r)
	}
	got, _ := s.MailGet(ctx, mailAcct, 100)
	if got.IsRead == nil || !*got.IsRead || got.Flag != "flagged" || got.Importance != "high" || !got.ToMe || got.FolderKey != fDeleted || got.Folder != "Deleted Items" ||
		!got.StateSeenAt.Equal(t1) || got.Subject != "Plan" || got.SenderName != "Ann Sender" || len(got.Recipients) != 1 {
		t.Fatalf("after update: %+v", got)
	}
	// An unknown read state is also a change from a known one.
	changed.Unread, changed.ReadState = nil, "unknown"
	if r := mustCommitMail(t, s, mailBatch(t1.Add(time.Hour), changed)); r.Updated != 1 {
		t.Fatalf("unknown read state: %+v", r)
	}
	// Known again, and a change of the read value alone.
	yes := true
	changed.Unread, changed.ReadState = &yes, "unread"
	if r := mustCommitMail(t, s, mailBatch(t1.Add(2*time.Hour), changed)); r.Updated != 1 {
		t.Fatalf("unread: %+v", r)
	}
	changed.ReadState = "read"
	changed.Unread = &read
	if r := mustCommitMail(t, s, mailBatch(t1.Add(3*time.Hour), changed)); r.Updated != 1 {
		t.Fatalf("read: %+v", r)
	}
}

func TestCommitMailReplacesAKeyThatTookANewMessage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	old := mailMsg(100, fInbox, "<a@x>", "Zebra crossing", 2)
	old.Attachments = []outlookmail.Attachment{{Key: 1, Name: "old.txt"}}
	mustCommitMail(t, s, mailBatch(mailT0, old))
	fresh := mailMsg(100, fInbox, "<new@x>", "Giraffe feeding", 1)
	fresh.Recipients = []outlookmail.Recipient{{Name: "Cy", Address: "cy@example.test"}, {Name: "Di", Address: "di@example.test"}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), fresh)); r != (MailResult{Replaced: 1}) {
		t.Fatalf("replace: %+v", r)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages`); n != 1 {
		t.Fatalf("rows: %d", n)
	}
	got, _ := s.MailGet(ctx, mailAcct, 100)
	if got.Subject != "Giraffe feeding" || got.InternetMessageID != "<new@x>" || len(got.Recipients) != 2 || len(got.Attachments) != 0 || got.BodyText != "body of Giraffe feeding" {
		t.Fatalf("after replace: %+v", got)
	}
	if rows, _ := s.MailList(ctx, MailFilter{Query: "zebra"}); len(rows) != 0 {
		t.Fatalf("old text still indexed: %d rows", len(rows))
	}
	if rows, _ := s.MailList(ctx, MailFilter{Query: "giraffe"}); len(rows) != 1 {
		t.Fatalf("new text not indexed: %d rows", len(rows))
	}
}

func TestCommitMailRekeysOnMessageID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := mailMsg(100, fInbox, "<a@x>", "Keep", 3)
	other := mailMsg(101, fInbox, "<b@x>", "Other", 2)
	mustCommitMail(t, s, mailBatch(mailT0, a, other))
	// A gone mark and an absence on the old key do not survive the move.
	if _, err := s.db.Exec(`insert into mail_absent(account, detail_key, first_missed_at, misses, fresh_at) values(?,100,'x',1,'')`, mailAcct); err != nil {
		t.Fatal(err)
	}
	moved := mailMsg(200, fInbox, "<a@x>", "Keep", 3)
	read := false
	moved.Unread, moved.ReadState = &read, "read"
	r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), moved, other))
	if r != (MailResult{Updated: 1}) {
		t.Fatalf("rekey: %+v", r)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages`); n != 2 {
		t.Fatalf("rows: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages where detail_key=200 and rowid=1 and read_state='read'`); n != 1 {
		t.Fatal("the row did not keep its rowid and take the new state")
	}
	if n := rowCount(t, s, `select count(*) from mail_absent`); n != 0 {
		t.Fatalf("absence rows: %d", n)
	}
	// Same Message-ID in another folder kind is a different message, not a re-key.
	sent := mailMsg(300, fSent, "<a@x>", "Keep", 3)
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), moved, other, sent)); r != (MailResult{Added: 1}) {
		t.Fatalf("other folder kind: %+v", r)
	}
	// A key already in the read is never taken for a re-key.
	dup := mailMsg(400, fInbox, "<b@x>", "Other", 2)
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(3*time.Hour), moved, other, sent, dup)); r != (MailResult{Added: 1}) {
		t.Fatalf("duplicate of a held message: %+v", r)
	}
	_ = ctx
}

func TestCommitMailFillsLateFields(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := mailMsg(100, fInbox, "<a@x>", "Late", 2)
	m.Body = outlookmail.Body{State: outlookmail.BodyNotRead, Path: "~/Files/x.dat"} // not read
	m.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt"}}
	mustCommitMail(t, s, mailBatch(mailT0, m))
	got, _ := s.MailGet(ctx, mailAcct, 100)
	if got.BodyState != "pending" || got.BodyText != "" || !got.HasAttachments {
		t.Fatalf("pending: %+v", got)
	}
	need, err := s.MailNeedBody(ctx, mailAcct)
	if err != nil || !need(100) || !need(999) {
		t.Fatalf("MailNeedBody before: %v", err)
	}
	// The body file is missing, then readable; an attachment arrives and one is downloaded.
	m.Body = outlookmail.Body{State: outlookmail.BodyMissing}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), m)); r != (MailResult{Updated: 1}) {
		t.Fatalf("missing: %+v", r)
	}
	if g, _ := s.MailGet(ctx, mailAcct, 100); g.BodyState != "missing" {
		t.Fatalf("state: %s", g.BodyState)
	}
	m.Body = outlookmail.Body{State: outlookmail.BodyFile, HTML: []byte("<p>arrived <b>late</b></p>")}
	m.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt", Downloaded: true}, {Key: 2, Name: "b.txt"}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), m)); r != (MailResult{Updated: 1}) {
		t.Fatalf("fill: %+v", r)
	}
	got, _ = s.MailGet(ctx, mailAcct, 100)
	if got.BodyState != "file" || got.BodyText != "arrived late" || len(got.Attachments) != 2 || !got.Attachments[0].Downloaded {
		t.Fatalf("filled: %+v", got)
	}
	if rows, _ := s.MailList(ctx, MailFilter{Query: "arrived"}); len(rows) != 1 {
		t.Fatalf("filled text not indexed: %d", len(rows))
	}
	need, _ = s.MailNeedBody(ctx, mailAcct)
	if need(100) || !need(999) {
		t.Fatal("MailNeedBody after: a held body is wanted again")
	}
	// A stored body is never replaced, and the next read changes nothing.
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>different</p>")}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(3*time.Hour), m)); r != (MailResult{}) {
		t.Fatalf("kept body: %+v", r)
	}
	if g, _ := s.MailGet(ctx, mailAcct, 100); g.BodyText != "arrived late" {
		t.Fatalf("body replaced: %q", g.BodyText)
	}
	// has_attachments is only raised by a later read, never cleared.
	m.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt", Inline: true, Downloaded: true}, {Key: 2, Name: "b.txt", Inline: true}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(4*time.Hour), m)); r != (MailResult{}) {
		t.Fatalf("has_attachments: %+v", r)
	}
	if g, _ := s.MailGet(ctx, mailAcct, 100); !g.HasAttachments {
		t.Fatal("has_attachments was cleared")
	}
	// A body that is only inline-empty or none is stored as such.
	n := mailMsg(101, fInbox, "<n@x>", "No body", 1)
	n.Body = outlookmail.Body{State: outlookmail.BodyNone}
	mustCommitMail(t, s, mailBatch(mailT0.Add(5*time.Hour), m, n))
	if g, _ := s.MailGet(ctx, mailAcct, 101); g.BodyState != "none" {
		t.Fatalf("none: %s", g.BodyState)
	}
}

func TestMailListFilters(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := mailMsg(1, fInbox, "<1@x>", "Budget review", 5)
	b := mailMsg(2, fInbox, "<2@x>", "Holiday photos", 4)
	b.SenderName, b.SenderAddress = "Zed 100%", "zed@example.test"
	flag := false
	b.Unread, b.ReadState, b.Flag = &flag, "read", "flagged"
	b.Attachments = []outlookmail.Attachment{{Key: 1, Name: "p.png"}}
	c := mailMsg(3, fSent, "<3@x>", "Budget sent", 3)
	d := mailMsg(4, fDeleted, "<4@x>", "Budget deleted", 2)
	mustCommitMail(t, s, mailBatch(mailT0, a, b, c, d))
	list := func(f MailFilter) []uint32 {
		t.Helper()
		rows, err := s.MailList(ctx, f)
		if err != nil {
			t.Fatalf("MailList(%+v): %v", f, err)
		}
		out := []uint32{}
		for _, r := range rows {
			out = append(out, r.DetailKey)
		}
		return out
	}
	eq := func(got []uint32, want ...uint32) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	}
	eq(list(MailFilter{}), 4, 3, 2, 1)
	eq(list(MailFilter{Account: mailAcct, Limit: 2}), 4, 3)
	eq(list(MailFilter{Account: "other"}))
	eq(list(MailFilter{Folder: "inbox"}), 2, 1)
	eq(list(MailFilter{Folder: "sent items"}), 3)
	eq(list(MailFilter{Account: mailAcct, Folder: "Deleted"}), 4)
	eq(list(MailFilter{Folder: "deleted"}), 4)
	eq(list(MailFilter{From: "zed 100%"}), 2)
	eq(list(MailFilter{From: "100%"}), 2)
	eq(list(MailFilter{From: "ANN@"}), 4, 3, 1)
	eq(list(MailFilter{Unread: true}), 4, 3, 1)
	eq(list(MailFilter{Flagged: true}), 2)
	eq(list(MailFilter{HasAttachments: true}), 2)
	eq(list(MailFilter{Since: mailT0.AddDate(0, 0, -4)}), 4, 3, 2)
	eq(list(MailFilter{Until: mailT0.AddDate(0, 0, -4)}), 1)
	eq(list(MailFilter{Query: "budget", Folder: "inbox", Unread: true}), 1)
	eq(list(MailFilter{Query: `"holiday photos"`}), 2)
	eq(list(MailFilter{Query: "body of holiday"}), 2)
	if _, err := s.MailList(ctx, MailFilter{Folder: "nowhere"}); !errors.Is(err, ErrUnknownMailFolder) {
		t.Fatalf("unknown folder: %v", err)
	}
	if _, err := s.MailList(ctx, MailFilter{Query: `"`}); err == nil {
		t.Fatal("a query with no terms")
	}
}

func TestNormalizeSubject(t *testing.T) {
	for in, want := range map[string]string{
		"RE: Fw: x": "x", "AW: x": "x", "Re:Re: x": "x", "plain": "plain", "  Fwd :  Wg: SV: y ": "y", "Re": "Re", "Reply: x": "Reply: x",
		"": "", "Re: ": "", "fw:re: Mixed Case": "Mixed Case", "Ünï: x": "Ünï: x", "S": "S",
	} {
		if got := NormalizeSubject(in); got != want {
			t.Errorf("NormalizeSubject(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMailThread(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	root := mailMsg(1, fInbox, "<r1@x>", "Project kickoff", 6)
	mid := mailMsg(2, fInbox, "<r2@x>", "Re: Project kickoff", 5)
	mid.InReplyTo = "<r1@x>"
	leaf := mailMsg(3, fSent, "<r3@x>", "RE: RE: Project kickoff", 4)
	leaf.InReplyTo = "<r2@x>"
	dupOfMid := mailMsg(4, fJunk, "<r2@x>", "Re: Project kickoff", 5)
	// No links: same subject with a shared participant, same subject with none, other subject.
	lone := mailMsg(10, fInbox, "<l1@x>", "Offsite plans", 3)
	sameShared := mailMsg(11, fInbox, "<l2@x>", "Fwd: Offsite plans", 2)
	sameShared.SenderAddress = "other@example.test"
	sameStranger := mailMsg(12, fInbox, "<l3@x>", "Offsite plans", 1)
	sameStranger.SenderAddress = "stranger@example.test"
	sameStranger.Recipients = []outlookmail.Recipient{{Name: "Nobody", Address: "nobody@example.test"}, {Name: "No address"}}
	empty := mailMsg(13, fInbox, "<e1@x>", "Re:", 1)
	// A reply whose parent is not held is no link.
	orphan := mailMsg(14, fInbox, "<o1@x>", "Orphan subject", 1)
	orphan.InReplyTo = "<missing@x>"
	noID := mailMsg(15, fInbox, "", "No id", 1)
	mustCommitMail(t, s, mailBatch(mailT0, root, mid, leaf, dupOfMid, lone, sameShared, sameStranger, empty, orphan, noID))
	keys := func(rows []MailRow) []uint32 {
		out := []uint32{}
		for _, r := range rows {
			out = append(out, r.DetailKey)
		}
		return out
	}
	for _, seed := range []uint32{1, 2, 3, 4} {
		rows, grouping, _, err := s.MailThread(ctx, mailAcct, seed)
		if err != nil || grouping != "reply_chain" || len(rows) != 4 || rows[0].DetailKey != 1 {
			t.Fatalf("seed %d: %v %q %v", seed, keys(rows), grouping, err)
		}
	}
	rows, grouping, trunc, err := s.MailThread(ctx, mailAcct, 10)
	if err != nil || grouping != "subject" || trunc || len(keys(rows)) != 2 || rows[0].DetailKey != 10 || rows[1].DetailKey != 11 {
		t.Fatalf("subject fallback: %v %q %v", keys(rows), grouping, err)
	}
	for _, seed := range []uint32{13, 14, 15} {
		rows, grouping, _, err = s.MailThread(ctx, mailAcct, seed)
		if err != nil || grouping != "subject" || len(rows) < 1 || rows[0].DetailKey != seed && seed == 13 {
			t.Fatalf("seed %d: %v %q %v", seed, keys(rows), grouping, err)
		}
	}
	if rows, _, _, _ := s.MailThread(ctx, mailAcct, 13); len(rows) != 1 {
		t.Fatalf("empty subject_norm: %d rows", len(rows))
	}
	if _, _, _, err := s.MailThread(ctx, mailAcct, 999); !errors.Is(err, ErrMailNotFound) {
		t.Fatalf("missing seed: %v", err)
	}
	people := MailParticipants(must(s.MailList(ctx, MailFilter{Folder: "inbox", Limit: 2})))
	if len(people) < 2 {
		t.Fatalf("participants: %v", people)
	}
	if got := MailParticipants([]MailRow{{}}); len(got) != 0 {
		t.Fatalf("nameless participant: %v", got)
	}
}

// longChain is a reply chain of n messages, each answering the one before.
func longChain(n int, subject string) []outlookmail.Message {
	var msgs []outlookmail.Message
	for i := 1; i <= n; i++ {
		m := mailMsg(uint32(i), fInbox, fmt.Sprintf("<c%d@x>", i), subject, 1)
		if i > 1 {
			m.InReplyTo = msgs[i-2].MessageID
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// A thread is cut at the cap, and truncated says so only when a message was left out.
func TestMailThreadCap(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		n         int
		truncated bool
	}{{mailThreadCap, false}, {mailThreadCap + 1, true}, {mailThreadCap + 30, true}} {
		s := newStore(t)
		mustCommitMail(t, s, mailBatch(mailT0, longChain(c.n, "Long thread")...))
		rows, grouping, truncated, err := s.MailThread(ctx, mailAcct, 1)
		if err != nil || grouping != "reply_chain" || len(rows) != min(c.n, mailThreadCap) || truncated != c.truncated {
			t.Fatalf("chain of %d: %d rows %q truncated=%v %v", c.n, len(rows), grouping, truncated, err)
		}
		if !slices.ContainsFunc(rows, func(r MailRow) bool { return r.DetailKey == 1 }) {
			t.Fatalf("chain of %d: the seed was cut", c.n)
		}
	}
}

// A subject group is cut at the cap the same way, and keeps its seed.
func TestMailThreadSubjectCap(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		n         uint32
		truncated bool
	}{{mailThreadCap, false}, {mailThreadCap + 1, true}} {
		s := newStore(t)
		var msgs []outlookmail.Message
		for i := uint32(1); i <= c.n; i++ {
			msgs = append(msgs, mailMsg(i, fInbox, fmt.Sprintf("<s%d@x>", i), "Standup", int(i)))
		}
		mustCommitMail(t, s, mailBatch(mailT0, msgs...))
		rows, grouping, truncated, err := s.MailThread(ctx, mailAcct, c.n) // the oldest message
		if err != nil || grouping != "subject" || len(rows) != min(int(c.n), mailThreadCap) || truncated != c.truncated {
			t.Fatalf("group of %d: %d rows %q truncated=%v %v", c.n, len(rows), grouping, truncated, err)
		}
		if rows[0].DetailKey != c.n {
			t.Fatalf("group of %d: the seed was cut, first row %d", c.n, rows[0].DetailKey)
		}
	}
}

func TestMailFoldersAndCoverage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if rows, err := s.MailFolders(ctx); err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty folders: %v %v", rows, err)
	}
	read := false
	a := mailMsg(1, fInbox, "<1@x>", "A", 5)
	b := mailMsg(2, fInbox, "<2@x>", "B", 1)
	b.Unread, b.ReadState = &read, "read"
	c := mailMsg(3, fSent, "<3@x>", "C", 3)
	mustCommitMail(t, s, mailBatch(mailT0, a, b, c))
	folders, err := s.MailFolders(ctx)
	if err != nil || len(folders) != 5 {
		t.Fatalf("folders: %v %v", folders, err)
	}
	if f := folders[0]; f.Kind != "inbox" || f.Messages != 2 || f.Unread != 1 || !f.OldestAt.Equal(a.Received) || !f.NewestAt.Equal(b.Received) || !f.ReadAt.Equal(mailT0) {
		t.Fatalf("inbox row: %+v", f)
	}
	var kinds []string
	for _, f := range folders {
		kinds = append(kinds, f.Kind)
	}
	if strings.Join(kinds, ",") != "inbox,to_me,sent,junk,deleted" {
		t.Fatalf("order: %v", kinds)
	}
	cov, err := s.MailCoverage(ctx)
	if err != nil || len(cov) != 2 || cov[0].Folder != "Inbox" || cov[0].Count != 2 || cov[1].Kind != "sent" || !cov[0].OldestAt.Equal(a.Received) {
		t.Fatalf("coverage: %+v %v", cov, err)
	}
	// Equal-kind folders order by name, then key; an unknown kind sorts last.
	if _, err := s.db.Exec(`insert into mail_folders(account, folder_key, name, kind) values(?,20,'b','other'),(?,21,'A','other'),(?,22,'A','other'),('outlook/aaa',1,'x','inbox')`, mailAcct, mailAcct, mailAcct); err != nil {
		t.Fatal(err)
	}
	folders, _ = s.MailFolders(ctx)
	if len(folders) != 9 || folders[0].Account != "outlook/aaa" || folders[6].FolderKey != 21 || folders[7].FolderKey != 22 || folders[8].Name != "b" {
		t.Fatalf("tie order: %+v", folders)
	}
}

func TestMailQueriesOnAReadOnlyArchive(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Readable", 1)))
	_ = s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	if rows, err := ro.MailList(ctx, MailFilter{}); err != nil || len(rows) != 1 {
		t.Fatalf("read-only list: %v %v", rows, err)
	}
}

func TestMailRowsWithoutAFolderRow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Orphan folder", 1)))
	if _, err := s.db.Exec(`delete from mail_folders`); err != nil {
		t.Fatal(err)
	}
	rows, err := s.MailList(ctx, MailFilter{})
	if err != nil || len(rows) != 1 || rows[0].Folder != "" || rows[0].FolderKind != "unknown" {
		t.Fatalf("no folder row: %+v %v", rows, err)
	}
	// An unlabelled message that moves in the same read can still be re-keyed or compared.
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), mailMsg(1, fInbox, "<1@x>", "Orphan folder", 1)))
}

func TestMailPassesThroughAnUntrustedBatch(t *testing.T) {
	s := newStore(t)
	b := mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "One", 1))
	b.Trusted = false
	if r := mustCommitMail(t, s, b); r != (MailResult{Added: 1}) {
		t.Fatalf("untrusted add: %+v", r)
	}
}

func TestCommitMailFillsRecipientsLate(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := mailMsg(100, fInbox, "<a@x>", "Orphaned at first", 2)
	m.Recipients = nil // the recipient objects were orphaned in the first read
	mustCommitMail(t, s, mailBatch(mailT0, m))
	if g, _ := s.MailGet(ctx, mailAcct, 100); len(g.Recipients) != 0 {
		t.Fatalf("recipients: %v", g.Recipients)
	}
	m.Recipients = []outlookmail.Recipient{{Name: "Bob", Address: "bob@example.test"}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), m)); r != (MailResult{Updated: 1}) {
		t.Fatalf("late recipients: %+v", r)
	}
	if g, _ := s.MailGet(ctx, mailAcct, 100); len(g.Recipients) != 1 {
		t.Fatalf("recipients after: %v", g.Recipients)
	}
	// Once held they are immutable.
	m.Recipients = []outlookmail.Recipient{{Name: "Eve"}, {Name: "Bob"}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), m)); r != (MailResult{}) {
		t.Fatalf("held recipients: %+v", r)
	}
}

func TestCommitMailRaisesHasAttachments(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	m := mailMsg(100, fInbox, "<a@x>", "No attachment yet", 2)
	mustCommitMail(t, s, mailBatch(mailT0, m))
	m.Attachments = []outlookmail.Attachment{{Key: 1, Name: "late.txt"}}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), m)); r != (MailResult{Updated: 1}) {
		t.Fatalf("raise: %+v", r)
	}
	if g, _ := s.MailGet(ctx, mailAcct, 100); !g.HasAttachments || len(g.Attachments) != 1 {
		t.Fatalf("after raise: %+v", g)
	}
}
