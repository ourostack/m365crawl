package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// withCopies gives a message header copies in the folders, each unread unless listed in read.
func withCopies(m outlookmail.Message, keys []uint32, read ...uint32) outlookmail.Message {
	m.FolderCopies = nil
	for _, k := range keys {
		unread := !slices.Contains(read, k)
		m.FolderCopies = append(m.FolderCopies, outlookmail.FolderCopy{Key: k, Unread: &unread})
	}
	return m
}

func sortedKeys(keys []uint32) []uint32 {
	slices.Sort(keys)
	return keys
}

func memberKeys(t *testing.T, s *Store, dk uint32) []uint32 {
	t.Helper()
	rows, err := s.db.Query(`select f.folder_key from mail_message_folders f join mail_messages m on m.rowid=f.message_rowid
where m.account=? and m.detail_key=? order by f.folder_key`, mailAcct, dk)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []uint32
	for rows.Next() {
		var k uint32
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

func folderCounts(t *testing.T, s *Store) map[string][2]int {
	t.Helper()
	rows, err := s.MailFolders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]int{}
	for _, f := range rows {
		out[f.Name] = [2]int{f.Messages, f.Unread}
	}
	return out
}

// A message sent to oneself is in Sent Items, Inbox and To Me. It is shown from Inbox, and
// --folder sent, --folder to_me, mail folders and the date all see the other folders (#270).
func TestMailMembershipListsAMessageInEveryFolder(t *testing.T) {
	s := newStore(t)
	self := timed(withCopies(mailMsg(100, fInbox, "<self@x>", "Note to self", 0), []uint32{fInbox, fSent, fToMe}, fSent), "2026-09-02T07:10:00Z", "2026-09-01T23:30:00Z")
	self.ToMe = true
	other := mailMsg(101, fInbox, "<other@x>", "Other", 1)
	b := mailBatch(mailT0, self, other)
	if r := mustCommitMail(t, s, b); r != (MailResult{Added: 2}) {
		t.Fatalf("%+v", r)
	}
	if got := memberKeys(t, s, 100); !slices.Equal(got, []uint32{fInbox, fSent, fToMe}) {
		t.Fatalf("membership %v", got)
	}
	if got := memberKeys(t, s, 101); got != nil {
		t.Fatalf("a message in one folder needs no rows: %v", got)
	}
	for folder, want := range map[string][]uint32{"sent": {100}, "Sent Items": {100}, "to_me": {100}, "inbox": {100, 101}} {
		if got := sortedKeys(listKeys(t, s, MailFilter{Folder: folder})); !slices.Equal(got, want) {
			t.Fatalf("--folder %s: %v, want %v", folder, got, want)
		}
	}
	row := listRows(t, s, MailFilter{Folder: "sent"})[100]
	if row.Folder != "Inbox" || row.FolderKind != "inbox" {
		t.Fatalf("the message is still shown from its current copy: %+v", row)
	}
	var kinds []string
	for _, f := range row.Folders {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []string{"inbox", "to_me", "sent"}) {
		t.Fatalf("folders %v", kinds)
	}
	// Dated by when it was sent, in every list: Sep 1 (UTC), not its Sep 2 received time.
	sep1 := MailFilter{Since: utc("2026-09-01T00:00:00Z"), Until: utc("2026-09-02T00:00:00Z")}
	for _, f := range []MailFilter{sep1, {Folder: "sent", Since: sep1.Since, Until: sep1.Until}, {Folder: "inbox", Since: sep1.Since, Until: sep1.Until}} {
		if got := listRows(t, s, f)[100]; !got.DatedAt.Equal(utc("2026-09-01T23:30:00Z")) {
			t.Fatalf("%+v: dated %v", f, got.DatedAt)
		}
	}
	// Read in Sent Items, unread in Inbox and To Me.
	if got := listKeys(t, s, MailFilter{Folder: "sent", Unread: true}); got != nil {
		t.Fatalf("--folder sent --unread: %v", got)
	}
	if got := sortedKeys(listKeys(t, s, MailFilter{Folder: "inbox", Unread: true})); !slices.Equal(got, []uint32{100, 101}) {
		t.Fatalf("--folder inbox --unread: %v", got)
	}
	counts := folderCounts(t, s)
	if counts["Sent Items"] != [2]int{1, 0} || counts["Inbox"] != [2]int{2, 2} || counts["To Me"] != [2]int{1, 1} {
		t.Fatalf("folder counts %v", counts)
	}
	if st := must(s.MailStatus(context.Background())); st.Messages != 2 || st.Unread != 2 {
		t.Fatalf("status counts each message once: %+v", st)
	}
	if r := mustCommitMail(t, s, b); r != (MailResult{}) {
		t.Fatalf("an unchanged read wrote: %+v", r)
	}
	// Outlook deletes the Sent copy: the message leaves --folder sent.
	gone := mailBatch(mailT0.Add(time.Hour), withCopies(self, []uint32{fInbox, fToMe}), other)
	if r := mustCommitMail(t, s, gone); r != (MailResult{Updated: 1}) {
		t.Fatalf("%+v", r)
	}
	if got := listKeys(t, s, MailFilter{Folder: "sent"}); got != nil {
		t.Fatalf("--folder sent after the copy left: %v", got)
	}
	// Down to one folder: no rows.
	mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), withCopies(self, []uint32{fInbox}), other))
	if got := memberKeys(t, s, 100); got != nil {
		t.Fatalf("membership %v", got)
	}
}

