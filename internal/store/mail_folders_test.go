package store

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// A message whose folder object the store does not hold is archived with its folder key, shows
// the kind unknown with no name, and is matched by --folder unknown and by nothing else.
func TestMailInAFolderTheStoreDoesNotHold(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	inbox := mailMsg(1, fInbox, "<1@x>", "Known folder", 5)
	lost := mailMsg(2, 999, "<2@x>", "Unknown folder", 4)
	lost.Folder = outlookmail.Folder{Key: 999, Kind: outlookmail.KindUnknown}
	b := mailBatch(mailT0, inbox, lost)
	b.Result.Coverage = append(b.Result.Coverage, outlookmail.Coverage{FolderKey: 999, Oldest: lost.Received, Newest: lost.Received, Count: 1})
	mustCommitMail(t, s, b)

	m, err := s.MailGet(ctx, mailAcct, 2)
	if err != nil || m.FolderKey != 999 || m.Folder != "" || m.FolderKind != "unknown" {
		t.Fatalf("%+v %v", m, err)
	}
	rows, err := s.MailList(ctx, MailFilter{Folder: "unknown"})
	if err != nil || len(rows) != 1 || rows[0].DetailKey != 2 {
		t.Fatalf("--folder unknown: %+v %v", rows, err)
	}
	if rows, err = s.MailList(ctx, MailFilter{Folder: "inbox"}); err != nil || len(rows) != 1 || rows[0].DetailKey != 1 {
		t.Fatalf("--folder inbox: %+v %v", rows, err)
	}
	if _, err = s.MailList(ctx, MailFilter{Folder: "no such folder"}); !errors.Is(err, ErrUnknownMailFolder) {
		t.Fatalf("%v", err)
	}
	cov, err := s.MailCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, c := range cov {
		kinds = append(kinds, c.Kind)
	}
	if len(cov) != 2 || kinds[1] != "unknown" && kinds[0] != "unknown" {
		t.Fatalf("coverage kinds %v", kinds)
	}
	// The folder turns up in a later read: the message moves into it.
	found := mailMsg(2, fDeleted, "<2@x>", "Unknown folder", 4)
	mustCommitMail(t, s, mailBatch(hr(1), inbox, found))
	if m, _ = s.MailGet(ctx, mailAcct, 2); m.FolderKind != "deleted" {
		t.Fatalf("%+v", m)
	}
}

// watchWrites counts every write to the mail tables (and the text index's shadow tables) in the
// table named writes, so a test can tell that a read wrote nothing.
func watchWrites(t *testing.T, s *Store) {
	t.Helper()
	ddl := "create table writes(n integer);"
	for _, tbl := range []string{"mail_messages", "mail_folders", "mail_coverage", "mail_recipients", "mail_attachments", "mail_absent", "mail_fts_content", "mail_fts_docsize", "mail_fts_data"} {
		for _, ev := range []string{"insert", "update", "delete"} {
			ddl += "create trigger w_" + tbl + "_" + ev + " after " + ev + " on " + tbl + " begin insert into writes values(1); end;"
		}
	}
	if _, err := s.db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
}

// Reading an unchanged mailbox again writes no row, no folder or coverage line and nothing of the
// text index, and does no text work: the cost of a forced re-read is the read itself.
func TestUnchangedReReadWritesNothingAndDerivesNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	batch := func(n int) MailBatch {
		a := mailMsg(1, fInbox, "<1@x>", "One", 5)
		a.Attachments = []outlookmail.Attachment{{Key: 1, Name: "a.txt"}}
		return mailBatch(hr(n), a, mailMsg(2, fInbox, "<2@x>", "Two", 4))
	}
	mustCommitMail(t, s, batch(0))
	watchWrites(t, s)
	calls := 0
	old := htmlText
	htmlText = func(b []byte) string { calls++; return old(b) }
	t.Cleanup(func() { htmlText = old })
	r, err := s.CommitMail(ctx, batch(1))
	if err != nil || r != (MailResult{}) {
		t.Fatalf("%+v %v", r, err)
	}
	if n := rowCount(t, s, `select count(*) from writes`); calls != 0 || n != 0 {
		t.Fatalf("%d text conversions, %d writes", calls, n)
	}
	// A change of coverage or of a folder name is still written.
	b := batch(2)
	b.Result.Folders[0].Name = "Renamed"
	b.Result.Coverage[0].Count++
	mustCommitMail(t, s, b)
	if got := rowCount(t, s, `select count(*) from mail_folders where name='Renamed'`); got != 1 {
		t.Fatal("a renamed folder was not written")
	}
	if got := rowCount(t, s, `select count(*) from mail_coverage where count=3`); got != 1 {
		t.Fatal("a changed coverage line was not written")
	}
}

