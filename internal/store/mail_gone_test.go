package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// hr returns the read time n hours after mailT0; every read is of a newer copy of the store.
func hr(n int) time.Time { return mailT0.Add(time.Duration(n) * time.Hour) }

func goneState(t *testing.T, s *Store, dk uint32) (gone, evicted bool, misses int) {
	t.Helper()
	m, err := s.MailGet(context.Background(), mailAcct, dk)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`select coalesce((select misses from mail_absent where account=? and detail_key=?),0)`, mailAcct, dk).Scan(&misses); err != nil {
		t.Fatal(err)
	}
	return !m.GoneAt.IsZero(), !m.EvictedAt.IsZero(), misses
}

func TestGoneNeedsTwoTrustedReadsOfDifferentCopies(t *testing.T) {
	s := newStore(t)
	anchor := mailMsg(1, fInbox, "<1@x>", "Anchor", 30)
	target := mailMsg(2, fInbox, "<2@x>", "Target", 10)
	mustCommitMail(t, s, mailBatch(hr(0), anchor, target))
	// First miss: remembered, not gone.
	if r := mustCommitMail(t, s, mailBatch(hr(1), anchor)); r != (MailResult{}) {
		t.Fatalf("first miss: %+v", r)
	}
	if g, e, n := goneState(t, s, 2); g || e || n != 1 {
		t.Fatalf("after one miss: gone=%v evicted=%v misses=%d", g, e, n)
	}
	// The same copy again (same FreshAt) is not a second look.
	same := mailBatch(hr(2), anchor)
	same.FreshAt = hr(1)
	if r := mustCommitMail(t, s, same); r != (MailResult{}) {
		t.Fatalf("same copy: %+v", r)
	}
	if g, _, n := goneState(t, s, 2); g || n != 1 {
		t.Fatalf("same copy: gone=%v misses=%d", g, n)
	}
	// An older copy is not a second look either.
	older := mailBatch(hr(3), anchor)
	older.FreshAt = hr(0)
	mustCommitMail(t, s, older)
	if g, _, _ := goneState(t, s, 2); g {
		t.Fatal("older copy confirmed an absence")
	}
	// A later copy that also misses it: gone, because it was received inside the covered range.
	if r := mustCommitMail(t, s, mailBatch(hr(4), anchor)); r != (MailResult{Gone: 1}) {
		t.Fatalf("second miss: %+v", r)
	}
	if g, e, n := goneState(t, s, 2); !g || e || n != 2 {
		t.Fatalf("after two misses: gone=%v evicted=%v misses=%d", g, e, n)
	}
	// A gone message is not judged again, and the default listing leaves it out.
	if r := mustCommitMail(t, s, mailBatch(hr(5), anchor)); r != (MailResult{}) {
		t.Fatalf("third read: %+v", r)
	}
	if rows, _ := s.MailList(context.Background(), MailFilter{}); len(rows) != 1 {
		t.Fatalf("default list holds %d rows", len(rows))
	}
	if rows, _ := s.MailList(context.Background(), MailFilter{IncludeGone: true}); len(rows) != 2 {
		t.Fatalf("include gone: %d rows", len(rows))
	}
	// It comes back: both marks and the absence row clear, and that is an update.
	if r := mustCommitMail(t, s, mailBatch(hr(6), anchor, target)); r != (MailResult{Updated: 1}) {
		t.Fatalf("reappearance: %+v", r)
	}
	if g, e, n := goneState(t, s, 2); g || e || n != 0 {
		t.Fatalf("after reappearance: gone=%v evicted=%v misses=%d", g, e, n)
	}
}

func TestEvictedWhenOlderThanTheCoveredRange(t *testing.T) {
	s := newStore(t)
	recent := mailMsg(1, fInbox, "<1@x>", "Recent", 5)
	old := mailMsg(2, fInbox, "<2@x>", "Old", 40)
	mustCommitMail(t, s, mailBatch(hr(0), recent, old))
	mustCommitMail(t, s, mailBatch(hr(1), recent))
	if r := mustCommitMail(t, s, mailBatch(hr(2), recent)); r != (MailResult{Evicted: 1}) {
		t.Fatalf("evicted: %+v", r)
	}
	if g, e, _ := goneState(t, s, 2); g || !e {
		t.Fatalf("gone=%v evicted=%v", g, e)
	}
	ctx := context.Background()
	if rows, _ := s.MailList(ctx, MailFilter{}); len(rows) != 1 {
		t.Fatalf("default list: %d", len(rows))
	}
	if rows, _ := s.MailList(ctx, MailFilter{IncludeEvicted: true}); len(rows) != 2 {
		t.Fatalf("include evicted: %d", len(rows))
	}
	// It reappears (the cache reached back again).
	if r := mustCommitMail(t, s, mailBatch(hr(3), recent, old)); r != (MailResult{Updated: 1}) {
		t.Fatalf("reappearance: %+v", r)
	}
	if _, e, _ := goneState(t, s, 2); e {
		t.Fatal("evicted mark kept")
	}
}