// The first read after the upgrade fills the membership of messages already archived without
// counting them as updated or moving their clocks; later changes count.
func TestMailMembershipBackfillsOnTheFirstRead(t *testing.T) {
	s := newStore(t)
	plain := mailMsg(100, fInbox, "<self@x>", "Note to self", 0)
	mustCommitMail(t, s, mailBatch(mailT0, plain))
	if _, err := s.db.Exec(`delete from meta where key like ?`, mailFoldersReadyKey+"%"); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.db.QueryRow(`select modified_at from mail_messages where detail_key=100`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	multi := withCopies(plain, []uint32{fInbox, fSent})
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), multi)); r != (MailResult{}) {
		t.Fatalf("backfill counted: %+v", r)
	}
	var after string
	if err := s.db.QueryRow(`select modified_at from mail_messages where detail_key=100`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before || !slices.Equal(memberKeys(t, s, 100), []uint32{fInbox, fSent}) {
		t.Fatalf("backfill moved the clock (%s → %s) or wrote %v", before, after, memberKeys(t, s, 100))
	}
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), withCopies(plain, []uint32{fInbox, fSent, fDeleted}))); r != (MailResult{Updated: 1}) {
		t.Fatalf("a later change: %+v", r)
	}
}

// Re-keying keeps the rowid the membership hangs from; a different message taking the key replaces
// it. Foreign keys are on, and no row is left without its message.
func TestMailMembershipFollowsRekeyAndReplace(t *testing.T) {
	s := newStore(t)
	var fk int
	if err := s.db.QueryRow(`pragma foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys %d %v", fk, err)
	}
	a := withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fSent})
	mustCommitMail(t, s, mailBatch(mailT0, a))
	rowid := listRows(t, s, MailFilter{})[100].Rowid
	moved := a
	moved.DetailKey = 200
	moved = withCopies(moved, []uint32{fInbox, fDeleted})
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), moved))
	if r := listRows(t, s, MailFilter{})[200]; r.Rowid != rowid || !slices.Equal(memberKeys(t, s, 200), []uint32{fInbox, fDeleted}) {
		t.Fatalf("re-keyed row %d (was %d): %v", r.Rowid, rowid, memberKeys(t, s, 200))
	}
	other := withCopies(mailMsg(200, fSent, "<b@x>", "B", 0), []uint32{fSent, fProject})
	if r := mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), other)); r.Replaced != 1 {
		t.Fatalf("%+v", r)
	}
	if got := memberKeys(t, s, 200); !slices.Equal(got, []uint32{fSent, fProject}) {
		t.Fatalf("replaced membership %v", got)
	}
	if n := rowCount(t, s, `select count(*) from pragma_foreign_key_check`); n != 0 {
		t.Fatalf("foreign key violations: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from mail_message_folders f where not exists(select 1 from mail_messages m where m.rowid=f.message_rowid and m.account=f.account)`); n != 0 {
		t.Fatalf("orphaned membership rows: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from mail_message_folders f join mail_messages m on m.rowid=f.message_rowid
where not exists(select 1 from mail_message_folders g where g.message_rowid=m.rowid and g.folder_key=m.folder_key)`); n != 0 {
		t.Fatalf("membership without the stored folder: %d", n)
	}
}

// An archive at schema 23 has no membership table: a read-only open lists as before, and a
// writable open adds the table empty, for the next read to fill.
func TestMailMembershipOnAnArchiveBeforeSchema24(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fSent, "<a@x>", "A", 0), mailMsg(101, fInbox, "<b@x>", "B", 0)))
	for _, q := range []string{`drop table mail_message_folders`, `update schema_migrations set version = 23`, `delete from meta where key like 'mail_folders_ready:%'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, ro, MailFilter{Folder: "sent"}); !slices.Equal(got, []uint32{100}) {
		t.Fatalf("read-only list %v", got)
	}
	if got := sortedKeys(listKeys(t, ro, MailFilter{Unread: true})); !slices.Equal(got, []uint32{100, 101}) {
		t.Fatalf("read-only unread %v", got)
	}
	if counts := folderCounts(t, ro); counts["Sent Items"] != [2]int{1, 1} {
		t.Fatalf("read-only counts %v", counts)
	}
	if _, err := ro.MailCopyChecks(ctx, []MailCopyScope{{mailAcct, "sent"}}, MailCopyRange{}); err != nil {
		t.Fatal(err)
	}
	_ = ro.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := rowCount(t, s, `select max(version) from schema_migrations`); v != SchemaVersion || SchemaVersion < 24 {
		t.Fatalf("version %d", v)
	}
	if n := rowCount(t, s, `select count(*) from mail_message_folders`); n != 0 {
		t.Fatalf("the upgrade seeded %d rows", n)
	}
	mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), withCopies(mailMsg(101, fInbox, "<b@x>", "B", 0), []uint32{fInbox, fSent}), mailMsg(100, fSent, "<a@x>", "A", 0)))
	if got := sortedKeys(listKeys(t, s, MailFilter{Folder: "sent"})); !slices.Equal(got, []uint32{100, 101}) {
		t.Fatalf("after the next read %v", got)
	}
}

