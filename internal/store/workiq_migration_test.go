package store

import (
	"context"
	"reflect"
	"strings"
	"testing"

	crawlstore "github.com/openclaw/crawlkit/store"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestWorkIQSettingsAndScope(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if enabled, err := s.WorkIQEnabled(ctx); err != nil || enabled {
		t.Fatal("new archive unexpectedly enabled WorkIQ")
	}
	for _, enabled := range []bool{true, true, false, false} {
		if err := s.SetWorkIQEnabled(ctx, enabled); err != nil {
			t.Fatal(err)
		}
		if got, err := s.WorkIQEnabled(ctx); err != nil || got != enabled {
			t.Fatalf("opt-in did not persist: %v %v", got, err)
		}
	}
	if got := rowCount(t, s, `select count(*) from sync_runs`); got != 0 {
		t.Fatal("configuration fabricated source attempts")
	}
}

func TestWorkIQMigrationPreservesCacheArchive(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	legacyDDL := strings.Replace(schemaDDL, workiqSchemaDDL, "", 1)
	cs, err := crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: legacyDDL, SchemaVersion: 20})
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{cs: cs, db: cs.DB()}
	// Integrated schema20 also runs the directional column migration after its DDL.
	must0(legacy.migrate(ctx))
	must0(legacy.ApplyAccount(ctx, acctA))
	_ = must(legacy.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "c1", "Chat", "Preserved")}))
	_ = must(legacy.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c1", "m1", "preserved message", base)}))
	message := mailMsg(21, fInbox, "<preserved@test.invalid>", "Preserved mail", 0)
	message.Attachments = []outlookmail.Attachment{{Key: 3, Name: "fixture.txt", Size: 10, ContentType: "text/plain"}}
	mustCommitMail(t, legacy, mailBatch(mailT0, message))
	mailBefore := must(legacy.MailGet(ctx, mailAcct, 21))
	messageRowid := rowCount(t, legacy, `select rowid from messages`)
	must0(legacy.RecordRun(ctx, Run{Source: "outlook|profile-1|mail", Status: "failed", ErrorCode: "outlook_profile_unreadable", StartedAt: base, FinishedAt: base, SourceAccounts: []string{mailAcct}}))
	_, err = cs.DB().ExecContext(ctx, `insert into meta(key,value) values('historical-clock','2026-09-01T12:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	must0(cs.Close())
	old := must(OpenReadOnly(ctx, path))
	if enabled, err := old.WorkIQEnabled(ctx); err != nil || enabled {
		t.Fatalf("old read-only source state: %v %v", enabled, err)
	}
	if rowCount(t, old, `select max(version) from schema_migrations`) != 20 ||
		rowCount(t, old, `select count(*) from sqlite_master where name like 'workiq_%'`) != 0 {
		t.Fatal("old read-only access migrated source state")
	}
	must0(old.Close())
	s := must(Open(ctx, path))
	defer func() { _ = s.Close() }()
	if got := rowCount(t, s, `select max(version) from schema_migrations`); got != 21 {
		t.Fatalf("migration version=%d, want21", got)
	}
	var oldClock string
	must0(s.db.QueryRowContext(ctx, `select value from meta where key='historical-clock'`).Scan(&oldClock))
	if oldClock != "2026-09-01T12:00:00.000Z" || rowCount(t, s, `select count(*) from workiq_attempts`) != 0 {
		t.Fatal("migration fabricated source observations")
	}
	mailAfter := must(s.MailGet(ctx, mailAcct, 21))
	if !reflect.DeepEqual(mailBefore, mailAfter) || rowCount(t, s, `select rowid from messages`) != messageRowid ||
		rowCount(t, s, `select count(*) from message_fts where rowid=? and message_fts match 'preserved'`, messageRowid) != 1 ||
		rowCount(t, s, `select count(*) from mail_fts where rowid=? and mail_fts match 'preserved'`, mailBefore.Rowid) != 1 {
		t.Fatal("migration changed canonical IDs, rowids, child rows, original clocks or FTS")
	}
	health := must(s.SourceHealth(ctx))
	if len(health) != 1 || health[0].LastErrorCode != "outlook_profile_unreadable" || health[0].LastOKAt != nil {
		t.Fatal("migration erased a local failure or invented a covering success")
	}
}

func TestWorkIQCurrentSchemaMissingTablesRefusedBeforeRepair(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "read-only"}[readOnly], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			path := s.cs.Path()
			_, err := s.db.ExecContext(ctx, `alter table workiq_aliases rename to retained_broken_workiq_aliases`)
			must0(err)
			must0(s.Close())
			open := Open
			if readOnly {
				open = OpenReadOnly
			}
			reopened, err := open(ctx, path)
			if err == nil {
				must0(reopened.Close())
				t.Fatal("current incomplete source state was accepted or recreated as checked-empty metadata")
			}
		})
	}
}
