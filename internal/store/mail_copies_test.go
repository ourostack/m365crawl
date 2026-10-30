package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

// Two folder sets of one account, as Outlook for Mac leaves them when it rebuilds the account in
// its store: each set hangs from its own account root and has its own Inbox, Sent Items, To Me and
// Deleted Items, with new keys for the same messages.
const (
	rootA, rootB, rootC                 = 100, 200, 300
	aInbox, aSent, aToMe, aDeleted      = 101, 102, 103, 104
	bInbox, bSent, bToMe, bDeleted, bPr = 201, 202, 203, 204, 205
)

func setFolders(root uint32) []outlookmail.Folder {
	k := func(n uint32) uint32 { return root + n }
	return []outlookmail.Folder{
		{Key: k(1), Parent: root, Name: "Inbox", Kind: "inbox"},
		{Key: k(2), Parent: root, Name: "Sent Items", Kind: "sent"},
		{Key: k(3), Parent: root, Name: "To Me", Kind: "to_me", Generic: true},
		{Key: k(4), Parent: root, Name: "Deleted Items", Kind: "deleted"},
		{Key: k(5), Parent: k(1), Name: "Projects", Kind: "other", Generic: true},
	}
}

func setMsg(dk, folder uint32, msgID string, daysAgo int) outlookmail.Message {
	m := mailMsg(dk, folder, msgID, "copy "+msgID, daysAgo)
	for _, f := range append(append(setFolders(rootA), setFolders(rootB)...), setFolders(rootC)...) {
		if f.Key == folder {
			m.Folder = f
		}
	}
	return m
}

