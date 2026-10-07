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

// Reading an unchanged mailbox again writes no row and does no text work: the cost of a forced
// re-read is the read itself.
func TestUnchangedReReadWritesNothingAndDerivesNothing(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	batch := func(n int) MailBatch {
		return mailBatch(hr(n), mailMsg(1, fInbox, "<1@x>", "One", 5), mailMsg(2, fInbox, "<2@x>", "Two", 4))
	}
	mustCommitMail(t, s, batch(0))
	if _, err := s.db.Exec(`create table writes(n integer); create trigger w after update on mail_messages begin insert into writes values(1); end`); err != nil {
		t.Fatal(err)
	}
	calls := 0
	old := htmlText
	htmlText = func(b []byte) string { calls++; return old(b) }
	t.Cleanup(func() { htmlText = old })
	r, err := s.CommitMail(ctx, batch(1))
	if err != nil || r != (MailResult{}) {
		t.Fatalf("%+v %v", r, err)
	}
	if calls != 0 || rowCount(t, s, `select count(*) from writes`) != 0 {
		t.Fatalf("%d text conversions, %d row updates", calls, rowCount(t, s, `select count(*) from writes`))
	}
}
