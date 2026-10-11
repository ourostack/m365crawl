package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

func allocateMailSource(t *testing.T, s *Store, account, kind, id string) uint32 {
	t.Helper()
	var key uint32
	err := s.inTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		key, err = mailSourceKey(context.Background(), tx, account, kind, id)
		return err
	})
	if err != nil {
		t.Fatalf("allocate source identity: %v", err)
	}
	return key
}

func TestMailSourceKeySequenceAndScopes(t *testing.T) {
	s := newStore(t)
	for _, item := range []struct {
		account, kind, id string
		want              uint32
	}{
		{"outlook/a", "message", "A", 1},
		{"outlook/a", "message", "A", 1},
		{"outlook/a", "message", "a", 2},
		{"outlook/b", "message", "A", 1},
		{"outlook/a", "folder", "A", 1},
		{"outlook/a", "attachment", "A", 1},
	} {
		if got := allocateMailSource(t, s, item.account, item.kind, item.id); got != item.want {
			t.Fatalf("%s/%s/%s key = %d, want %d", item.account, item.kind, item.id, got, item.want)
		}
	}
	if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 5 {
		t.Fatalf("registry rows = %d, want 5", n)
	}
}

func TestMailSourceKeyExistingHighWater(t *testing.T) {
	for _, item := range []struct {
		name, kind, seed string
	}{
		{"message", "message", `insert into mail_messages(account,detail_key,folder_key,first_seen_at,state_seen_at) values('outlook/test',1000,1,'','')`},
		{"absent message", "message", `insert into mail_absent(account,detail_key,first_missed_at) values('outlook/test',1000,'')`},
		{"message registry", "message", `insert into mail_source_ids(account,kind,source_id,key) values('outlook/test','message','old',1000)`},
		{"folder", "folder", `insert into mail_folders(account,folder_key,parent_key) values('outlook/test',1000,0)`},
		{"parent folder", "folder", `insert into mail_folders(account,folder_key,parent_key) values('outlook/test',1,1000)`},
		{"message folder", "folder", `insert into mail_messages(account,detail_key,folder_key,first_seen_at,state_seen_at) values('outlook/test',1,1000,'','')`},
		{"coverage folder", "folder", `insert into mail_coverage(account,folder_key,read_at) values('outlook/test',1000,'')`},
		{"root folder", "folder", `insert into meta(key,value) values('outlook_mail_root:outlook/test','1000')`},
		{"folder registry", "folder", `insert into mail_source_ids(account,kind,source_id,key) values('outlook/test','folder','old',1000)`},
		{"attachment", "attachment", `insert into mail_messages(rowid,account,detail_key,folder_key,first_seen_at,state_seen_at) values(1,'outlook/test',1,1,'',''); insert into mail_attachments(message_rowid,attachment_key) values(1,1000)`},
		{"attachment registry", "attachment", `insert into mail_source_ids(account,kind,source_id,key) values('outlook/test','attachment','old',1000)`},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.db.Exec(item.seed); err != nil {
				t.Fatal(err)
			}
			if got := allocateMailSource(t, s, "outlook/test", item.kind, "new"); got != 1001 {
				t.Fatalf("key = %d, want 1001", got)
			}
			if got := allocateMailSource(t, s, "outlook/other", item.kind, "new"); got != 1 {
				t.Fatalf("other account key = %d, want 1", got)
			}
		})
	}
}

func TestMailSourceKeyExhaustion(t *testing.T) {
	s := newStore(t)
	if _, err := s.db.Exec(`insert into mail_source_ids(account,kind,source_id,key) values('outlook/a','message','old',4294967295)`); err != nil {
		t.Fatal(err)
	}
	if got := allocateMailSource(t, s, "outlook/a", "message", "old"); got != 4294967295 {
		t.Fatalf("existing maximum key = %d", got)
	}
	err := s.inTx(context.Background(), func(tx *sql.Tx) error {
		_, err := mailSourceKey(context.Background(), tx, "outlook/a", "message", "new")
		return err
	})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "outlook_mail_key_exhausted" {
		t.Fatalf("exhaustion = %v, want outlook_mail_key_exhausted", err)
	}
	if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 1 {
		t.Fatalf("registry changed on exhaustion: %d", n)
	}
}

