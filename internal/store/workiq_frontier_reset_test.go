package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQExpiredFrontierRetainsOldEvidenceAndRequiresFreshAttempt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := seedWorkIQMailAttempt(t, s)
	resource := prepareRealMailInventory(t, s, first, []json.RawMessage{graphMailFixture("old", "inbox-id", "one@example.test")})
	_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
	must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
	old := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
	before := dump(t, s)
	if err := s.RestartWorkIQFrontier(ctx, first, resource, 1); err == nil || dump(t, s) != before {
		t.Fatal("partly admitted attempt could discard its own proof")
	}
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "partial", ErrorCode: "workiq_delta_expired", FinishedAt: mailT0}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	if err := s.RestartWorkIQFrontier(ctx, next, resource, 0); err == nil {
		t.Fatal("stale revision reset a newer last-good cursor")
	}
	must0(s.RestartWorkIQFrontier(ctx, next, resource, 1))
	p := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
	if p.ID != 0 {
		t.Fatal("expired active frontier remained resumable")
	}
	var cursor string
	must0(s.db.QueryRowContext(ctx, `select cursor from workiq_frontiers where id=? and retired=1`, old.ID).Scan(&cursor))
	if cursor != old.Cursor || rowCount(t, s, `select count(*) from workiq_pages where frontier_id=?`, old.ID) != 1 {
		t.Fatal("restart erased last-good cursor or prior page evidence")
	}
	stamp := mailT0.Add(123456789)
	page := must(workiq.DecodePage(ctx, must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{
		"statusCode": 200, "data": map[string]any{"value": []any{}, "@odata.deltaLink": resource + "?$deltatoken=fresh"},
	}}}}))))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: next, Resource: resource, ExpectedRevision: 0, ObservedAt: stamp,
		Page: page}))
	p = must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
	if p.ID == old.ID || p.Revision != 1 || !p.Baseline || p.Cursor != "" {
		t.Fatal("restart failed to create a fresh baseline generation")
	}
	var observed string
	must0(s.db.QueryRowContext(ctx, `select observed_at from workiq_pages where frontier_id=?`, p.ID).Scan(&observed))
	if observed != workIQTimeValue(&stamp) {
		t.Fatal("durable page observation lost its original clock precision")
	}
}
