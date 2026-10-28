package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func workIQChatPageMessage(t *testing.T, id string, created *time.Time) json.RawMessage {
	t.Helper()
	data := map[string]any{"id": id, "messageType": "message",
		"lastModifiedDateTime":         base.Format(time.RFC3339Nano),
		"body":                         map[string]any{"contentType": "text", "content": "retained clipped message"},
		"@microsoft.graph.downloadUrl": "https://example.invalid/private?sig=synthetic-fixture"}
	if created != nil {
		data["createdDateTime"] = created.Format(time.RFC3339Nano)
	}
	return must(json.Marshal(data))
}

func seedWorkIQChatPage(t *testing.T, s *Store, values []json.RawMessage) (int64, int64, json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	scope := WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID, WindowStart: base.Add(-365 * 24 * time.Hour)}
	attempt := must(s.BeginWorkIQAttempt(ctx, scope, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	if values == nil {
		values = []json.RawMessage{}
	}
	envelope := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{
		map[string]any{"statusCode": 200, "data": map[string]any{"value": values}},
	}}}))
	page := must(workiq.DecodePage(ctx, envelope))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/chats/chat/messages", Page: page, ObservedAt: base}))
	var id int64
	must0(s.db.QueryRowContext(ctx, `select max(id) from workiq_pages`).Scan(&id))
	return attempt, id, []byte(`{"id":"chat","tenantId":"tenant-1","chatType":"group","topic":"Synthetic room"}`)
}

func TestWorkIQChatStoredPageClipsWindowAndWithholdsUnverifiedIDs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	old, current, future := base.Add(-2*365*24*time.Hour), base.Add(-time.Hour), base.Add(time.Hour)
	attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{
		workIQChatPageMessage(t, "old", &old), workIQChatPageMessage(t, "current", &current),
		workIQChatPageMessage(t, "future", &future), workIQChatPageMessage(t, "unknown-clock", nil),
	})
	if err := s.AdmitWorkIQFrontier(ctx, attempt, "/chats/chat/messages", 1); err == nil {
		t.Fatal("chat cursor advanced before actual stored-page preparation")
	}
	first := must(s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 2))
	if first.Complete || first.Seen != 2 || first.InWindow != 1 || first.OutsideWindow != 1 ||
		first.Gaps["workiq_message_identity_unverified"] != 1 || rowCount(t, s, `select count(*) from messages`) != 0 {
		t.Fatal("stored chat page stopped at the first old message, guessed native equality, or lost its exact window counts")
	}
	second := must(s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 2))
	if !second.Complete || second.Seen != 2 || second.OutsideWindow != 1 || second.UnknownClock != 1 ||
		second.Gaps["workiq_clock_unknown"] != 1 {
		t.Fatal("chat page resumption lost original offset, fixed window end, or unknown clock gap")
	}
	must0(s.AdmitWorkIQFrontier(ctx, attempt, "/chats/chat/messages", 1))
	if rowCount(t, s, `select count(*) from workiq_pages where state='admitted' and prepared_complete=1`) != 1 {
		t.Fatal("actual chat preparation did not produce durable frontier admission evidence")
	}
}

func TestWorkIQChatStoredPageResumeCannotMoveItsOriginalWindowEnd(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	current, future := base.Add(-time.Hour), base.Add(time.Hour)
	first, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{
		workIQChatPageMessage(t, "current", &current), workIQChatPageMessage(t, "future", &future),
	})
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: base}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID,
		WindowStart: base.Add(-365 * 24 * time.Hour)}, base.Add(2*time.Hour)))
	verifyWorkIQTestAttempt(t, s, next)
	result := must(s.PrepareWorkIQChatPage(ctx, next, page, chat, 128))
	if !result.Complete || result.InWindow != 1 || result.OutsideWindow != 1 {
		t.Fatal("resuming an old page advanced its original fixed window and admitted future source evidence")
	}
	var observed string
	must0(s.db.QueryRowContext(ctx, `select observed_at from workiq_pages where id=?`, page).Scan(&observed))
	if observed != base.Format(time.RFC3339Nano) {
		t.Fatal("chat resumption refreshed the original source observation")
	}
}