// Folder-copy pairing uses the membership: an older Sent row is a copy of the current row whose
// membership has Sent Items, even when that row is shown from Inbox. A current row without Sent
// Items still does not stand for it (its Sent copy may only have left the cache).
func TestMailMembershipPairsFolderCopiesByRole(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		current []uint32
		paired  bool
	}{{"current has sent", []uint32{bInbox, bSent}, true}, {"current lacks sent", []uint32{bInbox, bDeleted}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			older := setMsg(1001, aSent, "<s@t>", 0)
			current := withCopies(setMsg(2001, bInbox, "<s@t>", 0), tc.current)
			seedTwoSets(t, s, []outlookmail.Message{older}, []outlookmail.Message{current})
			rows := listRows(t, s, MailFilter{IncludeCopies: true})
			if paired := rows[1001].CopyOf == mailAcct+":2001"; paired != tc.paired {
				t.Fatalf("copy_of %q", rows[1001].CopyOf)
			}
			want := []uint32{1001}
			if tc.paired {
				want = []uint32{2001}
			}
			if got := listKeys(t, s, MailFilter{Folder: "sent"}); !slices.Equal(got, want) {
				t.Fatalf("--folder sent %v, want %v", got, want)
			}
			checks, err := s.MailCopyChecks(ctx, []MailCopyScope{{mailAcct, "sent"}}, MailCopyRange{Since: mailT0.AddDate(0, 0, -1), Until: mailT0.AddDate(0, 0, 1)})
			if err != nil || len(checks) != 1 {
				t.Fatalf("%v %v", checks, err)
			}
			if c := checks[0]; c.Messages != 1 || (c.InCurrentCopy == 1) != tc.paired {
				t.Fatalf("%+v", c)
			}
		})
	}
}

