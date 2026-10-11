package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestMailReadOnlyLegacyAndPartialColumns(t *testing.T) {
	for _, partial := range []string{
		"",
		`alter table mail_messages add column conversation_id text not null default ''`,
		`alter table mail_recipients add column role text check(role in ('to','cc','bcc'))`,
	} {
		t.Run(partial, func(t *testing.T) {
			path, oldRowid := mailSchema7Archive(t)
			if partial != "" {
				db, err := sql.Open("sqlite", sqliteFileURI(path, "mode=rw"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(partial); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			s, err := OpenReadOnly(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			before := rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='conversation_id'`) +
				rowCount(t, s, `select count(*) from pragma_table_info('mail_recipients') where name='role'`)
			rows, err := s.MailList(context.Background(), MailFilter{Account: mailAcct})
			if err != nil || len(rows) != 1 || rows[0].Rowid != oldRowid || rows[0].ConversationID != "" {
				t.Fatalf("legacy list = %+v/%v", rows, err)
			}
			m, err := s.MailGet(context.Background(), mailAcct, 21)
			if err != nil || len(m.Recipients) != 1 || m.Recipients[0].Role != "" || m.BodyText == "" {
				t.Fatalf("legacy get = %+v/%v", m, err)
			}
			found, truncated, err := s.MailSearch(context.Background(), "preservation", MailFilter{Account: mailAcct})
			if err != nil || truncated || len(found) != 1 {
				t.Fatalf("legacy search = %d/%t/%v", len(found), truncated, err)
			}
			thread, group, truncated, err := s.MailThread(context.Background(), mailAcct, 21)
			if err != nil || truncated || group != "subject" || len(thread) != 1 {
				t.Fatalf("legacy thread = %d/%s/%t/%v", len(thread), group, truncated, err)
			}
			after := rowCount(t, s, `select count(*) from pragma_table_info('mail_messages') where name='conversation_id'`) +
				rowCount(t, s, `select count(*) from pragma_table_info('mail_recipients') where name='role'`)
			if before != after || rowCount(t, s, `select max(version) from schema_migrations`) != 7 {
				t.Fatal("offline mail read upgraded the archive")
			}
		})
	}
}