func TestWorkIQChatStoredPageQualifiedAtomicDataAndReplay(t *testing.T) {
	original := workIQMessageIDsQualified
	workIQMessageIDsQualified = func(workiq.MessageClass) bool { return true }
	t.Cleanup(func() { workIQMessageIDsQualified = original })
	s := newStore(t)
	ctx := context.Background()
	at := base.Add(-time.Hour)
	attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{workIQChatPageMessage(t, "one", &at)})
	result := must(s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128))
	if !result.Complete || result.Counts.Inserted != 1 || rowCount(t, s, `select count(*) from messages`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where kind='message' and cache_seen=0`) != 1 {
		t.Fatal("synthetic qualified preparation failed to atomically retain one source row and its page receipt")
	}
	before := dump(t, s)
	replay := must(s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128))
	if !replay.Complete || replay.Seen != 0 || dump(t, s) != before {
		t.Fatal("same prepared page replay refreshed or duplicated source facts")
	}
	if strings.Contains(before, "synthetic-fixture") {
		t.Fatal("qualified stored-page projection retained a download URL credential")
	}
}

func TestWorkIQChatStoredPageRejectsCorruptScopeClockEnvelopeAndOffset(t *testing.T) {
	for _, edit := range []string{
		`update workiq_pages set data_json='broken'`,
		`update workiq_pages set data_json='{"structuredContent":{"results":[{"statusCode":200,"data":{}}]}}'`,
		`update workiq_pages set data_json='{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[{"id":""}]}}]}}'`,
		`update workiq_pages set data_json='{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[{"id":"same"},{"id":"same"}]}}]}}'`,
		`update workiq_pages set state='retired'`,
		`update workiq_frontiers set resource='/chats/other/messages'`,
		`update workiq_frontiers set qualification='different'`,
		`update workiq_attempts set window_start='';update workiq_frontiers set scope=''`,
		`update workiq_attempts set window_start='bad';update workiq_frontiers set scope='bad'`,
		`update workiq_attempts set started_at='bad'`,
		`update workiq_attempts set window_end='bad'`,
		`update workiq_pages set observed_at='bad'`,
		`update workiq_pages set observed_at='0001-01-01T00:00:00Z'`,
		`update workiq_attempts set window_start='2026-09-01T13:00:00.000Z';update workiq_frontiers set scope='2026-09-01T13:00:00.000Z'`,
		`update workiq_pages set prepared_attempt_id=attempt_id,prepared_offset=-1`,
		`update workiq_pages set prepared_attempt_id=attempt_id,prepared_complete=1,prepared_offset=0`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			at := base.Add(-time.Hour)
			attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{workIQChatPageMessage(t, "one", &at)})
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128); err == nil || dump(t, s) != before {
				t.Fatal("corrupt source scope, window, page or offset admitted data or changed progress")
			}
		})
	}
	s := newStore(t)
	attempt, page, chat := seedWorkIQChatPage(t, s, nil)
	for _, input := range []struct {
		attempt, page int64
		chat          json.RawMessage
		limit         int
	}{
		{attempt, page, chat, 0}, {attempt, -1, chat, 1},
		{attempt, page, []byte(`{`), 1},
		{attempt, page, []byte(`{"id":"chat","tenantId":"other"}`), 1},
	} {
		if _, err := s.PrepareWorkIQChatPage(context.Background(), input.attempt, input.page, input.chat, input.limit); err == nil {
			t.Fatal("invalid chat preparation bound/owner/page was accepted")
		}
	}
	result := must(s.PrepareWorkIQChatPage(context.Background(), attempt, page, chat, 1))
	if !result.Complete || result.Seen != 0 {
		t.Fatal("an actually empty terminal page did not seal as empty")
	}
	must0(s.SetWorkIQEnabled(context.Background(), false))
	if _, err := s.PrepareWorkIQChatPage(context.Background(), attempt, page, chat, 1); err == nil {
		t.Fatal("disabled source prepared retained page data")
	}
}

func TestWorkIQChatStoredPageFailuresRollBackCanonicalRowsAndReceipt(t *testing.T) {
	original := workIQMessageIDsQualified
	workIQMessageIDsQualified = func(workiq.MessageClass) bool { return true }
	t.Cleanup(func() { workIQMessageIDsQualified = original })
	var attempt, page int64
	var chat json.RawMessage
	setup := func(t *testing.T, s *Store) {
		at := base.Add(-time.Hour)
		attempt, page, chat = seedWorkIQChatPage(t, s, []json.RawMessage{workIQChatPageMessage(t, "one", &at)})
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128)
		return err
	})
	s := newStore(t)
	setup(t, s)
	inj := injectFaults(t, s)
	inj.arm("rowsaffected", 1)
	_, err := s.PrepareWorkIQChatPage(context.Background(), attempt, page, chat, 128)
	if fired := inj.disarm(); !fired || err == nil || rowCount(t, s, `select count(*) from messages`) != 0 {
		t.Fatal("failed receipt CAS committed a canonical row")
	}
	blocked := newStore(t)
	setup(t, blocked)
	_, err = blocked.db.ExecContext(context.Background(), `create trigger block_chat_receipt before update of prepared_offset on workiq_pages
begin select raise(ignore); end`)
	must0(err)
	if _, err := blocked.PrepareWorkIQChatPage(context.Background(), attempt, page, chat, 128); err == nil ||
		rowCount(t, blocked, `select count(*) from messages`) != 0 {
		t.Fatal("lost page-offset CAS admitted data without its original receipt")
	}
}

func TestWorkIQChatStoredPageMapperAndGapFailureNeverAdvanceReceipt(t *testing.T) {
	ctx := context.Background()
	t.Run("qualified-invalid-version", func(t *testing.T) {
		original := workIQMessageIDsQualified
		workIQMessageIDsQualified = func(workiq.MessageClass) bool { return true }
		t.Cleanup(func() { workIQMessageIDsQualified = original })
		s := newStore(t)
		at := base.Add(-time.Hour)
		var data map[string]any
		must0(json.Unmarshal(workIQChatPageMessage(t, "one", &at), &data))
		data["lastModifiedDateTime"] = "bad"
		attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{must(json.Marshal(data))})
		before := dump(t, s)
		if _, err := s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128); err == nil || dump(t, s) != before {
			t.Fatal("a qualified invalid source clock partially advanced data/progress")
		}
	})
	t.Run("gap-persistence", func(t *testing.T) {
		s := newStore(t)
		at := base.Add(-time.Hour)
		attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{workIQChatPageMessage(t, "one", &at)})
		inj := injectFaults(t, s)
		inj.arm("exec", 2) // receipt reservation then the unverified-identity gap
		_, err := s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128)
		if fired := inj.disarm(); !fired || err == nil || rowCount(t, s, `select count(*) from workiq_pages where prepared_complete=1`) != 0 {
			t.Fatal("failed gap persistence was reported as prepared cursor evidence")
		}
	})
}

func TestWorkIQChatPendingPagesBindExactOwnerWindowAndAdmission(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt, page, chat := seedWorkIQChatPage(t, s, nil)
	ids := must(s.WorkIQPendingChatPages(ctx, attempt, "/chats/chat/messages", 0, 128))
	if len(ids) != 1 || ids[0] != page {
		t.Fatal("chat resumption lost its exact original stored page")
	}
	if _, err := s.WorkIQPendingMailPages(ctx, attempt, "/chats/chat/messages", 0, 128); err == nil {
		t.Fatal("mail traversal crossed the chat owner boundary")
	}
	must(s.PrepareWorkIQChatPage(ctx, attempt, page, chat, 128))
	must0(s.AdmitWorkIQFrontier(ctx, attempt, "/chats/chat/messages", 1))
	if ids := must(s.WorkIQPendingChatPages(ctx, attempt, "/chats/chat/messages", 0, 128)); len(ids) != 0 {
		t.Fatal("admitted chat pages were returned as pending backfill")
	}
}