// WorkIQ: a server write that changes the stored folder clears the native membership (server
// folders have their own keys); a native read whose copy is not newer leaves the rows alone; the
// retained native projection does not carry the membership.
func TestMailMembershipWithWorkIQ(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	native := withCopies(mailMsg(100, fInbox, "<one@example.test>", "Subject", 0), []uint32{fInbox, fSent})
	mustCommitMail(t, s, mailBatch(mailT0, native))
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, attempt)
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	if got := memberKeys(t, s, 100); !slices.Equal(got, []uint32{fInbox, fSent}) {
		t.Fatalf("a bind that keeps the native folder kept %v", got)
	}
	var projection string
	must0(s.db.QueryRowContext(ctx, `select coalesce(cache_projection,'') from workiq_rows where kind='mail'`).Scan(&projection))
	if strings.Contains(projection, "FolderCopies") {
		t.Fatal("the retained native projection carries the membership")
	}
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, run)
	must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "archive-id", WellKnown: "archive"}))
	raw := []byte(strings.ReplaceAll(string(graphMailFixture("graph-one", "archive-id", "one@example.test")), "2026-09-10T08:01:00Z", "2026-09-11T08:01:00Z"))
	must0(s.StageWorkIQMailItems(ctx, run, "archive-id", []json.RawMessage{raw}, mailT0.Add(time.Hour)))
	settleWorkIQMailFixture(t, s, run)
	_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	if got := memberKeys(t, s, 100); got != nil {
		t.Fatalf("a server move kept native membership %v", got)
	}
	if got := listKeys(t, s, MailFilter{Folder: "archive"}); !slices.Equal(got, []uint32{100}) {
		t.Fatalf("--folder archive %v", got)
	}
	// The same native store copy again: not newer, so the server's folder and its (empty) membership stay.
	mustCommitMail(t, s, mailBatch(mailT0, native))
	if got := memberKeys(t, s, 100); got != nil || len(listKeys(t, s, MailFilter{Folder: "sent"})) != 0 {
		t.Fatalf("a stale native copy rewrote the membership: %v", got)
	}
	// A newer native copy is evidence again.
	mustCommitMail(t, s, mailBatch(mailT0.Add(2*time.Hour), native))
	if got := memberKeys(t, s, 100); !slices.Equal(got, []uint32{fInbox, fSent}) {
		t.Fatalf("a newer native copy wrote %v", got)
	}
}

