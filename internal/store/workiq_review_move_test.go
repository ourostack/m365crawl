package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQCacheFirstFolderMoveWriteFailureRollsBack(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := setupWorkIQMailControl(t, s, true)
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Hour)))
	verifyWorkIQTestAttempt(t, s, run)
	must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "archive-id", WellKnown: "archive"}))
	must0(s.StageWorkIQMailItems(ctx, run, "archive-id", []json.RawMessage{graphMailFixture("moved", "archive-id", "one@example.test")}, mailT0.Add(time.Hour)))
	settleWorkIQMailFixture(t, s, run)
	must(s.db.ExecContext(ctx, `create trigger fail_cache_first_move before update of folder_key on mail_messages
when new.folder_key<>old.folder_key begin select raise(abort,'synthetic move write failure'); end`))
	before := dump(t, s)
	_, err := s.CommitWorkIQMailGroup(ctx, run, "one@example.test")
	if err == nil || !strings.Contains(err.Error(), "synthetic move write failure") || dump(t, s) != before {
		t.Fatal("folder write failure lost its cause or partially committed the proven move", err)
	}
}

func TestWorkIQCacheFirstBodyDoesNotPreventProvenServerFolderMove(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := setupWorkIQMailControl(t, s, true)
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	before := must(s.MailGet(ctx, mailAcct, 100))
	if before.BodyText != "body of Subject" || before.FolderKind != "inbox" || before.FolderKey != fInbox || before.DetailKey != 100 {
		t.Fatal("fixture did not retain the cache-first body and folder")
	}
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
	observed := mailT0.Add(time.Hour)
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, observed))
	verifyWorkIQTestAttempt(t, s, run)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, run, 0, 128)
	must0(err)
	stage := func(resource string, data any) {
		t.Helper()
		raw := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{
			map[string]any{"statusCode": 200, "data": data},
		}}}))
		page := must(workiq.DecodePage(ctx, raw))
		progress := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
		must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: run, Resource: resource,
			ExpectedRevision: progress.Revision, Page: page, ObservedAt: observed}))
		var id int64
		must0(s.db.QueryRowContext(ctx, `select max(id) from workiq_pages`).Scan(&id))
		if !must(s.PrepareWorkIQMailPage(ctx, run, id, 128)) {
			t.Fatal("actual move inventory page did not prepare")
		}
	}
	stage("/me/mailFolders/archive", map[string]any{"id": "archive-id", "displayName": "Archive", "childFolderCount": 0})
	stage("/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{
		map[string]any{"id": "inbox-id", "childFolderCount": 0},
		map[string]any{"id": "archive-id", "displayName": "Archive", "childFolderCount": 0},
	}})
	inbox := "/me/mailFolders/inbox-id/messages/delta"
	archive := "/me/mailFolders/archive-id/messages/delta"
	stage(inbox, map[string]any{"value": []json.RawMessage{[]byte(`{"id":"one","@removed":{"reason":"changed"}}`)},
		"@odata.deltaLink": inbox + "?$deltatoken=end"})
	stage(archive, map[string]any{"value": []json.RawMessage{graphMailFixture("moved", "archive-id", "one@example.test")},
		"@odata.deltaLink": archive + "?$deltatoken=end"})
	must0(s.SealWorkIQMailInventory(ctx, run, observed))
	for after, done := int64(0), false; !done; {
		after, done, err = s.CarryWorkIQMailInventory(ctx, run, after, 128)
		must0(err)
	}
	_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	after := must(s.MailGet(ctx, mailAcct, 100))
	if after.FolderKind != "archive" || after.BodyText != before.BodyText || after.Rowid != before.Rowid ||
		after.SenderAddress != before.SenderAddress || !after.SentAt.Equal(before.SentAt) ||
		len(after.Recipients) != len(before.Recipients) || rowCount(t, s, `select count(*) from mail_messages`) != 1 {
		t.Fatal("body precedence blocked the proven move or changed cache-first immutable identity")
	}
	batch := mailBatch(observed.Add(time.Hour), mailMsg(100, fInbox, "<one@example.test>", "Subject", 0))
	batch.FreshAt = mailT0
	must(s.CommitMail(ctx, batch))
	replayed := must(s.MailGet(ctx, mailAcct, 100))
	if replayed.FolderKey != after.FolderKey || replayed.BodyText != before.BodyText || replayed.Rowid != before.Rowid {
		t.Fatal("replaying the same native copy reversed the server move")
	}
	batch.FreshAt, batch.ReadAt = observed.Add(2*time.Hour), observed.Add(2*time.Hour)
	must(s.CommitMail(ctx, batch))
	newCopy := must(s.MailGet(ctx, mailAcct, 100))
	if newCopy.FolderKey != fInbox || newCopy.BodyText != before.BodyText || newCopy.Rowid != before.Rowid {
		t.Fatal("a genuinely newer native copy lost its own folder state or stable identity")
	}
}
