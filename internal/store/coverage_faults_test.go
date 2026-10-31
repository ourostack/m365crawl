package store

import (
	"context"
	"errors"
	"testing"
)

// Each coverage read reports a database failure instead of answering from what it could read.
func TestCoverageReadsReportDatabaseFailures(t *testing.T) {
	ctx := context.Background()
	fail := errors.New("meta unreadable")
	t.Run("span value", func(t *testing.T) {
		s := newStore(t)
		real := mailSpanValue
		t.Cleanup(func() { mailSpanValue = real })
		mailSpanValue = func(context.Context, rowQuerier, string) (string, bool, error) { return "", false, fail }
		if _, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x.test>", "a", 2))); !errors.Is(err, fail) {
			t.Fatalf("commit: %v", err)
		}
		if _, err := s.MailCoverageSpans(ctx, mailAcct); !errors.Is(err, fail) {
			t.Fatalf("read: %v", err)
		}
	})
	broken := func(t *testing.T, legacy bool, stmts ...string) *Store {
		t.Helper()
		s := newStore(t)
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<a@x.test>", "a", 5), mailMsg(2, fInbox, "<b@x.test>", "b", 1)))
		if legacy {
			must(s.db.ExecContext(ctx, `delete from meta where key=?`, mailSpanKey+mailAcct))
			must0(s.RecordRun(ctx, Run{StartedAt: mailT0, FinishedAt: mailT0, Source: "outlook|profile-1|mail", Status: "ok"}))
		}
		for _, q := range stmts {
			must(s.db.ExecContext(ctx, q))
		}
		return s
	}
	for name, s := range map[string]func(t *testing.T) *Store{
		"write time": func(t *testing.T) *Store {
			return broken(t, false, `drop table calendar_sources`, `create view calendar_sources as select 1 as source, 1 as account_id, 1 as synced_at`)
		},
		"sent shift": func(t *testing.T) *Store {
			return broken(t, false, `alter table mail_folders rename to mail_folders_real`, `create view mail_folders as select account from mail_folders_real`)
		},
		"legacy run": func(t *testing.T) *Store {
			return broken(t, true, `drop table sync_runs`, `create view sync_runs as select 1 as id`)
		},
		"legacy ranges": func(t *testing.T) *Store {
			return broken(t, true, `alter table mail_coverage rename to mail_coverage_real`, `create view mail_coverage as select account from mail_coverage_real`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s(t).MailCoverageSpans(ctx, mailAcct); err == nil {
				t.Fatal("a broken archive answered")
			}
		})
	}
	chat := func(t *testing.T, stmts ...string) {
		t.Helper()
		s := newStore(t)
		covConv(t, s, "19:c@unq.gbl.spaces", "Chat", covT0.Add(1))
		covMsg(t, s, "19:c@unq.gbl.spaces", "c1", covSelf, "Text", covT0.Add(1), 1, false)
		for _, q := range stmts {
			must(s.db.ExecContext(ctx, q))
		}
		if _, err := s.ChatEvidence(ctx, covTenant, covUser, ChatScopeChats, covT0, covT0.AddDate(0, 0, 1)); err == nil {
			t.Fatal("a broken archive answered")
		}
	}
	t.Run("conversations", func(t *testing.T) {
		chat(t, `alter table conversations rename to conversations_real`, `create view conversations as select tenant_id, user_id, id, kind from conversations_real`)
	})
	t.Run("sequence", func(t *testing.T) {
		chat(t, `alter table messages rename to messages_real`,
			`create view messages as select tenant_id, user_id, conversation_id, id, sender_id, message_type, sent_at, deleted_at from messages_real`)
	})
	t.Run("teams reads", func(t *testing.T) {
		s := newStore(t)
		must(s.db.ExecContext(ctx, `drop table sync_runs`))
		must(s.db.ExecContext(ctx, `create view sync_runs as select 1 as id`))
		if _, err := s.TeamsReads(ctx, covAccount); err == nil {
			t.Fatal("a broken archive answered")
		}
	})
	t.Run("teams accounts", func(t *testing.T) {
		s := newStore(t)
		must(s.db.ExecContext(ctx, `alter table accounts rename to accounts_real`))
		must(s.db.ExecContext(ctx, `create view accounts as select tenant_id, user_id from accounts_real`))
		if _, err := s.TeamsReads(ctx, covAccount); err == nil {
			t.Fatal("a broken archive answered")
		}
	})
}