// When WorkIQ is on, native folder keys that collide with a server folder are remapped; the
// membership is remapped with the stored folder, so it still includes it.
func TestMailMembershipFollowsNativeFolderKeyRemap(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "server-folder", "displayName": "Server folder", "childFolderCount": 0}}})
	_, err = s.PrepareWorkIQMailPage(ctx, attempt, root, 128)
	must0(err)
	resource := "/me/mailFolders/server-folder/messages/delta"
	page := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []json.RawMessage{graphMailFixture("one", "server-folder", "one@example.test")}, "@odata.deltaLink": resource + "?$deltatoken=end"})
	_, err = s.PrepareWorkIQMailPage(ctx, attempt, page, 128)
	must0(err)
	must0(s.SealWorkIQMailInventory(ctx, attempt, mailT0))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var serverFolder uint32
	must0(s.db.QueryRowContext(ctx, `select folder_key from mail_messages`).Scan(&serverFolder))
	local := withCopies(mailMsg(1000, serverFolder, "other@example.test", "Other", 0), []uint32{serverFolder, fSent})
	local.Folder = outlookmail.Folder{Key: serverFolder, Kind: "other", Name: "Native unrelated"}
	b := mailBatch(mailT0, local)
	b.Result.Folders = append(b.Result.Folders, local.Folder)
	mustCommitMail(t, s, b)
	if b.Result.Messages[0].FolderCopies[0].Key != serverFolder {
		t.Fatal("the remap changed the caller's batch")
	}
	var stored uint32
	must0(s.db.QueryRowContext(ctx, `select folder_key from mail_messages where internet_message_id='other@example.test'`).Scan(&stored))
	got := memberKeys(t, s, 1000)
	if stored == serverFolder || !slices.Contains(got, stored) || slices.Contains(got, serverFolder) || len(got) != 2 {
		t.Fatalf("stored folder %d, membership %v (server folder %d)", stored, got, serverFolder)
	}
}

// A folder list reads through the folder indexes of both the messages and the membership; it
// never scans every message.
func TestMailMembershipFolderListUsesIndexes(t *testing.T) {
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fSent}), mailMsg(101, fSent, "<b@x>", "B", 1)))
	for name, f := range map[string]MailFilter{
		"sent":        {Folder: "sent"},
		"sent window": {Folder: "sent", Since: mailT0.AddDate(0, 0, -3), Until: mailT0},
		"inbox":       {Folder: "inbox", Unread: true},
	} {
		plan := queryPlan(t, s, f)
		joined := strings.Join(plan, "\n")
		if !strings.Contains(joined, "mail_message_folders_folder") || !strings.Contains(joined, "mail_messages_folder") {
			t.Fatalf("%s: no folder index:\n%s", name, joined)
		}
		for _, d := range plan {
			if d == "SCAN m" || d == "SCAN mail_messages" || d == "SCAN x" || strings.HasPrefix(d, "SCAN m ") && !strings.Contains(d, "INDEX") {
				t.Fatalf("%s: a full scan of the messages:\n%s", name, joined)
			}
		}
	}
}

// Without --folder, --unread keeps a message unread in any folder it is in, so the list, the count
// and the folder counts agree: a message read in Inbox (the copy it is shown from) and unread in a
// folder a rule copied it to.
func TestMailMembershipUnreadAnywhere(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	read := false
	m := withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fProject}, fInbox)
	m.Unread, m.ReadState = &read, "read"
	mustCommitMail(t, s, mailBatch(mailT0, m, mailMsg(101, fSent, "<b@x>", "B", 1)))
	if got := sortedKeys(listKeys(t, s, MailFilter{Unread: true})); !slices.Equal(got, []uint32{100, 101}) {
		t.Fatalf("--unread %v", got)
	}
	if n := must(s.MailCount(ctx, MailFilter{Unread: true})); n != 2 {
		t.Fatalf("count %d", n)
	}
	if got := listKeys(t, s, MailFilter{Folder: "inbox", Unread: true}); got != nil {
		t.Fatalf("--folder inbox --unread %v", got)
	}
	if counts := folderCounts(t, s); counts["Projects"] != [2]int{1, 1} || counts["Inbox"] != [2]int{1, 0} {
		t.Fatalf("counts %v", counts)
	}
	if st := must(s.MailStatus(ctx)); st.Unread != 2 {
		t.Fatalf("status unread %d", st.Unread)
	}
}