// setBatch is one trusted read of the folder set under root.
func setBatch(read time.Time, root uint32, msgs ...outlookmail.Message) MailBatch {
	b := mailBatch(read, msgs...)
	b.Result.RootKey = root
	b.Result.Folders = setFolders(root)
	b.Result.Coverage = nil
	cov := map[uint32]*outlookmail.Coverage{}
	for _, m := range msgs {
		c := cov[m.FolderKey]
		if c == nil {
			c = &outlookmail.Coverage{FolderKey: m.FolderKey, Oldest: m.Received, Newest: m.Received}
			cov[m.FolderKey] = c
		}
		c.Count++
		c.Oldest, c.Newest = minTime(c.Oldest, m.Received), maxTime(c.Newest, m.Received)
	}
	for _, f := range b.Result.Folders {
		if c := cov[f.Key]; c != nil {
			b.Result.Coverage = append(b.Result.Coverage, *c)
		}
	}
	return b
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// commitUnmappedThenMapped reads the set under root twice: first with its folders unmapped, so its
// messages cannot be matched to archived ones, then mapped.
func commitUnmappedThenMapped(t *testing.T, s *Store, hour int, root uint32, msgs ...outlookmail.Message) {
	t.Helper()
	unmapped := make([]outlookmail.Message, len(msgs))
	for i, m := range msgs {
		m.Folder = outlookmail.Folder{Key: m.FolderKey, Kind: outlookmail.KindUnknown}
		unmapped[i] = m
	}
	early := setBatch(hr(hour), root, unmapped...)
	early.Result.Folders = nil
	mustCommitMail(t, s, early)
	mustCommitMail(t, s, setBatch(hr(hour+1), root, msgs...))
}

// seedTwoSets reproduces the shape seen on a real archive: set A is read first; set B is then
// read while its folders are still unmapped (kind unknown), so its copies are archived as new
// messages instead of being re-keyed; a later read maps B's folders and the copies land in B's
// Sent Items beside A's.
func seedTwoSets(t *testing.T, s *Store, a []outlookmail.Message, b []outlookmail.Message) {
	t.Helper()
	mustCommitMail(t, s, setBatch(hr(1), rootA, a...))
	commitUnmappedThenMapped(t, s, 2, rootB, b...)
}

func listKeys(t *testing.T, s *Store, f MailFilter) []uint32 {
	t.Helper()
	rows, err := s.MailList(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	var out []uint32
	for _, r := range rows {
		out = append(out, r.DetailKey)
	}
	return out
}

func sameKeys(got []uint32, want ...uint32) bool { return slices.Equal(got, want) }

// Two copies of Sent Items holding the same Message-ID list and count once: the copy in the
// folder set the latest read used stands for the message, and the older copy is still archived,
// under its own id, with copy_of naming the newer one.
func TestMailCopiesAcrossFolderSetsCountOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTwoSets(t, s,
		[]outlookmail.Message{setMsg(1001, aSent, "<X@example.test>", 3)},
		[]outlookmail.Message{setMsg(2001, bSent, " <x@EXAMPLE.test> ", 3)})
	if got := listKeys(t, s, MailFilter{Folder: "sent"}); !sameKeys(got, 2001) {
		t.Fatalf("--folder sent lists %v, want the current copy alone", got)
	}
	if got := listKeys(t, s, MailFilter{}); !sameKeys(got, 2001) {
		t.Fatalf("unfiltered list %v", got)
	}
	rows, err := s.MailList(ctx, MailFilter{Folder: "sent", IncludeCopies: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
	for _, r := range rows {
		switch r.DetailKey {
		case 2001:
			if r.OlderCopy || r.CopyOf != "" {
				t.Fatalf("current copy %+v", r)
			}
		case 1001:
			if !r.OlderCopy || r.CopyOf != mailAcct+":2001" {
				t.Fatalf("older copy %+v", r)
			}
		}
	}
	old, err := s.MailGet(ctx, mailAcct, 1001)
	if err != nil || !old.OlderCopy || old.CopyOf != mailAcct+":2001" || old.GoneAt != (time.Time{}) || old.EvictedAt != (time.Time{}) {
		t.Fatalf("older copy by id: %+v %v", old, err)
	}
	folders, err := s.MailFolders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.Kind != "sent" {
			continue
		}
		switch f.FolderKey {
		case aSent:
			if f.Current || f.Messages != 0 {
				t.Fatalf("older Sent Items %+v: its only message is a copy", f)
			}
		case bSent:
			if !f.Current || f.Messages != 1 || f.Unread != 1 {
				t.Fatalf("current Sent Items %+v", f)
			}
		}
	}
	cov, err := s.MailCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current := map[uint32]bool{}
	for _, c := range cov {
		current[c.FolderKey] = c.Current
	}
	if len(cov) != 2 || current[aSent] || !current[bSent] {
		t.Fatalf("coverage %+v: the older copy keeps its last range, marked not current", cov)
	}
}

// A message that only an older folder set holds is still listed: it is the archive's only record
// of it. It is marked as an older copy and names no newer copy.
func TestMailOlderCopyOnlyMessageStaysListed(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTwoSets(t, s,
		[]outlookmail.Message{setMsg(1001, aSent, "<x@t>", 3), setMsg(1002, aSent, "<only-a@t>", 2)},
		[]outlookmail.Message{setMsg(2001, bSent, "<x@t>", 3)})
	rows, err := s.MailList(ctx, MailFilter{Folder: "sent"})
	if err != nil || len(rows) != 2 || rows[0].DetailKey != 1002 || !rows[0].OlderCopy || rows[0].CopyOf != "" || rows[1].DetailKey != 2001 {
		t.Fatalf("%+v %v", rows, err)
	}
}

// An older row is a copy of a newer row of another set with its Message-ID and folder role
// first (so mail sent to oneself still pairs Sent with Sent), then of any current-set row with
// its Message-ID, whatever the folder: the current set says where the message is now, so a message
// moved after the rebuild is not listed from its old folder. Rows of one set never merge, and a
// row without a Message-ID is never a copy.
func TestMailCopiesPairByRoleThenByCurrentMessageID(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	noID := setMsg(1005, aSent, "", 6)
	noIDb := setMsg(2005, bSent, "", 6)
	seedTwoSets(t, s,
		[]outlookmail.Message{
			setMsg(1001, aInbox, "<moved@t>", 3),
			setMsg(1002, aToMe, "<tome@t>", 4),
			noID,
			setMsg(1006, aProject(), "<user@t>", 7),
			setMsg(1007, aSent, "<self@t>", 5),
			setMsg(1008, aInbox, "<only-a@t>", 8),
		},
		[]outlookmail.Message{
			setMsg(2001, bDeleted, "<moved@t>", 3),
			setMsg(2002, bInbox, "<tome@t>", 4),
			setMsg(2003, bSent, "<self@t>", 5),
			setMsg(2004, bInbox, "<self@t>", 5),
			noIDb,
			setMsg(2006, bPr, "<user@t>", 7),
		})
	got := listKeys(t, s, MailFilter{})
	if !sameKeys(got, 2001, 2002, 2004, 2003, 2005, 1005, 2006, 1008) {
		t.Fatalf("listed %v", got)
	}
	if got := listKeys(t, s, MailFilter{Folder: "inbox"}); !sameKeys(got, 2002, 2004, 1008) {
		t.Fatalf("a message moved out of Inbox after the rebuild is not listed from the older Inbox: %v", got)
	}
	for dk, want := range map[uint32]string{1001: "2001", 1002: "2002", 1006: "2006", 1007: "2003", 1005: "", 1008: ""} {
		r, err := s.MailGet(ctx, mailAcct, dk)
		if want != "" {
			want = mailAcct + ":" + want
		}
		if err != nil || r.CopyOf != want || !r.OlderCopy {
			t.Fatalf("%d: copy_of %q, want %q (%v)", dk, r.CopyOf, want, err)
		}
	}
}

func aProject() uint32 { return rootA + 5 }

// Without a recorded read root, or when no archived folder hangs from the recorded one (an
// archive written before roots were recorded, or another source's folder layout), the archive
// makes no claim: every folder is current and nothing is a copy.
func TestMailCopiesMakeNoClaimWithoutAKnownCurrentSet(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTwoSets(t, s, []outlookmail.Message{setMsg(1001, aSent, "<x@t>", 3)}, []outlookmail.Message{setMsg(2001, bSent, "<x@t>", 3)})
	for _, q := range []string{`update meta set value='999' where key like 'outlook_mail_root:%'`, `delete from meta where key like 'outlook_mail_root:%'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
		rows, err := s.MailList(ctx, MailFilter{Folder: "sent"})
		if err != nil || len(rows) != 2 || rows[0].OlderCopy || rows[1].OlderCopy || rows[0].CopyOf != "" {
			t.Fatalf("%s: %+v %v", q, rows, err)
		}
		folders, err := s.MailFolders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range folders {
			if !f.Current {
				t.Fatalf("%s: %+v", q, f)
			}
		}
		if checks, err := s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "sent"}}, MailCopyRange{}); err != nil || len(checks) != 0 {
			t.Fatalf("%s: %+v %v", q, checks, err)
		}
	}
}

// A trusted read keeps the coverage of an older folder set, which is that copy's last range: the
// read says nothing about a folder it no longer reads. A folder of its own set that it finds empty
// still loses its coverage.
func TestMailCoverageOfAnOlderSetSurvivesATrustedRead(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, setBatch(hr(1), rootA, setMsg(1001, aSent, "<x@t>", 30), setMsg(1002, aInbox, "<y@t>", 20)))
	mustCommitMail(t, s, setBatch(hr(2), rootB, setMsg(2001, bSent, "<z@t>", 3), setMsg(2002, bInbox, "<w@t>", 2)))
	mustCommitMail(t, s, setBatch(hr(3), rootB, setMsg(2001, bSent, "<z@t>", 3))) // trusted, same root; B's Inbox is empty now
	cov, err := s.MailCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	have := map[uint32]bool{}
	for _, c := range cov {
		have[c.FolderKey] = true
	}
	if !have[aSent] || !have[aInbox] || !have[bSent] || have[bInbox] {
		t.Fatalf("coverage %+v", cov)
	}
}

// The gone and evicted rule judges a missing message against the ranges of the read's own folder
// set only: an older set's retained coverage reaching further back must not turn an eviction into
// a deletion.
func TestMailAbsenceIgnoresOlderSetCoverage(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	mustCommitMail(t, s, setBatch(hr(1), rootA, setMsg(1001, aInbox, "<old@t>", 30)))
	keep := setMsg(2001, bInbox, "<keep@t>", 5)
	trash := setMsg(2002, bDeleted, "<trash@t>", 10)
	mustCommitMail(t, s, setBatch(hr(2), rootB, keep, trash)) // root changed: untrusted
	mustCommitMail(t, s, setBatch(hr(3), rootB, keep, trash))
	mustCommitMail(t, s, setBatch(hr(4), rootB, keep))
	mustCommitMail(t, s, setBatch(hr(5), rootB, keep))
	m, err := s.MailGet(ctx, mailAcct, 2002)
	if err != nil || m.EvictedAt.IsZero() || !m.GoneAt.IsZero() {
		t.Fatalf("a Deleted Items message older than the current Inbox range is evicted: %+v %v", m, err)
	}
}

func day(n int) time.Time { return mailT0.AddDate(0, 0, -n) }

func checkOf(t *testing.T, s *Store, kind string, since, until time.Time) MailCopyCheck {
	t.Helper()
	checks, err := s.MailCopyChecks(context.Background(), []MailCopyScope{{Account: mailAcct, Kind: kind}}, MailCopyRange{Since: since, Until: until})
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 {
		t.Fatalf("checks %+v", checks)
	}
	return checks[0]
}

// For a range, the copy check compares what the current Sent Items holds with what older copies
// hold, counting each message once.
func TestMailCopyCheck(t *testing.T) {
	s := newStore(t)
	a := []outlookmail.Message{
		setMsg(1001, aSent, "<both@t>", 3),
		setMsg(1002, aSent, "<lost-1@t>", 3),
		setMsg(1003, aSent, "<lost-2@t>", 3),
		setMsg(1004, aSent, "<before@t>", 20),
		setMsg(1005, aSent, "<old-both@t>", 21),
	}
	b := []outlookmail.Message{
		setMsg(2001, bSent, "<both@t>", 3),
		setMsg(2002, bSent, "<new@t>", 3),
		setMsg(2010, bSent, "<oldest@t>", 10),
	}
	seedTwoSets(t, s, a, b)

	// Day 3: the current copy's range (from day 10) covers it, yet two messages are only in the
	// older copy.
	c := checkOf(t, s, "sent", day(3).Add(-time.Hour), day(3).Add(time.Hour))
	if c.State != "partial" || c.Messages != 4 || c.InCurrentCopy != 2 || c.OnlyInOlderCopies != 2 || c.LackingInCurrentRange != 2 ||
		!c.CurrentOldestAt.Equal(day(10)) || c.Account != mailAcct || c.Kind != "sent" || c.Copies != 2 {
		t.Fatalf("day 3: %+v", c)
	}
	// Day 20: before the current copy's range; only the older copy reaches it.
	c = checkOf(t, s, "sent", day(20).Add(-time.Hour), day(20).Add(time.Hour))
	if c.State != "older_copy_only" || c.Messages != 1 || c.OnlyInOlderCopies != 1 || c.LackingInCurrentRange != 0 {
		t.Fatalf("day 20: %+v", c)
	}
	// Day 15: a quiet day before the current range that the older copy's range reaches.
	if c = checkOf(t, s, "sent", day(15).Add(-time.Hour), day(15).Add(time.Hour)); c.State != "older_copy_only" || c.Messages != 0 {
		t.Fatalf("day 15: %+v", c)
	}
	// Day 10: the current copy holds everything the older one has.
	if c = checkOf(t, s, "sent", day(10).Add(-time.Hour), day(10).Add(time.Hour)); c.State != "consistent" || c.Messages != 1 || c.InCurrentCopy != 1 {
		t.Fatalf("day 10: %+v", c)
	}
	// Day 40: before every copy's range.
	if c = checkOf(t, s, "sent", day(40).Add(-time.Hour), day(40).Add(time.Hour)); c.State != "consistent" || c.Messages != 0 {
		t.Fatalf("day 40: %+v", c)
	}
	// An open range: everything, counted once.
	if c = checkOf(t, s, "sent", time.Time{}, time.Time{}); c.State != "partial" || c.Messages != 7 || c.InCurrentCopy != 3 || c.OnlyInOlderCopies != 4 {
		t.Fatalf("open: %+v", c)
	}
}

// The check runs only for an account and role with an older copy; a role without one, an
// unknown account and a role-less kind give nothing.
func TestMailCopyCheckOnlyWhereOlderCopiesExist(t *testing.T) {
	s := newStore(t)
	seedTwoSets(t, s, []outlookmail.Message{setMsg(1001, aSent, "<x@t>", 3)}, []outlookmail.Message{setMsg(2001, bSent, "<x@t>", 3)})
	checks, err := s.MailCopyChecks(context.Background(), []MailCopyScope{
		{Account: mailAcct, Kind: "other"}, {Account: "outlook/none", Kind: "sent"}, {Account: mailAcct, Kind: "sent"}, {Account: mailAcct, Kind: "sent"},
	}, MailCopyRange{})
	if err != nil || len(checks) != 1 || checks[0].Kind != "sent" {
		t.Fatalf("%+v %v", checks, err)
	}
	// Inbox has an older copy (empty), so it is checked, as is To Me through the inbox role.
	checks, err = s.MailCopyChecks(context.Background(), []MailCopyScope{{Account: mailAcct, Kind: "to_me"}}, MailCopyRange{})
	if err != nil || len(checks) != 1 || checks[0].Kind != "inbox" || checks[0].State != "consistent" {
		t.Fatalf("%+v %v", checks, err)
	}
}

// The list's gone and evicted switches apply to the current copy's messages. An older copy's
// messages count whatever their marks: later trusted reads of the new set find them missing and
// mark them, and the check must still say the current copy lacks them.
func TestMailCopyCheckFollowsGoneAndEvicted(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTwoSets(t, s, []outlookmail.Message{setMsg(1001, aSent, "<lost@t>", 3)}, []outlookmail.Message{setMsg(2001, bSent, "<x@t>", 5), setMsg(2002, bSent, "<y@t>", 4)})
	mustCommitMail(t, s, setBatch(hr(4), rootB, setMsg(2001, bSent, "<x@t>", 5), setMsg(2002, bSent, "<y@t>", 4)))
	if m, err := s.MailGet(ctx, mailAcct, 1001); err != nil || m.GoneAt.IsZero() {
		t.Fatalf("the older copy's message is marked gone by two trusted reads: %+v %v", m, err)
	}
	if c := checkOf(t, s, "sent", time.Time{}, time.Time{}); c.Messages != 3 || c.LackingInCurrentRange != 1 || c.State != "partial" || c.OlderCopiesMarked != 1 {
		t.Fatalf("an older copy's gone message still counts: %+v", c)
	}
	for col, mark := range map[string]string{
		"gone_at":    `update mail_messages set gone_at=?, evicted_at=null where detail_key=2002`,
		"evicted_at": `update mail_messages set evicted_at=?, gone_at=null where detail_key=2002`,
	} {
		if _, err := s.db.Exec(mark, fmtTime(hr(9))); err != nil {
			t.Fatal(err)
		}
		if c := checkOf(t, s, "sent", time.Time{}, time.Time{}); c.Messages != 2 || c.InCurrentCopy != 1 {
			t.Fatalf("%s: a current message the list hides is not counted: %+v", col, c)
		}
		checks, err := s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "sent"}}, MailCopyRange{IncludeGone: true, IncludeEvicted: true})
		if err != nil || checks[0].Messages != 3 || checks[0].InCurrentCopy != 2 {
			t.Fatalf("%s: %+v %v", col, checks, err)
		}
	}
}

func TestNormalizeMailMessageID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{" <A@B.test> ", "a@b.test"},
		{"a@b", "a@b"},
		{"<a@b", "<a@b"},
		{"<>", ""},
		{"", ""},
	} {
		if got := normalizeMailMessageID(c.in); got != c.want {
			t.Fatalf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFolderChainEnds(t *testing.T) {
	cycle := map[uint32]uint32{1: 2, 2: 1}
	if _, reaches := folderChain(cycle, 1, 9); reaches {
		t.Fatal("a cycle reaches no root")
	}
	if end, reaches := folderChain(cycle, 1, 1); !reaches || end != 1 {
		t.Fatal("a folder that is the root reaches it")
	}
	if earliest(time.Time{}, day(1)) != day(1) || earliest(day(1), time.Time{}) != day(1) || earliest(day(1), day(2)) != day(2) || earliest(day(2), day(1)) != day(2) {
		t.Fatal("earliest")
	}
}

func TestMailCopiesRefuseABadRecordedRoot(t *testing.T) {
	s := newStore(t)
	seedTwoSets(t, s, []outlookmail.Message{setMsg(1001, aSent, "<x@t>", 3)}, []outlookmail.Message{setMsg(2001, bSent, "<x@t>", 3)})
	if _, err := s.db.Exec(`update meta set value='x' where key like 'outlook_mail_root:%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MailList(context.Background(), MailFilter{}); err == nil || !strings.Contains(err.Error(), "root key") {
		t.Fatalf("%v", err)
	}
}

// seedCopiesForFaults holds every shape the copy rules read: copies in two older sets and the
// current one, rows without a Message-ID or a received time, a user folder, an empty role.
func seedCopiesForFaults(t *testing.T, s *Store) {
	t.Helper()
	noTime := setMsg(1009, aSent, "<no-time@t>", 4)
	noTime.Received = time.Time{}
	seedTwoSets(t, s,
		[]outlookmail.Message{
			setMsg(1001, aSent, "<x@t>", 3), setMsg(1002, aSent, "<y@t>", 4), setMsg(1003, aSent, "", 5),
			setMsg(1004, aInbox, "<in@t>", 6), setMsg(1005, aToMe, "<tome@t>", 2), setMsg(1006, aProject(), "<p@t>", 7), noTime,
		},
		[]outlookmail.Message{setMsg(2001, bSent, "<x@t>", 3), setMsg(2002, bSent, "<y@t>", 4), setMsg(2004, bInbox, "<in@t>", 6), setMsg(2005, bToMe, "<new@t>", 1)})
	if _, err := s.db.Exec(`update mail_messages set received_at=null where detail_key=1009`); err != nil {
		t.Fatal(err)
	}
}

func TestMailCopyReadsSurfaceFailures(t *testing.T) {
	sweepReadFaults(t, seedCopiesForFaults, func(ctx context.Context, s *Store) error {
		if _, err := s.MailList(ctx, MailFilter{}); err != nil {
			return err
		}
		if _, err := s.MailGet(ctx, mailAcct, 1001); err != nil {
			return err
		}
		if _, err := s.MailFolders(ctx); err != nil {
			return err
		}
		if _, err := s.MailCoverage(ctx); err != nil {
			return err
		}
		if _, _, _, err := s.MailThread(ctx, mailAcct, 2001); err != nil {
			return err
		}
		if _, err := s.MailStatusOf(ctx, mailAcct); err != nil {
			return err
		}
		if _, err := s.EventMail(ctx, calendar.Event{ICalUID: "none"}, 0); err != nil {
			return err
		}
		_, err := s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "sent"}, {Account: mailAcct, Kind: "inbox"}, {Account: mailAcct, Kind: "archive"}}, MailCopyRange{Since: day(30), Until: day(0)})
		return err
	})
}

