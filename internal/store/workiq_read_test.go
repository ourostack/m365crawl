package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	crawlstore "github.com/openclaw/crawlkit/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func requireWorkIQReadOrigin(t *testing.T, item any, want string) {
	t.Helper()
	field := reflect.ValueOf(item).FieldByName("Provenance")
	if !field.IsValid() || field.Kind() != reflect.String || field.String() != want {
		t.Fatalf("canonical read did not publish independent source sightings: want %s", want)
	}
}

func TestWorkIQReadMessagesRetainSourceSightingsAcrossReadPaths(t *testing.T) {
	ctx := context.Background()
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-only", true: "both"}[mixed], func(t *testing.T) {
			s := newStore(t)
			_, _ = seedWorkIQBoundMessage(t, s, mixed)
			want := "workiq_server"
			if mixed {
				want = "both"
			}
			for _, selectRows := range []func() ([]MessageRow, error){
				func() ([]MessageRow, error) { rows, _, err := s.Messages(ctx, Filter{}); return rows, err },
				func() ([]MessageRow, error) { rows, _, err := s.Search(ctx, "server", Filter{}); return rows, err },
				func() ([]MessageRow, error) { rows, _, err := s.Thread(ctx, "chat", "one", Filter{}); return rows, err },
				func() ([]MessageRow, error) {
					return s.MessagesByKey(ctx, []string{acctA.TenantID + "|" + acctA.UserID + "|chat|one"})
				},
			} {
				rows := must(selectRows())
				if len(rows) != 1 {
					t.Fatal("canonical message read lost the source row")
				}
				requireWorkIQReadOrigin(t, rows[0], want)
			}
			must0(s.SetWorkIQEnabled(ctx, false))
			rows, _, err := s.Messages(ctx, Filter{})
			must0(err)
			requireWorkIQReadOrigin(t, rows[0], want)
		})
	}
}

func TestWorkIQReadConversationsDoNotConfuseWinnerWithOrigin(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQConversation(t, s)
	_ = must(s.CommitWorkIQConversation(ctx, attempt, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Source room"}`), base))
	rows, _, err := s.Conversations(ctx, "", "", Filter{})
	must0(err)
	requireWorkIQReadOrigin(t, rows[0], "workiq_server")
	_ = must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Native room")}))
	rows, _, err = s.Conversations(ctx, "", "", Filter{})
	must0(err)
	requireWorkIQReadOrigin(t, rows[0], "both")
}

func TestWorkIQReadMailPublishesEffectiveDeletionAndOrigin(t *testing.T) {
	ctx := context.Background()
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "server-only", true: "both"}[mixed], func(t *testing.T) {
			s := newStore(t)
			attempt := seedWorkIQMailAttempt(t, s)
			if mixed {
				mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
			}
			var data map[string]any
			must0(json.Unmarshal(graphMailFixture("one", "inbox-id", "one@example.test"), &data))
			data["deletedDateTime"] = "2026-09-10T08:01:00Z"
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{must(json.Marshal(data))})
			_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			var key uint32
			must0(s.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&key))
			row := must(s.MailGet(ctx, mailAcct, key))
			requireWorkIQReadOrigin(t, row, map[bool]string{false: "workiq_server", true: "both"}[mixed])
			field := reflect.ValueOf(row).FieldByName("DeletedAt")
			if !field.IsValid() || !field.Interface().(time.Time).Equal(time.Date(2026, 9, 10, 8, 1, 0, 0, time.UTC)) {
				t.Fatal("public mail lost its effective explicit deletion mark")
			}
		})
	}
}