func TestMailSourceKeyRollbackAndCancel(t *testing.T) {
	s := newStore(t)
	boom := errors.New("synthetic rollback")
	err := s.inTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := mailSourceKey(context.Background(), tx, "outlook/a", "message", "new"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback = %v, want synthetic error", err)
	}
	if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 0 {
		t.Fatalf("rollback retained %d mappings", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = s.inTx(context.Background(), func(tx *sql.Tx) error {
		_, err := mailSourceKey(ctx, tx, "outlook/a", "message", "new")
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v, want context.Canceled", err)
	}
	if got := allocateMailSource(t, s, "outlook/a", "message", "new"); got != 1 {
		t.Fatalf("first key after rollback = %d, want 1", got)
	}
}

func TestMailSourceKeyInvalidInputsAndStoredKeys(t *testing.T) {
	for _, input := range []struct{ account, kind, id string }{
		{"", "message", "x"},
		{"outlook/a", "unknown", "x"},
		{"outlook/a", "message", ""},
	} {
		t.Run("invalid input "+input.account+"/"+input.kind+"/"+input.id, func(t *testing.T) {
			s := newStore(t)
			err := s.inTx(context.Background(), func(tx *sql.Tx) error {
				_, err := mailSourceKey(context.Background(), tx, input.account, input.kind, input.id)
				return err
			})
			if err == nil || rowCount(t, s, `select count(*) from mail_source_ids`) != 0 {
				t.Fatalf("invalid input accepted or wrote a mapping: %v", err)
			}
		})
	}
	for _, seed := range []string{
		`insert into mail_absent(account,detail_key,first_missed_at) values('outlook/a',-1,'')`,
		`insert into mail_absent(account,detail_key,first_missed_at) values('outlook/a',1.5,'')`,
		`insert into mail_absent(account,detail_key,first_missed_at) values('outlook/a',4294967296,'')`,
		`insert into mail_absent(account,detail_key,first_missed_at) values('outlook/a','never-print-fixture-value','')`,
		`insert into meta(key,value) values('outlook_mail_root:outlook/a','never-print-fixture-value')`,
	} {
		t.Run("invalid stored key "+seed, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.db.Exec(seed); err != nil {
				t.Fatal(err)
			}
			kind := "message"
			if strings.Contains(seed, "outlook_mail_root") {
				kind = "folder"
			}
			err := s.inTx(context.Background(), func(tx *sql.Tx) error {
				_, err := mailSourceKey(context.Background(), tx, "outlook/a", kind, "new")
				return err
			})
			if err == nil || strings.Contains(err.Error(), "never-print-fixture-value") {
				t.Fatalf("stored-key error absent or leaked a value: %v", err)
			}
			if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 0 {
				t.Fatalf("invalid stored key created %d mappings", n)
			}
		})
	}
}

func TestMailSourceKeyDatabaseFailure(t *testing.T) {
	s := newStore(t)
	if _, err := s.db.Exec(`drop table mail_absent`); err != nil {
		t.Fatal(err)
	}
	err := s.inTx(context.Background(), func(tx *sql.Tx) error {
		_, err := mailSourceKey(context.Background(), tx, "outlook/a", "message", "new")
		return err
	})
	if err == nil {
		t.Fatal("missing archive table did not report a failure")
	}
}

func TestMailSourceKeyStoredMappingAndWriteFailures(t *testing.T) {
	t.Run("invalid existing mapping", func(t *testing.T) {
		s := newStore(t)
		err := s.inTx(context.Background(), func(tx *sql.Tx) error {
			if _, err := tx.Exec(`pragma ignore_check_constraints=on`); err != nil {
				return err
			}
			if _, err := tx.Exec(`insert into mail_source_ids(account,kind,source_id,key) values('outlook/a','message','broken',0)`); err != nil {
				return err
			}
			if _, err := tx.Exec(`pragma ignore_check_constraints=off`); err != nil {
				return err
			}
			_, err := mailSourceKey(context.Background(), tx, "outlook/a", "message", "broken")
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "invalid stored mail source identity key") {
			t.Fatalf("invalid mapping = %v", err)
		}
	})
	for _, item := range []struct{ name, kind, setup string }{
		{"missing root metadata", "folder", `drop table meta`},
		{"insert failure", "message", `create trigger reject_mail_source_id before insert on mail_source_ids begin select raise(abort,'synthetic identity failure'); end`},
	} {
		t.Run(item.name, func(t *testing.T) {
			s := newStore(t)
			if _, err := s.db.Exec(item.setup); err != nil {
				t.Fatal(err)
			}
			err := s.inTx(context.Background(), func(tx *sql.Tx) error {
				_, err := mailSourceKey(context.Background(), tx, "outlook/a", item.kind, "new")
				return err
			})
			if err == nil {
				t.Fatal("database failure was hidden")
			}
			if n := rowCount(t, s, `select count(*) from mail_source_ids`); n != 0 {
				t.Fatalf("failed allocation wrote %d mappings", n)
			}
		})
	}
}
