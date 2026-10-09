package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestMailWindowsRekeyAcrossFoldersAndMacDefault(t *testing.T) {
	for _, windows := range []bool{false, true} {
		t.Run(map[bool]string{false: "Mac default", true: "Windows opt in"}[windows], func(t *testing.T) {
			s := newStore(t)
			old := mailMsg(101, fInbox, "<move@example.test>", "move preservation", 2)
			old.Attachments = []outlookmail.Attachment{{Key: 7, Name: "kept", Downloaded: false}}
			mustCommitMail(t, s, mailBatch(mailT0, old))
			before, err := s.MailGet(context.Background(), mailAcct, 101)
			if err != nil {
				t.Fatal(err)
			}
			moved := old
			moved.DetailKey, moved.FolderKey = 201, fDeleted
			moved.Folder = outlookmail.Folder{Key: fDeleted, Kind: "deleted"}
			b := mailBatch(mailT0.Add(time.Hour), moved)
			b.RekeyAcrossFolders = windows
			res := mustCommitMail(t, s, b)
			got, err := s.MailGet(context.Background(), mailAcct, 201)
			if err != nil {
				t.Fatal(err)
			}
			if windows {
				if res.Updated != 1 || res.Added != 0 || got.Rowid != before.Rowid || got.BodyText != before.BodyText || len(got.Recipients) != 1 || len(got.Attachments) != 1 {
					t.Fatalf("Windows re-key did not preserve row/children: %+v/%+v", res, got)
				}
				if n := rowCount(t, s, `select count(*) from mail_fts where rowid=? and mail_fts match 'preservation'`, before.Rowid); n != 1 {
					t.Fatal("re-key lost FTS")
				}
			} else if res.Added != 1 || got.Rowid == before.Rowid {
				t.Fatalf("Mac default changed: %+v/%+v", res, got)
			}
		})
	}
}

func TestMailWindowsRekeyRejectsAmbiguity(t *testing.T) {
	for _, item := range []struct {
		name       string
		old, newer []uint32
		folder     uint32
	}{
		{"two old same kind", []uint32{101, 102}, []uint32{201}, fInbox},
		{"two new same kind", []uint32{101}, []uint32{201, 202}, fInbox},
		{"two old across kinds", []uint32{101, 102}, []uint32{201}, fDeleted},
		{"old remains", []uint32{101}, []uint32{101, 201}, fInbox},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := newStore(t)
			old := make([]outlookmail.Message, len(item.old))
			for i, key := range item.old {
				old[i] = mailMsg(key, fInbox, "<copy@example.test>", "copies", 2)
			}
			mustCommitMail(t, s, mailBatch(mailT0, old...))
			newer := make([]outlookmail.Message, len(item.newer))
			for i, key := range item.newer {
				newer[i] = mailMsg(key, item.folder, "<copy@example.test>", "copies", 1)
			}
			b := mailBatch(mailT0.Add(time.Hour), newer...)
			b.RekeyAcrossFolders = true
			res := mustCommitMail(t, s, b)
			wantAdded := len(newer)
			if item.name == "old remains" {
				wantAdded--
			}
			if res.Added != wantAdded {
				t.Fatalf("ambiguous re-key collapsed copies: %+v, want %d additions", res, wantAdded)
			}
			if n := rowCount(t, s, `select count(*) from mail_messages`); n != len(old)+wantAdded {
				t.Fatalf("copies = %d, want %d", n, len(old)+wantAdded)
			}
		})
	}
}

func TestMailWindowsStableKeyAndUniqueSameKindMove(t *testing.T) {
	s := newStore(t)
	m := mailMsg(101, fInbox, "<move@example.test>", "stable move", 2)
	mustCommitMail(t, s, mailBatch(mailT0, m))
	before, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil {
		t.Fatal(err)
	}
	m.FolderKey, m.Folder = fDeleted, outlookmail.Folder{Key: fDeleted, Kind: "deleted"}
	b := mailBatch(mailT0.Add(time.Hour), m)
	b.RekeyAcrossFolders = true
	if res := mustCommitMail(t, s, b); res.Added != 0 || res.Updated != 1 {
		t.Fatalf("stable-key folder move = %+v", res)
	}
	m.DetailKey = 201
	b = mailBatch(mailT0.Add(2*time.Hour), m)
	b.RekeyAcrossFolders = true
	if res := mustCommitMail(t, s, b); res.Added != 0 || res.Updated != 1 {
		t.Fatalf("unique same-kind re-key = %+v", res)
	}
	got, err := s.MailGet(context.Background(), mailAcct, 201)
	if err != nil || got.Rowid != before.Rowid {
		t.Fatalf("move changed row identity: %+v/%v", got, err)
	}
	m.DetailKey = 101
	b = mailBatch(mailT0.Add(3*time.Hour), m)
	b.RekeyAcrossFolders = true
	if res := mustCommitMail(t, s, b); res.Added != 0 || res.Updated != 1 {
		t.Fatalf("old native identity reappearance = %+v", res)
	}
	got, err = s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || got.Rowid != before.Rowid {
		t.Fatalf("old identity reappearance changed row: %+v/%v", got, err)
	}
}

func TestMailWindowsSparseDetailLateFill(t *testing.T) {
	s := newStore(t)
	m := mailMsg(101, fInbox, "", "sparse cached detail", 2)
	m.Recipients = nil
	m.Body = outlookmail.Body{State: outlookmail.BodyNone}
	b := mailBatch(mailT0, m)
	b.RekeyAcrossFolders = true
	mustCommitMail(t, s, b)
	before, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil {
		t.Fatal(err)
	}
	m.MessageID = "<late@example.test>"
	m.ICalUID = "ABCDEF"
	m.ConversationID = "late-conversation"
	m.Recipients = []outlookmail.Recipient{{Name: "To", Address: "to@example.test", Role: "to"}}
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>late body</p>")}
	b = mailBatch(mailT0.Add(time.Hour), m)
	b.RekeyAcrossFolders = true
	mustCommitMail(t, s, b)
	got, err := s.MailGet(context.Background(), mailAcct, 101)
	if err != nil || got.Rowid != before.Rowid || got.InternetMessageID != m.MessageID || got.ICalUID != "abcdef" || got.ConversationID != m.ConversationID || got.BodyState != outlookmail.BodyInline || len(got.Recipients) != 1 || got.Recipients[0].Role != "to" {
		t.Fatalf("sparse detail failed to fill: %+v/%v", got, err)
	}
}