func TestSmallFoldersUseTheCombinedRange(t *testing.T) {
	s := newStore(t)
	inbox := mailMsg(1, fInbox, "<1@x>", "Inbox anchor", 20)
	sent := mailMsg(2, fSent, "<2@x>", "Sent anchor", 30)
	// Deleted Items has its own read range (5 days), but the combined range reaches back 30 days.
	deletedKeep := mailMsg(3, fDeleted, "<3@x>", "Deleted keep", 5)
	deletedInside := mailMsg(4, fDeleted, "<4@x>", "Deleted inside", 25)
	deletedOlder := mailMsg(5, fDeleted, "<5@x>", "Deleted older", 45)
	junk := mailMsg(6, fJunk, "<6@x>", "Junk inside", 10)
	mustCommitMail(t, s, mailBatch(hr(0), inbox, sent, deletedKeep, deletedInside, deletedOlder, junk))
	keep := []outlookmail.Message{inbox, sent, deletedKeep}
	mustCommitMail(t, s, mailBatch(hr(1), keep...))
	r := mustCommitMail(t, s, mailBatch(hr(2), keep...))
	if r != (MailResult{Gone: 2, Evicted: 1}) {
		t.Fatalf("combined range: %+v", r)
	}
	for dk, want := range map[uint32]string{4: "gone", 5: "evicted", 6: "gone"} {
		g, e, _ := goneState(t, s, dk)
		if got := map[bool]string{true: "x"}[g || e]; got == "" {
			t.Fatalf("message %d unmarked", dk)
		}
		if (want == "gone") != g || (want == "evicted") != e {
			t.Fatalf("message %d: gone=%v evicted=%v, want %s", dk, g, e, want)
		}
	}
}

func TestInboxUsesItsOwnRange(t *testing.T) {
	s := newStore(t)
	inbox := mailMsg(1, fInbox, "<1@x>", "Inbox anchor", 5)
	sent := mailMsg(2, fSent, "<2@x>", "Sent anchor", 60)
	lost := mailMsg(3, fInbox, "<3@x>", "Older than the inbox range", 20)
	mustCommitMail(t, s, mailBatch(hr(0), inbox, sent, lost))
	mustCommitMail(t, s, mailBatch(hr(1), inbox, sent))
	if r := mustCommitMail(t, s, mailBatch(hr(2), inbox, sent)); r != (MailResult{Evicted: 1}) {
		t.Fatalf("own range: %+v", r)
	}
}

func TestNothingIsMarkedWithoutARangeOrATime(t *testing.T) {
	s := newStore(t)
	noTime := mailMsg(1, fInbox, "<1@x>", "No time", 5)
	noTime.Received = time.Time{}
	anchor := mailMsg(2, fInbox, "<2@x>", "Anchor", 20)
	mustCommitMail(t, s, mailBatch(hr(0), noTime, anchor))
	mustCommitMail(t, s, mailBatch(hr(1), anchor))
	if r := mustCommitMail(t, s, mailBatch(hr(2), anchor)); r != (MailResult{}) {
		t.Fatalf("no received time: %+v", r)
	}
	// A read with no coverage at all (an empty mailbox) gives no range to judge by.
	empty := mailBatch(hr(3))
	mustCommitMail(t, s, empty)
	if r := mustCommitMail(t, s, mailBatch(hr(4))); r != (MailResult{}) {
		t.Fatalf("no coverage: %+v", r)
	}
	if g, e, _ := goneState(t, s, 2); g || e {
		t.Fatalf("marked without a range: gone=%v evicted=%v", g, e)
	}
	if n := rowCount(t, s, `select count(*) from mail_coverage`); n != 0 {
		t.Fatalf("coverage rows kept after an empty trusted read: %d", n)
	}
}

func TestAnUntrustedReadNeverMarks(t *testing.T) {
	s := newStore(t)
	anchor := mailMsg(1, fInbox, "<1@x>", "Anchor", 30)
	target := mailMsg(2, fInbox, "<2@x>", "Target", 10)
	mustCommitMail(t, s, mailBatch(hr(0), anchor, target))
	for i := 1; i <= 3; i++ {
		b := mailBatch(hr(i), anchor)
		b.Trusted = false
		mustCommitMail(t, s, b)
	}
	if g, e, n := goneState(t, s, 2); g || e || n != 0 {
		t.Fatalf("untrusted: gone=%v evicted=%v misses=%d", g, e, n)
	}
	// Its coverage still shows what it read, and a trusted empty read later is the first miss.
	if n := rowCount(t, s, `select count(*) from mail_coverage`); n != 1 {
		t.Fatalf("coverage rows: %d", n)
	}
}