// Each kind of change to a stored message is one update, and nothing else is.
func TestEachKindOfChangeIsOneUpdate(t *testing.T) {
	read := false
	pendingBody := func(m *outlookmail.Message) { m.Body = outlookmail.Body{State: outlookmail.BodyNotRead} }
	cases := []struct {
		name  string
		first func(*outlookmail.Message)
		then  func(*outlookmail.Message)
	}{
		{"read state", nil, func(m *outlookmail.Message) { m.Unread, m.ReadState = &read, "read" }},
		{"flag", nil, func(m *outlookmail.Message) { m.Flag = "flagged" }},
		{"importance", nil, func(m *outlookmail.Message) { m.Importance = "high" }},
		{"to me", nil, func(m *outlookmail.Message) { m.ToMe = true }},
		{"folder move", nil, func(m *outlookmail.Message) {
			m.FolderKey, m.Folder = fDeleted, outlookmail.Folder{Key: fDeleted, Kind: "deleted"}
		}},
		{"late body", pendingBody, func(m *outlookmail.Message) {
			m.Body = outlookmail.Body{State: outlookmail.BodyFile, HTML: []byte("<p>late</p>")}
		}},
		{"attachment raised", nil, func(m *outlookmail.Message) { m.Attachments = []outlookmail.Attachment{{Key: 7, Name: "late.txt"}} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStore(t)
			mk := func(change func(*outlookmail.Message)) outlookmail.Message {
				m := mailMsg(1, fInbox, "<1@x>", "One", 5)
				if change != nil {
					change(&m)
				}
				return m
			}
			mustCommitMail(t, s, mailBatch(hr(0), mk(c.first), mailMsg(2, fInbox, "<2@x>", "Two", 4)))
			if r := mustCommitMail(t, s, mailBatch(hr(1), mk(c.first), mailMsg(2, fInbox, "<2@x>", "Two", 4))); r != (MailResult{}) {
				t.Fatalf("unchanged read: %+v", r)
			}
			changed := mk(func(m *outlookmail.Message) {
				if c.first != nil {
					c.first(m)
				}
				c.then(m)
			})
			if r := mustCommitMail(t, s, mailBatch(hr(2), changed, mailMsg(2, fInbox, "<2@x>", "Two", 4))); r != (MailResult{Updated: 1}) {
				t.Fatalf("one change: %+v", r)
			}
			if r := mustCommitMail(t, s, mailBatch(hr(3), changed, mailMsg(2, fInbox, "<2@x>", "Two", 4))); r != (MailResult{}) {
				t.Fatalf("read again: %+v", r)
			}
		})
	}
}

// Messages in a folder the store does not hold are counted in a bucket of kind unknown, with the
// unread ones; there is none while no message is in that state.
func TestMailFoldersHasAnUnknownBucket(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<1@x>", "Known", 5)))
	rows, err := s.MailFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Kind == "unknown" {
			t.Fatalf("an unknown bucket without messages: %+v", r)
		}
	}
	read := false
	a, b, c := mailMsg(2, 999, "<2@x>", "Lost one", 4), mailMsg(3, 998, "<3@x>", "Lost two", 3), mailMsg(4, 999, "<4@x>", "Lost three", 2)
	b.Unread, b.ReadState = &read, "read"
	mustCommitMail(t, s, mailBatch(hr(1), mailMsg(1, fInbox, "<1@x>", "Known", 5), a, b, c))
	if rows, err = s.MailFolders(ctx); err != nil {
		t.Fatal(err)
	}
	last := rows[len(rows)-1]
	if last.Kind != "unknown" || last.Messages != 3 || last.Unread != 2 || last.Account != mailAcct || last.Name != "" {
		t.Fatalf("%+v", rows)
	}
}