func TestMailCopyCheckOverEveryShape(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedCopiesForFaults(t, s)
	if got := listKeys(t, s, MailFilter{Folder: "sent"}); !sameKeys(got, 2001, 2002, 1003, 1009) {
		t.Fatalf("sent %v", got)
	}
	checks, err := s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "sent"}, {Account: mailAcct, Kind: "inbox"}, {Account: mailAcct, Kind: "archive"}}, MailCopyRange{})
	if err != nil || len(checks) != 2 {
		t.Fatalf("%+v %v", checks, err)
	}
	if c := checks[0]; c.Messages != 4 || c.InCurrentCopy != 2 || c.OnlyInOlderCopies != 2 || c.LackingInCurrentRange != 0 || c.State != "consistent" {
		t.Fatalf("sent %+v", c)
	}
	if c := checks[1]; c.Kind != "inbox" || c.Messages != 3 || c.InCurrentCopy != 2 || c.LackingInCurrentRange != 1 || c.State != "partial" || !c.CurrentOldestAt.Equal(day(6)) {
		t.Fatalf("inbox %+v", c)
	}
}

// Between two older sets the row seen last stands for the message, then the row archived last; a
// message the current set also holds is stood for by the current row. A message whose folder
// object the store does not hold is never an older copy.
func TestMailCopiesBetweenOlderSets(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	cSent := uint32(rootC + 2)
	lost := setMsg(3009, 999, "<lost-folder@t>", 1)
	lost.Folder = outlookmail.Folder{Key: 999, Kind: outlookmail.KindUnknown}
	mustCommitMail(t, s, setBatch(hr(1), rootA, setMsg(1001, aSent, "<z@t>", 3), setMsg(1002, aSent, "<all@t>", 4)))
	commitUnmappedThenMapped(t, s, 2, rootC, setMsg(3001, cSent, "<z@t>", 3), setMsg(3002, cSent, "<all@t>", 4), lost)
	commitUnmappedThenMapped(t, s, 4, rootB, setMsg(2002, bSent, "<all@t>", 4))
	// Two trusted reads of later sets left set A's rows absent twice: they are marked, as before.
	rows, err := s.MailList(ctx, MailFilter{IncludeCopies: true, IncludeGone: true, IncludeEvicted: true})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32]MailRow{}
	for _, r := range rows {
		got[r.DetailKey] = r
	}
	if got[1001].CopyOf != mailAcct+":3001" || got[3001].CopyOf != "" || got[1002].CopyOf != mailAcct+":2002" || got[3002].CopyOf != mailAcct+":2002" ||
		got[3009].OlderCopy || got[3009].CopyOf != "" || !got[3001].OlderCopy {
		t.Fatalf("%+v", got)
	}
	if _, err := s.db.Exec(`update mail_messages set state_seen_at='2026-09-10T08:00:00.000Z' where detail_key in (1001, 3001)`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.MailGet(ctx, mailAcct, 1001); err != nil || r.CopyOf != mailAcct+":3001" {
		t.Fatalf("a tie goes to the row archived last: %+v %v", r, err)
	}
}

