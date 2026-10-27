package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func TestSourceHealthMailModificationClock(t *testing.T) {
	for _, name := range []string{"body", "attachment added", "attachment downloaded", "recipients", "conversation", "native identity", "header flag", "state", "rekey", "replace", "reappeared", "folder renamed", "unchanged"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			m := mailMsg(1, fInbox, "<one@example.test>", "Synthetic", 1)
			m.Body = outlookmail.Body{State: outlookmail.BodyNone}
			m.Recipients = nil
			m.Attachments = []outlookmail.Attachment{{Key: 10, Name: "synthetic.txt"}}
			if name == "native identity" {
				m.MessageID = ""
			}
			mustCommitMail(t, s, mailBatch(mailT0, m))
			written := mailT0.Add(time.Hour)
			b := mailBatch(written, m)
			switch name {
			case "body":
				b.Result.Messages[0].Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>filled</p>")}
			case "attachment added":
				b.Result.Messages[0].Attachments = append(b.Result.Messages[0].Attachments, outlookmail.Attachment{Key: 11, Name: "second.txt"})
			case "attachment downloaded":
				b.Result.Messages[0].Attachments = []outlookmail.Attachment{{Key: 10, Name: "synthetic.txt", Downloaded: true}}
			case "recipients":
				b.Result.Messages[0].Recipients = []outlookmail.Recipient{{Address: "synthetic@example.test"}}
			case "conversation":
				b.Result.Messages[0].ConversationID = "synthetic-conversation"
			case "native identity":
				b.RekeyAcrossFolders = true
				b.Result.Messages[0].MessageID, b.Result.Messages[0].ICalUID = "<one@example.test>", "synthetic-invite"
			case "header flag":
				no := false
				b.Result.Messages[0].HasAttachments = &no
			case "state":
				b.Result.Messages[0].Flag = "flagged"
			case "rekey":
				b.Result.Messages[0].DetailKey = 2
			case "replace":
				b.Result.Messages[0].MessageID = "<replacement@example.test>"
			case "reappeared":
				must0(s.runInTx(ctx, []string{`update mail_messages set gone_at='2026-09-10T08:30:00.000Z'`}))
			case "folder renamed":
				b.Result.Folders[0].Name = "Renamed synthetic inbox"
			}
			mustCommitMail(t, s, b)
			got, err := s.sourceNewestItem(ctx, "outlook_mail", mailAcct)
			want := written
			if name == "unchanged" {
				want = mailT0
			}
			if err != nil || got == nil || !got.Equal(want) {
				t.Fatalf("mail modification clock=%v err=%v, want %s", got, err, want)
			}
			var state string
			must0(s.db.QueryRow(`select state_seen_at from mail_messages`).Scan(&state))
			if name != "state" && name != "replace" && name != "reappeared" && state != fmtTime(mailT0) {
				t.Fatalf("content write redefined published state_seen_at: %s", state)
			}
		})
	}
}

func TestSourceHealthMailDisappearanceClock(t *testing.T) {
	for _, name := range []string{"gone", "evicted"} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			keep := mailMsg(1, fInbox, "<keep@example.test>", "Keep", 2)
			missing := mailMsg(2, fInbox, "<missing@example.test>", "Missing", 1)
			if name == "evicted" {
				missing.Received = mailT0.AddDate(0, 0, -20)
			}
			mustCommitMail(t, s, mailBatch(mailT0, keep, missing))
			mustCommitMail(t, s, mailBatch(mailT0.Add(time.Hour), keep))
			written := mailT0.Add(2 * time.Hour)
			res := mustCommitMail(t, s, mailBatch(written, keep))
			if res.Gone+res.Evicted != 1 {
				t.Fatalf("expected one disappearance mark: %+v", res)
			}
			got, err := s.sourceNewestItem(ctx, "outlook_mail", mailAcct)
			if err != nil || got == nil || !got.Equal(written) {
				t.Fatalf("mark clock=%v err=%v, want %s", got, err, written)
			}
		})
	}
}

