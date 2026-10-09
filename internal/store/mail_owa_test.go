package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
)

func owaMessage(id, folder, messageID string, daysAgo int) outlookowa.Message {
	m := mailMsg(0, 0, messageID, "native mail "+id, daysAgo)
	m.Folder = outlookmail.Folder{}
	has := false
	m.HasAttachments = &has
	m.Recipients[0].Role, m.Recipients[0].KindRaw = "to", 0
	return outlookowa.Message{ID: id, FolderID: folder, Value: m}
}

func owaBatch(at time.Time, messages ...outlookowa.Message) OWAMailBatch {
	r := outlookowa.Result{
		RootID: "root",
		Folders: []outlookowa.Folder{
			{ID: "inbox", ParentID: "root", Value: outlookmail.Folder{Name: "Inbox", Kind: "inbox"}},
			{ID: "sent", ParentID: "root", Value: outlookmail.Folder{Name: "Sent", Kind: "sent"}},
		},
		Messages: messages,
	}
	for _, m := range messages {
		r.SeenIDs = append(r.SeenIDs, m.ID)
	}
	return OWAMailBatch{Account: mailAcct, ReadAt: at, FreshAt: at, Result: r, Trusted: true}
}

func mustCommitOWA(t *testing.T, s *Store, b OWAMailBatch) MailResult {
	t.Helper()
	res, err := s.CommitOWAMail(context.Background(), b)
	if err != nil {
		t.Fatalf("CommitOWAMail: %v", err)
	}
	return res
}