// A thread lists a message once: a copy that a newer folder set holds is left out, unless it is
// the message the thread was asked for.
func TestMailThreadLeavesCopiesOut(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	parent := setMsg(2003, bInbox, "<p@t>", 5)
	reply := func(dk, folder uint32) outlookmail.Message {
		m := setMsg(dk, folder, "<x@t>", 3)
		m.InReplyTo = "<p@t>"
		return m
	}
	seedTwoSets(t, s, []outlookmail.Message{reply(1001, aSent)}, []outlookmail.Message{reply(2001, bSent), parent})
	keys := func(seed uint32) []uint32 {
		rows, grouping, _, err := s.MailThread(ctx, mailAcct, seed)
		if err != nil || grouping != "reply_chain" {
			t.Fatalf("%v %v", grouping, err)
		}
		var out []uint32
		for _, r := range rows {
			out = append(out, r.DetailKey)
		}
		return out
	}
	if got := keys(2001); !sameKeys(got, 2003, 2001) {
		t.Fatalf("from the current copy: %v", got)
	}
	if got := keys(1001); !sameKeys(got, 2003, 1001, 2001) {
		t.Fatalf("from the older copy: %v", got)
	}
}

// A message the current set holds anywhere is not missing from it: deleted after the rebuild (its
// current row gone) or moved to another folder. Drafts, Deleted Items and Archive are not checked:
// their coverage is only the oldest item they hold.
func TestMailCopyCheckCountsWhatTheCurrentSetHoldsAnywhere(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTwoSets(t, s,
		[]outlookmail.Message{setMsg(1001, aInbox, "<deleted-later@t>", 3), setMsg(1002, aInbox, "<moved@t>", 3), setMsg(1003, aDeleted, "<trash@t>", 3)},
		[]outlookmail.Message{setMsg(2001, bInbox, "<deleted-later@t>", 3), setMsg(2002, bDeleted, "<moved@t>", 3), setMsg(2010, bInbox, "<anchor@t>", 10)})
	if _, err := s.db.Exec(`update mail_messages set gone_at=? where detail_key=2001`, fmtTime(hr(9))); err != nil {
		t.Fatal(err)
	}
	if c := checkOf(t, s, "inbox", day(3).Add(-time.Hour), day(3).Add(time.Hour)); c.State != "consistent" || c.Messages != 0 || c.LackingInCurrentRange != 0 {
		t.Fatalf("%+v", c)
	}
	checks, err := s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "inbox"}}, MailCopyRange{Since: day(3).Add(-time.Hour), Until: day(3).Add(time.Hour), IncludeGone: true})
	if err != nil || checks[0].Messages != 1 || checks[0].InCurrentCopy != 1 || checks[0].State != "consistent" {
		t.Fatalf("%+v %v", checks, err)
	}
	checks, err = s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "deleted"}, {Account: mailAcct, Kind: "drafts"}, {Account: mailAcct, Kind: "archive"}}, MailCopyRange{Since: day(7)})
	if err != nil || len(checks) != 0 {
		t.Fatalf("only Inbox and Sent Items are checked: %+v %v", checks, err)
	}
	// Sent Items has no older copy once the older set's Sent Items is another kind of folder.
	if _, err := s.db.Exec(`update mail_folders set kind='other' where folder_key=?`, aSent); err != nil {
		t.Fatal(err)
	}
	if checks, err = s.MailCopyChecks(ctx, []MailCopyScope{{Account: mailAcct, Kind: "sent"}}, MailCopyRange{}); err != nil || len(checks) != 0 {
		t.Fatalf("%+v %v", checks, err)
	}
}

