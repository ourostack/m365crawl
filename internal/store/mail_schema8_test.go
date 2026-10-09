package store

import (
	"context"
	"database/sql"
	"testing"
)

func mailSchema7Archive(t *testing.T) (string, int64) {
	t.Helper()
	path := writableArchivePath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<schema7@example.test>", "schema preservation", 1)))
	var rowid int64
	if err := s.db.QueryRow(`select rowid from mail_messages where account=? and detail_key=21`, mailAcct).Scan(&rowid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into mail_absent(account,detail_key,first_missed_at,misses,fresh_at) values(?,22,'2026-09-10T08:00:00.000Z',1,'2026-09-10T08:00:00.000Z')`, mailAcct); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`drop index if exists mail_messages_conversation`); err != nil {
		t.Fatal(err)
	}
	for _, col := range []struct{ table, column string }{
		{"mail_messages", "conversation_id"},
		{"mail_recipients", "role"},
	} {
		var n int
		if err := s.db.QueryRow(`select count(*) from pragma_table_info(?) where name=?`, col.table, col.column).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			if _, err := s.db.Exec(`alter table ` + col.table + ` drop column ` + col.column); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.db.Exec(`drop table if exists mail_source_ids`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update schema_migrations set version=7`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, rowid
}

func TestMailSchema8FreshAndUpgrade(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		s := newStore(t)
		if v := rowCount(t, s, `select max(version) from schema_migrations`); v != 8 {
			t.Fatalf("schema version = %d, want 8", v)
		}
		assertMailSchema8(t, s)
	})
	t.Run("upgrade preserves mail", func(t *testing.T) {
		path, oldRowid := mailSchema7Archive(t)
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		assertMailSchema8(t, s)
		var rowid int64
		var conversation string
		if err := s.db.QueryRow(`select rowid,conversation_id from mail_messages where account=? and detail_key=21`, mailAcct).Scan(&rowid, &conversation); err != nil {
			t.Fatal(err)
		}
		if rowid != oldRowid || conversation != "" {
			t.Fatalf("rowid/conversation = %d/%q, want %d/empty", rowid, conversation, oldRowid)
		}
		if n := rowCount(t, s, `select count(*) from mail_recipients where message_rowid=? and role is null`, rowid); n != 1 {
			t.Fatalf("Mac null-role recipients = %d, want 1", n)
		}
		if n := rowCount(t, s, `select count(*) from mail_fts where rowid=? and mail_fts match 'preservation'`, rowid); n != 1 {
			t.Fatalf("preserved FTS rows = %d, want 1", n)
		}
		if n := rowCount(t, s, `select count(*) from mail_absent where account=? and detail_key=22 and misses=1`, mailAcct); n != 1 {
			t.Fatalf("preserved absence rows = %d, want 1", n)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		again, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = again.Close() }()
		assertMailSchema8(t, again)
	})
}

func assertMailSchema8(t *testing.T, s *Store) {
	t.Helper()
	for _, query := range []string{
		`select count(*) from pragma_table_info('mail_messages') where name='conversation_id'`,
		`select count(*) from pragma_table_info('mail_recipients') where name='role'`,
		`select count(*) from sqlite_master where type='table' and name='mail_source_ids'`,
		`select count(*) from sqlite_master where type='index' and name='mail_messages_conversation'`,
	} {
		if n := rowCount(t, s, query); n != 1 {
			t.Fatalf("schema object count = %d, want 1: %s", n, query)
		}
	}
}

func TestMailSchema8Constraints(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `insert into mail_source_ids(account,kind,source_id,key) values('a','message','A',1),('a','message','a',2)`); err != nil {
		t.Fatalf("case-distinct source ids: %v", err)
	}
	for _, q := range []string{
		`insert into mail_source_ids(account,kind,source_id,key) values('a','other','x',3)`,
		`insert into mail_source_ids(account,kind,source_id,key) values('a','message','x',0)`,
		`insert into mail_source_ids(account,kind,source_id,key) values('a','message','x',4294967296)`,
		`insert into mail_source_ids(account,kind,source_id,key) values('a','message','x',1.5)`,
		`insert into mail_source_ids(account,kind,source_id,key) values('a','message','x',1)`,
		`insert into mail_source_ids(account,kind,source_id,key) values('a','message','A',3)`,
		`insert into mail_recipients(message_rowid,ord,role) values(1,0,'replyto')`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err == nil {
			t.Fatalf("invalid registry/role insert succeeded: %s", q)
		}
	}
	for _, role := range []any{nil, "to", "cc", "bcc"} {
		if _, err := s.db.ExecContext(ctx, `insert into mail_recipients(message_rowid,ord,role) values(1,0,?)`, role); err != nil {
			t.Fatalf("valid role %v: %v", role, err)
		}
		if _, err := s.db.ExecContext(ctx, `delete from mail_recipients where message_rowid=1`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMailSchema8ReadOnlyNeedsUpgrade(t *testing.T) {
	path, _ := mailSchema7Archive(t)
	s, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	need, err := s.NeedsUpgrade(context.Background())
	if err != nil || !need {
		t.Fatalf("v7 read-only NeedsUpgrade = %t/%v, want true/nil", need, err)
	}
	if n := rowCount(t, s, `select count(*) from pragma_table_info('mail_recipients') where name='role'`); n != 0 {
		t.Fatal("read-only open changed the schema")
	}
}

func TestMailSchema8PartialRepairAndRollback(t *testing.T) {
	for _, partial := range []string{
		`alter table mail_messages add column conversation_id text not null default ''`,
		`alter table mail_recipients add column role text check(role in ('to','cc','bcc'))`,
	} {
		t.Run(partial, func(t *testing.T) {
			path, _ := mailSchema7Archive(t)
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
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			assertMailSchema8(t, s)
		})
	}
	t.Run("rollback and retry", func(t *testing.T) {
		path, _ := mailSchema7Archive(t)
		db, err := sql.Open("sqlite", sqliteFileURI(path, "mode=rw"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`create table mail_messages_conversation(x integer)`); err != nil {
			t.Fatal(err)
		}
		if s, err := Open(context.Background(), path); err == nil {
			_ = s.Close()
			t.Fatal("migration succeeded despite the index-name collision")
		}
		for _, table := range []string{"mail_messages", "mail_recipients"} {
			var n int
			if err := db.QueryRow(`select count(*) from pragma_table_info(?) where name in ('conversation_id','role')`, table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("migration left %d new columns on %s after failure", n, table)
			}
		}
		if _, err := db.Exec(`drop table mail_messages_conversation`); err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		assertMailSchema8(t, s)
	})
}