func TestAReadWithNoAgeNeverConfirms(t *testing.T) {
	s := newStore(t)
	anchor := mailMsg(1, fInbox, "<1@x>", "Anchor", 30)
	target := mailMsg(2, fInbox, "<2@x>", "Target", 10)
	mustCommitMail(t, s, mailBatch(hr(0), anchor, target))
	for i := 1; i <= 3; i++ {
		b := mailBatch(hr(i), anchor)
		b.FreshAt = time.Time{}
		mustCommitMail(t, s, b)
	}
	if g, _, n := goneState(t, s, 2); g || n != 1 {
		t.Fatalf("no age: gone=%v misses=%d", g, n)
	}
}

func manyMail(n int) []outlookmail.Message {
	out := make([]outlookmail.Message, n)
	for i := range out {
		out[i] = mailMsg(uint32(1000+i), fInbox, "<m"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))+"@x>", "Bulk", 1+i%20)
	}
	return out
}

func TestAReadThatLostTooMuchMarksNothing(t *testing.T) {
	s := newStore(t)
	all := manyMail(100)
	mustCommitMail(t, s, mailBatch(hr(0), all...))
	// 25 of 100 missing: more than 20 and more than 10 percent.
	if r := mustCommitMail(t, s, mailBatch(hr(1), all[25:]...)); r != (MailResult{Withheld: 25}) {
		t.Fatalf("withheld: %+v", r)
	}
	if n := rowCount(t, s, `select count(*) from mail_absent`); n != 0 {
		t.Fatalf("absence recorded from a withheld read: %d", n)
	}
	// Exactly 20 missing is not more than the minimum: judged.
	if r := mustCommitMail(t, s, mailBatch(hr(2), all[20:]...)); r.Withheld != 0 {
		t.Fatalf("20 missing: %+v", r)
	}
	if n := rowCount(t, s, `select count(*) from mail_absent`); n != 20 {
		t.Fatalf("absence rows: %d", n)
	}
}

func TestManyMissingButUnderTenPercentIsJudged(t *testing.T) {
	s := newStore(t)
	all := manyMail(300)
	mustCommitMail(t, s, mailBatch(hr(0), all...))
	if r := mustCommitMail(t, s, mailBatch(hr(1), all[25:]...)); r.Withheld != 0 {
		t.Fatalf("25 of 300: %+v", r)
	}
	if n := rowCount(t, s, `select count(*) from mail_absent`); n != 25 {
		t.Fatalf("absence rows: %d", n)
	}
}

func TestAChangedRootMakesTheReadUntrusted(t *testing.T) {
	s := newStore(t)
	anchor := mailMsg(1, fInbox, "<1@x>", "Anchor", 30)
	target := mailMsg(2, fInbox, "<2@x>", "Target", 10)
	first := mailBatch(hr(0), anchor, target)
	first.Result.RootKey = 1
	mustCommitMail(t, s, first)
	// The root moved: that read confirms and records nothing.
	b := mailBatch(hr(1), anchor)
	b.Result.RootKey = 7
	if r := mustCommitMail(t, s, b); r != (MailResult{}) {
		t.Fatalf("changed root: %+v", r)
	}
	if g, _, n := goneState(t, s, 2); g || n != 0 {
		t.Fatalf("changed root: gone=%v misses=%d", g, n)
	}
	if n := rowCount(t, s, `select count(*) from meta where key=? and value='7'`, mailRootKey+mailAcct); n != 1 {
		t.Fatal("root not stored")
	}
	// With the root steady again, two misses confirm.
	for i := 2; i <= 3; i++ {
		b := mailBatch(hr(i), anchor)
		b.Result.RootKey = 7
		mustCommitMail(t, s, b)
	}
	if g, _, _ := goneState(t, s, 2); !g {
		t.Fatal("steady root did not confirm")
	}
}

func TestEvictionCandidatesDoNotCountTowardWithholding(t *testing.T) {
	s := newStore(t)
	all := manyMail(100)
	for i := range all { // days 1 to 20 old
		all[i].Received = mailT0.AddDate(0, 0, -(1 + i%20))
	}
	// 30 messages older than every other message in the folder: the cache dropped them.
	for i := 0; i < 30; i++ {
		all[i].Received = mailT0.AddDate(0, 0, -(100 + i))
	}
	mustCommitMail(t, s, mailBatch(hr(0), all...))
	mustCommitMail(t, s, mailBatch(hr(1), all[30:]...))
	if r := mustCommitMail(t, s, mailBatch(hr(2), all[30:]...)); r != (MailResult{Evicted: 30}) {
		t.Fatalf("evicted: %+v", r)
	}
}