func TestSourceHealthMailRederivationClock(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<one@example.test>", "Synthetic", 1)))
	must0(s.runInTx(ctx, []string{`update mail_messages set body_text_version=0,body_text='stale'`}))
	before := time.Now().UTC().Truncate(time.Millisecond)
	_, err := s.Rederive(ctx)
	must0(err)
	got, err := s.sourceNewestItem(ctx, "outlook_mail", mailAcct)
	if err != nil || got == nil || got.Before(before) || got.After(time.Now()) {
		t.Fatalf("rederivation write clock=%v err=%v, want current archive write", got, err)
	}
}

func TestSourceHealthOldSchemaWithoutOptionalTables(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.RecordRun(ctx, Run{Source: "teams", Status: "failed", StartedAt: base, FinishedAt: base}))
	must0(s.runInTx(ctx, []string{`drop table calendar_source_events`, `drop table mail_messages`, `drop table mail_folders`, `drop table records`, `update schema_migrations set version=2`}))
	rows, err := s.SourceHealth(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID == nil {
		t.Fatalf("old-schema failure lost to optional-table absence: %+v %v", rows, err)
	}
	for _, family := range []string{"outlook_mail", "outlook_calendar"} {
		got, err := s.sourceNewestItem(ctx, family, "outlook/Legacy")
		if err != nil || got != nil {
			t.Fatalf("absent optional %s clock=%v err=%v", family, got, err)
		}
	}
}

func TestSourceHealthMailClockMigrationAndReadOnlyFallback(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	must0(err)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(1, fInbox, "<old@example.test>", "Synthetic old row", 1)))
	marked := mailT0.Add(time.Hour)
	_, err = s.db.Exec(`update mail_messages set gone_at=?`, fmtTime(marked))
	must0(err)
	must0(s.runInTx(ctx, []string{
		`alter table mail_messages drop column modified_at`,
		`alter table mail_folders drop column modified_at`,
		`update schema_migrations set version=19`,
	}))
	must0(s.Close())
	s, err = OpenReadOnly(ctx, path)
	must0(err)
	got, err := s.sourceNewestItem(ctx, "outlook_mail", mailAcct)
	if err != nil || got == nil || !got.Equal(marked) {
		t.Fatalf("legacy read clock=%v err=%v, want known mark %s", got, err, marked)
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='modified_at'`); n != 0 {
		t.Fatal("read-only clock migrated")
	}
	must0(s.Close())
	s, err = Open(ctx, path)
	must0(err)
	defer func() { _ = s.Close() }()
	var modified, state string
	must0(s.db.QueryRow(`select modified_at,state_seen_at from mail_messages`).Scan(&modified, &state))
	if modified != fmtTime(marked) || state != fmtTime(mailT0) {
		t.Fatalf("migration invented time or changed state clock: modified=%s state=%s", modified, state)
	}
	must0(s.runInTx(ctx, []string{
		`alter table mail_messages drop column modified_at`,
		`alter table mail_folders drop column modified_at`,
		`create trigger clock_migration_refusal before update on mail_messages begin select raise(fail,'synthetic clock migration refused'); end`,
	}))
	if err := s.migrate(ctx); err == nil {
		t.Fatal("migration failure was hidden")
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='modified_at'`); n != 0 {
		t.Fatal("failed clock migration committed part of its schema")
	}
	must0(s.runInTx(ctx, []string{`drop trigger clock_migration_refusal`}))
	must0(s.migrate(ctx))
	got, err = s.sourceNewestItem(ctx, "outlook_mail", mailAcct)
	if err != nil || got == nil || !got.Equal(marked) {
		t.Fatalf("retried migration lost known clock: %v %v", got, err)
	}
}

func TestSourceHealthMissingCurrentTablesAreErrors(t *testing.T) {
	for _, tc := range []struct{ table, family string }{
		{"mail_messages", "outlook_mail"}, {"calendar_source_events", "outlook_calendar"}, {"records", "teams"},
	} {
		t.Run(tc.table, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			must0(s.runInTx(ctx, []string{"drop table " + tc.table}))
			if _, err := s.sourceNewestItem(ctx, tc.family, "synthetic/account"); err == nil {
				t.Fatal("current-schema corruption became an absent optional clock")
			}
			if tc.table == "records" {
				if _, err := s.SourceHealth(ctx); err == nil {
					t.Fatal("missing records became healthy source history")
				}
			}
		})
	}
}