func owaStoredKey(t *testing.T, s *Store, account, kind, id string) uint32 {
	t.Helper()
	var key uint32
	if err := s.db.QueryRow(`select key from mail_source_ids where account=? and kind=? and source_id=?`, account, kind, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCommitOWAMailImportRepeatUpdateAndAccounts(t *testing.T) {
	s := newStore(t)
	a := owaMessage("id-a", "inbox", "<a@example.test>", 2)
	b := owaMessage("id-b", "sent", "<b@example.test>", 1)
	a.Value.ConversationID, b.Value.ConversationID = "conversation", "conversation"
	res := mustCommitOWA(t, s, owaBatch(mailT0, b, a))
	if res != (MailResult{Added: 2}) {
		t.Fatalf("initial import: %+v", res)
	}
	if key := owaStoredKey(t, s, mailAcct, "message", "id-a"); key != 1 {
		t.Fatalf("first stable public key = %d, want 1", key)
	}
	first, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil || first.ConversationID != "conversation" || len(first.Recipients) != 1 || first.Recipients[0].Role != "to" || first.BodyText != "body of native mail id-a" {
		t.Fatalf("normalized mail: %+v / %v", first, err)
	}
	if res := mustCommitOWA(t, s, owaBatch(mailT0.Add(time.Hour), a, b)); res != (MailResult{}) {
		t.Fatalf("unchanged import wrote mail: %+v", res)
	}
	unread := false
	a.Value.Unread, a.Value.ReadState, a.Value.Flag = &unread, "read", "flagged"
	if res := mustCommitOWA(t, s, owaBatch(mailT0.Add(2*time.Hour), a, b)); res != (MailResult{Updated: 1}) {
		t.Fatalf("state update: %+v", res)
	}
	got, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil || got.Rowid != first.Rowid || got.ReadState != "read" || got.Flag != "flagged" {
		t.Fatalf("stable state/public identity: %+v / %v", got, err)
	}
	other := owaBatch(mailT0, a)
	other.Account = "outlook/other-profile"
	if res := mustCommitOWA(t, s, other); res.Added != 1 {
		t.Fatalf("other account: %+v", res)
	}
	if key := owaStoredKey(t, s, other.Account, "message", "id-a"); key != 1 {
		t.Fatalf("account-local key = %d, want 1", key)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages`); n != 3 {
		t.Fatalf("account rows = %d, want 3", n)
	}
}

func TestCommitOWAMailRollsBackIdentityAndMail(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
	}{
		{"normalization allocator failure", `create trigger reject_native before insert on mail_source_ids when new.kind='message' begin select raise(abort,'synthetic identity failure'); end`},
		{"mail child failure", `create trigger reject_native before insert on mail_recipients begin select raise(abort,'synthetic child failure'); end`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			res, err := s.CommitOWAMail(context.Background(), owaBatch(mailT0, owaMessage("id-a", "inbox", "<a@example.test>", 1)))
			if err == nil || res != (MailResult{}) {
				t.Fatalf("failed transaction returned success/partial result: %+v / %v", res, err)
			}
			for _, table := range []string{"mail_source_ids", "mail_folders", "mail_messages", "mail_recipients", "mail_attachments", "mail_coverage", "mail_absent", "mail_fts"} {
				if n := rowCount(t, s, `select count(*) from `+table); n != 0 {
					t.Fatalf("%s retained %d rolled-back rows", table, n)
				}
			}
			if n := rowCount(t, s, `select count(*) from meta where key=?`, mailRootKey+mailAcct); n != 0 {
				t.Fatal("rolled-back root marker persisted")
			}
			if _, err := s.db.Exec(`drop trigger reject_native`); err != nil {
				t.Fatal(err)
			}
			if res := mustCommitOWA(t, s, owaBatch(mailT0, owaMessage("id-a", "inbox", "<a@example.test>", 1))); res.Added != 1 {
				t.Fatalf("retry after rollback: %+v", res)
			}
			if key := owaStoredKey(t, s, mailAcct, "message", "id-a"); key != 1 {
				t.Fatalf("rolled-back allocation consumed a key: %d", key)
			}
		})
	}
}

func TestCommitOWAMailLateFailurePreservesPriorArchive(t *testing.T) {
	s := newStore(t)
	held := owaMessage("held-id", "inbox", "<held@example.test>", 2)
	mustCommitOWA(t, s, owaBatch(mailT0, held))
	before, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore := rowCount(t, s, `select count(*) from mail_source_ids`)
	if _, err := s.db.Exec(`create trigger reject_late_native before insert on mail_recipients when new.name='reject' begin select raise(abort,'synthetic late child failure'); end`); err != nil {
		t.Fatal(err)
	}
	added := owaMessage("a-added", "sent", "<added@example.test>", 1)
	rejected := owaMessage("z-rejected", "sent", "<rejected@example.test>", 1)
	rejected.Value.Recipients[0].Name = "reject"
	held.Value.Flag = "flagged"
	res, err := s.CommitOWAMail(context.Background(), owaBatch(mailT0.Add(time.Hour), held, added, rejected))
	if err == nil || res != (MailResult{}) {
		t.Fatalf("late failure exposed partial result: %+v / %v", res, err)
	}
	if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != registryBefore {
		t.Fatalf("late failure retained new identities: %d, want %d", n, registryBefore)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages`); n != 1 {
		t.Fatalf("late failure retained new messages: %d", n)
	}
	after, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil || after.Rowid != before.Rowid || after.Flag != "none" || after.BodyText != before.BodyText || !after.StateSeenAt.Equal(before.StateSeenAt) {
		t.Fatalf("late failure changed held mail: %+v / %v", after, err)
	}
	if n := rowCount(t, s, `select count(*) from mail_fts`); n != 1 {
		t.Fatalf("late failure changed FTS rows: %d", n)
	}
}

func TestCommitOWAMailRejectsInvalidAndCanceledBatch(t *testing.T) {
	s := newStore(t)
	b := owaBatch(mailT0, owaMessage("", "inbox", "<a@example.test>", 1))
	if res, err := s.CommitOWAMail(context.Background(), b); err == nil || res != (MailResult{}) {
		t.Fatalf("invalid identity: %+v / %v", res, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b = owaBatch(mailT0, owaMessage("id-a", "inbox", "<a@example.test>", 1))
	if res, err := s.CommitOWAMail(ctx, b); !errors.Is(err, context.Canceled) || res != (MailResult{}) {
		t.Fatalf("canceled transaction: %+v / %v", res, err)
	}
	if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 0 {
		t.Fatalf("invalid/canceled batch allocated %d identities", n)
	}
}

func TestCommitOWAMailUnavailableAttachmentFlagDoesNotInferOrErase(t *testing.T) {
	s := newStore(t)
	m := owaMessage("id-a", "inbox", "<a@example.test>", 1)
	m.Value.HasAttachments = nil
	m.AttachmentIDs = []string{"attachment"}
	m.Value.Attachments = []outlookmail.Attachment{{Name: "synthetic.txt", Size: 5}}
	mustCommitOWA(t, s, owaBatch(mailT0, m))
	get := func(want bool) {
		t.Helper()
		got, err := s.MailGet(context.Background(), mailAcct, 1)
		if err != nil || got.HasAttachments != want || len(got.Attachments) != 1 || got.Attachments[0].Downloaded {
			t.Fatalf("native attachment flag/metadata: %+v / %v, want flag %v", got, err, want)
		}
	}
	get(false)
	known := true
	m.Value.HasAttachments = &known
	mustCommitOWA(t, s, owaBatch(mailT0.Add(time.Hour), m))
	get(true)
	m.Value.HasAttachments = nil
	mustCommitOWA(t, s, owaBatch(mailT0.Add(2*time.Hour), m))
	get(true)
	known = false
	m.Value.HasAttachments = &known
	mustCommitOWA(t, s, owaBatch(mailT0.Add(3*time.Hour), m))
	get(false)
	m.Value.HasAttachments = nil
	mustCommitOWA(t, s, owaBatch(mailT0.Add(4*time.Hour), m))
	get(false)
	mac := mailMsg(99, fInbox, "<mac@example.test>", "Mac inference", 1)
	mac.Attachments = []outlookmail.Attachment{{Key: 8, Name: "synthetic.txt"}}
	mustCommitMail(t, s, mailBatch(mailT0, mac))
	got, err := s.MailGet(context.Background(), mailAcct, 99)
	if err != nil || !got.HasAttachments {
		t.Fatalf("Mac nil inference changed: %+v / %v", got, err)
	}
}

func TestCommitOWAMailAbsenceFreshnessAndReappearance(t *testing.T) {
	s := newStore(t)
	a := owaMessage("id-a", "inbox", "<a@example.test>", 3)
	b := owaMessage("id-b", "inbox", "<b@example.test>", 2)
	c := owaMessage("id-c", "inbox", "<c@example.test>", 1)
	mustCommitOWA(t, s, owaBatch(mailT0, a, b, c))
	missing := owaBatch(mailT0.Add(time.Hour), a, c)
	if res := mustCommitOWA(t, s, missing); res.Gone != 0 || res.Evicted != 0 {
		t.Fatalf("first miss confirmed absence: %+v", res)
	}
	missing.ReadAt = missing.ReadAt.Add(time.Hour)
	if res := mustCommitOWA(t, s, missing); res.Gone != 0 || res.Evicted != 0 {
		t.Fatalf("same freshness confirmed absence: %+v", res)
	}
	missing.FreshAt = missing.FreshAt.Add(time.Hour)
	if res := mustCommitOWA(t, s, missing); res.Gone != 1 || res.Evicted != 0 {
		t.Fatalf("second distinct trusted miss: %+v", res)
	}
	if res := mustCommitOWA(t, s, owaBatch(mailT0.Add(4*time.Hour), a, b, c)); res.Updated != 1 {
		t.Fatalf("reappearance: %+v", res)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages where gone_at is not null or evicted_at is not null`); n != 0 {
		t.Fatalf("reappearance left %d marks", n)
	}
}

func TestCommitOWAMailUntrustedAndSeenMissingDoNotJudgeAbsence(t *testing.T) {
	for _, mode := range []string{"untrusted", "doubtful", "loss", "seen unmapped", "root changed"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			a := owaMessage("id-a", "inbox", "<a@example.test>", 3)
			b := owaMessage("id-b", "inbox", "<b@example.test>", 2)
			c := owaMessage("id-c", "inbox", "<c@example.test>", 1)
			mustCommitOWA(t, s, owaBatch(mailT0, a, b, c))
			missing := owaBatch(mailT0.Add(time.Hour), a, c)
			switch mode {
			case "untrusted":
				missing.Trusted = false
			case "doubtful":
				missing.Result.Doubtful = true
			case "loss":
				missing.Result.Losses = []outlookmail.Loss{{Code: outlookmail.CodeMailUnmapped, Count: 1}}
			case "seen unmapped":
				missing.Result.SeenIDs = append(missing.Result.SeenIDs, "id-b")
			case "root changed":
				missing.Result.RootID = "another-root"
				for i := range missing.Result.Folders {
					missing.Result.Folders[i].ParentID = "another-root"
				}
			}
			mustCommitOWA(t, s, missing)
			if n := rowCount(t, s, `select count(*) from mail_absent`); n != 0 {
				t.Fatalf("%s recorded %d unsafe absences", mode, n)
			}
			if n := rowCount(t, s, `select count(*) from mail_messages where gone_at is not null or evicted_at is not null`); n != 0 {
				t.Fatalf("%s marked %d missing rows", mode, n)
			}
		})
	}
}

func TestCommitOWAMailUniqueNativeMovePreservesRow(t *testing.T) {
	s := newStore(t)
	old := owaMessage("old-id", "inbox", "<move@example.test>", 1)
	mustCommitOWA(t, s, owaBatch(mailT0, old))
	before, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil {
		t.Fatal(err)
	}
	moved := old
	moved.ID, moved.FolderID = "new-id", "sent"
	res := mustCommitOWA(t, s, owaBatch(mailT0.Add(time.Hour), moved))
	key := owaStoredKey(t, s, mailAcct, "message", "new-id")
	after, err := s.MailGet(context.Background(), mailAcct, key)
	if err != nil || res != (MailResult{Updated: 1}) || after.Rowid != before.Rowid || after.FolderKind != "sent" || after.BodyText != before.BodyText {
		t.Fatalf("native cross-folder move: %+v / %+v / %v", res, after, err)
	}
	if n := rowCount(t, s, `select count(*) from mail_fts where rowid=?`, before.Rowid); n != 1 {
		t.Fatal("native move lost FTS identity")
	}
	mustCommitOWA(t, s, owaBatch(mailT0.Add(2*time.Hour), old))
	again, err := s.MailGet(context.Background(), mailAcct, 1)
	if err != nil || again.Rowid != before.Rowid {
		t.Fatalf("native alias reappearance: %+v / %v", again, err)
	}
}

func TestCommitOWAMailNativeMoveNeverConsumesSeenOrAmbiguousRows(t *testing.T) {
	for _, mode := range []string{"seen unmapped old identity", "ambiguous old copies", "ambiguous new copies"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			old := owaMessage("old-id", "inbox", "<copy@example.test>", 2)
			seed := []outlookowa.Message{old}
			if mode == "ambiguous old copies" {
				another := old
				another.ID = "another-old"
				seed = append(seed, another)
			}
			mustCommitOWA(t, s, owaBatch(mailT0, seed...))
			key := owaStoredKey(t, s, mailAcct, "message", "old-id")
			before, err := s.MailGet(context.Background(), mailAcct, key)
			if err != nil {
				t.Fatal(err)
			}
			incoming := old
			incoming.ID, incoming.FolderID = "new-id", "sent"
			b := owaBatch(mailT0.Add(time.Hour), incoming)
			wantAdded := 1
			switch mode {
			case "seen unmapped old identity":
				b.Result.SeenIDs = append(b.Result.SeenIDs, "old-id")
				b.Result.Doubtful = true
			case "ambiguous new copies":
				another := incoming
				another.ID = "another-new"
				b.Result.Messages = append(b.Result.Messages, another)
				b.Result.SeenIDs = append(b.Result.SeenIDs, another.ID)
				wantAdded = 2
			}
			res := mustCommitOWA(t, s, b)
			after, err := s.MailGet(context.Background(), mailAcct, key)
			if err != nil || res.Added != wantAdded || res.Updated != 0 || after.Rowid != before.Rowid {
				t.Fatalf("unsafe native re-key: %+v / %+v / %v", res, after, err)
			}
			if n := rowCount(t, s, `select count(*) from mail_messages`); n != len(seed)+wantAdded {
				t.Fatalf("native copies collapsed: %d", n)
			}
		})
	}
}

func TestCommitOWAMailEvictionUsesCachedRange(t *testing.T) {
	s := newStore(t)
	old := owaMessage("id-old", "inbox", "<old@example.test>", 5)
	newer := owaMessage("id-new", "inbox", "<new@example.test>", 1)
	mustCommitOWA(t, s, owaBatch(mailT0, old, newer))
	mustCommitOWA(t, s, owaBatch(mailT0.Add(time.Hour), newer))
	res := mustCommitOWA(t, s, owaBatch(mailT0.Add(2*time.Hour), newer))
	if res.Evicted != 1 || res.Gone != 0 {
		t.Fatalf("old cache eviction was called deletion: %+v", res)
	}
	if n := rowCount(t, s, `select count(*) from mail_messages where evicted_at is not null and gone_at is null`); n != 1 {
		t.Fatalf("eviction marks = %d", n)
	}
}

func TestCommitOWAMailWithholdsBulkLoss(t *testing.T) {
	s := newStore(t)
	messages := []outlookowa.Message{owaMessage("anchor-old", "inbox", "<old@example.test>", 3), owaMessage("anchor-new", "inbox", "<new@example.test>", 1)}
	for i := 0; i < 21; i++ {
		messages = append(messages, owaMessage(fmt.Sprintf("missing-%02d", i), "inbox", fmt.Sprintf("<missing-%d@example.test>", i), 2))
	}
	mustCommitOWA(t, s, owaBatch(mailT0, messages...))
	res := mustCommitOWA(t, s, owaBatch(mailT0.Add(time.Hour), messages[0], messages[1]))
	if res.Withheld != 21 || res.Gone != 0 || res.Evicted != 0 {
		t.Fatalf("bulk-loss withholding: %+v", res)
	}
	if n := rowCount(t, s, `select count(*) from mail_absent`); n != 0 {
		t.Fatalf("withheld batch remembered %d absences", n)
	}
}