func TestWorkIQReadOldArchiveRemainsNativeWithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	cs := must(crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: strings.Replace(strings.Replace(strings.Replace(schemaDDL, workiqSchemaDDL, "", 1), workiqDocumentSchemaDDL, "", 1), workiqWindowSchemaDDL, "", 1), SchemaVersion: 20}))
	legacy := &Store{cs: cs, db: cs.DB()}
	must0(legacy.migrate(ctx))
	must0(legacy.ApplyAccount(ctx, acctA))
	_ = must(legacy.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "chat", "Chat", "Native room")}))
	_ = must(legacy.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "chat", "one", "native message", base)}))
	mustCommitMail(t, legacy, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
	must0(legacy.Close())
	old := must(OpenReadOnly(ctx, path))
	defer func() { _ = old.Close() }()
	messages, _, err := old.Messages(ctx, Filter{})
	must0(err)
	requireWorkIQReadOrigin(t, messages[0], "desktop_cache")
	conversations, _, err := old.Conversations(ctx, "", "", Filter{})
	must0(err)
	requireWorkIQReadOrigin(t, conversations[0], "desktop_cache")
	requireWorkIQReadOrigin(t, must(old.MailGet(ctx, mailAcct, 100)), "desktop_cache")
	coverage := must(old.MailCoverage(ctx))
	if len(coverage) != 1 || coverage[0].Count != 1 || coverage[0].CountBasis != "" || coverage[0].CacheCount != nil {
		t.Fatal("old native-only read acquired source count semantics")
	}
	if rowCount(t, old, `select max(version) from schema_migrations`) != 20 ||
		rowCount(t, old, `select count(*) from sqlite_master where name like 'workiq_%'`) != 0 {
		t.Fatal("public provenance migrated an old read-only archive")
	}
}

func TestWorkIQReadCorruptSourceSightingsNeverBecomeNative(t *testing.T) {
	ctx := context.Background()
	for _, family := range []string{"message", "conversation", "mail"} {
		t.Run(family, func(t *testing.T) {
			s := newStore(t)
			var read func() error
			switch family {
			case "message":
				_, _ = seedWorkIQBoundMessage(t, s, false)
				read = func() error { _, _, err := s.Messages(ctx, Filter{}); return err }
			case "conversation":
				attempt := seedWorkIQConversation(t, s)
				_ = must(s.CommitWorkIQConversation(ctx, attempt, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group"}`), base))
				read = func() error { _, _, err := s.Conversations(ctx, "", "", Filter{}); return err }
			case "mail":
				s, _ = sourceMailIntegrityArchive(t)
				read = func() error { _, err := s.MailList(ctx, MailFilter{}); return err }
			}
			_, err := s.db.ExecContext(ctx, `update workiq_rows set cache_seen=0,server_seen=0`)
			must0(err)
			if err := read(); err == nil {
				t.Fatal("invalid source evidence was silently presented as native content")
			}
		})
	}
}

func TestWorkIQReadProjectionFailuresSurface(t *testing.T) {
	ops := map[string]func(context.Context, *Store) error{
		"message": func(ctx context.Context, s *Store) error { _, _, err := s.Messages(ctx, Filter{}); return err },
		"conversation": func(ctx context.Context, s *Store) error {
			_, _, err := s.Conversations(ctx, "", "", Filter{})
			return err
		},
		"mail": func(ctx context.Context, s *Store) error { _, err := s.MailList(ctx, MailFilter{}); return err },
	}
	setup := func(t *testing.T, s *Store) {
		seedReadable(t, s)
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, setup, op) })
	}
	s := newStore(t)
	if err := s.inTx(context.Background(), func(tx *sql.Tx) error {
		_, _, err := workIQReadColumns(context.Background(), tx, "record")
		return err
	}); err == nil {
		t.Fatal("unrelated records acquired a guessed source projection")
	}
}

func TestWorkIQReadProjectionTransactionsNeverReturnFailedRows(t *testing.T) {
	ops := map[string]func(context.Context, *Store) (bool, error){
		"message": func(ctx context.Context, s *Store) (bool, error) {
			rows, _, err := s.Messages(ctx, Filter{})
			return rows == nil, err
		},
		"conversation": func(ctx context.Context, s *Store) (bool, error) {
			rows, _, err := s.Conversations(ctx, "", "", Filter{})
			return rows == nil, err
		},
		"mail": func(ctx context.Context, s *Store) (bool, error) {
			rows, err := s.MailList(ctx, MailFilter{})
			return rows == nil, err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			for _, kind := range []string{"begin", "commit"} {
				t.Run(kind, func(t *testing.T) {
					s := newStore(t)
					seedReadable(t, s)
					mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
					inj := injectFaults(t, s)
					inj.arm(kind, 1)
					empty, err := op(context.Background(), s)
					fired := inj.disarm()
					if !fired || err == nil || !empty {
						t.Fatal("a failed content/source read transaction returned usable rows")
					}
				})
			}
		})
	}
}