// A failure anywhere in a membership write rolls the whole read back, and a failed membership read
// surfaces.
func TestMailMembershipFaults(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		mustCommitMail(t, s, mailBatch(mailT0, withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fSent}), mailMsg(102, fSent, "<c@x>", "C", 2)))
	}
	t.Run("write", func(t *testing.T) {
		sweepFaults(t, seed, func(ctx context.Context, s *Store) error {
			_, err := s.CommitMail(ctx, mailBatch(hr(1), withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fDeleted}),
				withCopies(mailMsg(101, fInbox, "<b@x>", "B", 1), []uint32{fInbox, fSent}), mailMsg(102, fSent, "<c@x>", "C", 2)))
			return err
		})
	})
	t.Run("read", func(t *testing.T) {
		sweepReadFaults(t, func(t *testing.T, s *Store) {
			seedTwoSets(t, s, []outlookmail.Message{setMsg(1001, aSent, "<s@t>", 0)}, []outlookmail.Message{withCopies(setMsg(2001, bInbox, "<s@t>", 0), []uint32{bInbox, bSent})})
		}, func(ctx context.Context, s *Store) error {
			if _, err := s.MailList(ctx, MailFilter{Folder: "sent", Unread: true}); err != nil {
				return err
			}
			if _, err := s.MailCount(ctx, MailFilter{Unread: true}); err != nil {
				return err
			}
			if _, err := s.MailFolders(ctx); err != nil {
				return err
			}
			_, err := s.MailCopyChecks(ctx, []MailCopyScope{{mailAcct, "sent"}}, MailCopyRange{Since: mailT0.AddDate(0, 0, -1)})
			return err
		})
	})
}

// A server move clears the membership in the same transaction as the rest of the server write.
func TestMailMembershipServerMoveFaults(t *testing.T) {
	var run int64
	sweepFaults(t, func(t *testing.T, s *Store) {
		ctx := context.Background()
		attempt := seedWorkIQMailAttempt(t, s)
		mustCommitMail(t, s, mailBatch(mailT0, withCopies(mailMsg(100, fInbox, "<one@example.test>", "Subject", 0), []uint32{fInbox, fSent})))
		must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}))
		must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("graph-one", "inbox-id", "one@example.test")}, mailT0))
		settleWorkIQMailFixture(t, s, attempt)
		_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
		run = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
		verifyWorkIQTestAttempt(t, s, run)
		must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "archive-id", WellKnown: "archive"}))
		raw := []byte(strings.ReplaceAll(string(graphMailFixture("graph-one", "archive-id", "one@example.test")), "2026-09-10T08:01:00Z", "2026-09-11T08:01:00Z"))
		must0(s.StageWorkIQMailItems(ctx, run, "archive-id", []json.RawMessage{raw}, mailT0.Add(time.Hour)))
		settleWorkIQMailFixture(t, s, run)
	}, func(ctx context.Context, s *Store) error {
		_, err := s.CommitWorkIQMailGroup(ctx, run, "one@example.test")
		return err
	})
}

// Folders of one kind are listed by name, then by key.
func TestMailMembershipFolderOrder(t *testing.T) {
	s := newStore(t)
	const fAlpha, fProjects2 = 30, 31
	m := withCopies(mailMsg(100, fInbox, "<a@x>", "A", 0), []uint32{fInbox, fProject, fAlpha, fProjects2})
	b := mailBatch(mailT0, m)
	b.Result.Folders = append(b.Result.Folders, outlookmail.Folder{Key: fAlpha, Parent: 1, Name: "alpha", Kind: "other"}, outlookmail.Folder{Key: fProjects2, Parent: 1, Name: "Projects", Kind: "other"})
	mustCommitMail(t, s, b)
	var keys []uint32
	for _, f := range listRows(t, s, MailFilter{})[100].Folders {
		keys = append(keys, f.Key)
	}
	if !slices.Equal(keys, []uint32{fInbox, fAlpha, fProject, fProjects2}) {
		t.Fatalf("order %v", keys)
	}
}
