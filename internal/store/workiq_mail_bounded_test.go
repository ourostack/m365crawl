package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQMailGroupNeverMarksAnUnrelatedCanonicalRow(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := seedWorkIQMailAttempt(t, s)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0), mailMsg(101, fInbox, "two@example.test", "Subject", 0)))
	prepareRealMailInventory(t, s, first, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test"), graphMailFixture("two", "inbox-id", "two@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "two@example.test"))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	must0(s.StageWorkIQMailFolder(ctx, next, workiq.Folder{ID: "inbox-id"}))
	must0(s.StageWorkIQMailItems(ctx, next, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0))
	settleWorkIQMailFixture(t, s, next)
	_, err := s.db.ExecContext(ctx, `create trigger block_unrelated_source before update on workiq_rows
when new.item_rowid=(select rowid from mail_messages where detail_key=101)
begin select raise(abort,'unbounded unrelated source mark'); end`)
	must0(err)
	_, err = s.db.ExecContext(ctx, `create trigger block_unrelated_public before update on mail_messages
when new.detail_key=101 begin select raise(abort,'unbounded unrelated public mark'); end`)
	must0(err)
	if _, err := s.CommitWorkIQMailGroup(ctx, next, "one@example.test"); err != nil {
		t.Fatalf("one bounded identity group touched an unrelated source/canonical row: %v", err)
	}
}

func TestWorkIQAbsentMailDoesNotExpandCacheMaintenanceSchema(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `alter table mail_messages rename column account to acct`)
	must0(err)
	if _, err := s.Rederive(ctx); err != nil {
		t.Fatalf("unattempted WorkIQ changed the legacy pre-source maintenance boundary: %v", err)
	}
}

func TestWorkIQMailMaintenanceDoesNotInterpretServerAsNative(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	var before string
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows where kind='mail'`).Scan(&before))
	_, err := s.db.ExecContext(ctx, `update meta set value='0' where key='mail_text_version'`)
	must0(err)
	_, err = s.Rederive(ctx)
	must0(err)
	var after, body string
	must0(s.db.QueryRowContext(ctx, `select server_json from workiq_rows where kind='mail'`).Scan(&after))
	must0(s.db.QueryRowContext(ctx, `select body_text from mail_messages`).Scan(&body))
	if before != after || body != "Server body" {
		t.Fatal("cache-only maintenance reinterpreted or discarded retained server content")
	}
}