// A thread gathers its messages with copies already left out, so copies never take the place of a
// message under the cap and truncated stays true to what was left out.
func TestMailThreadCapCountsNoCopies(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	n := mailThreadCap - 40
	var a, b []outlookmail.Message
	for i := range n {
		for _, x := range []struct {
			dk     uint32
			folder uint32
			out    *[]outlookmail.Message
		}{{uint32(1000 + i), aSent, &a}, {uint32(5000 + i), bSent, &b}} {
			m := setMsg(x.dk, x.folder, fmt.Sprintf("<t-%d@t>", i), 3)
			m.Subject, m.Received = "Plan", mailT0.Add(-time.Duration(i)*time.Minute)
			*x.out = append(*x.out, m)
		}
	}
	seedTwoSets(t, s, a, b)
	rows, grouping, truncated, err := s.MailThread(ctx, mailAcct, 5000)
	if err != nil || grouping != "subject" || truncated || len(rows) != n {
		t.Fatalf("%s truncated=%v rows=%d (want %d) %v", grouping, truncated, len(rows), n, err)
	}
	for _, r := range rows {
		if r.OlderCopy {
			t.Fatalf("a copy in the thread: %+v", r)
		}
	}
}

// The status mail block and an event's related mail count and list a message once.
func TestMailStatusAndEventMailLeaveCopiesOut(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	invite := func(dk, folder uint32) outlookmail.Message {
		m := setMsg(dk, folder, "<invite@t>", 3)
		m.ICalUID = "ABC"
		return m
	}
	seedTwoSets(t, s, []outlookmail.Message{invite(1001, aInbox), setMsg(1002, aInbox, "<only-a@t>", 4)}, []outlookmail.Message{invite(2001, bInbox)})
	st, err := s.MailStatusOf(ctx, mailAcct)
	if err != nil || st.Messages != 2 || st.Unread != 2 {
		t.Fatalf("%+v %v", st, err)
	}
	related, err := s.EventMail(ctx, calendar.Event{ICalUID: "abc"}, 0)
	if err != nil || len(related) != 1 || related[0].DetailKey != 2001 {
		t.Fatalf("%+v %v", related, err)
	}
}
