package store

import (
	"context"
	"encoding/json"
	"testing"
)

func prepareRealMailInventory(t *testing.T, s *Store, attempt int64, values []json.RawMessage) string {
	t.Helper()
	ctx := context.Background()
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := "/me/mailFolders?includeHiddenFolders=true"
	id := stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
		t.Fatal("actual root inventory preparation failed")
	}
	resource := "/me/mailFolders/inbox-id/messages/delta"
	id = stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": values, "@odata.deltaLink": resource + "?$deltatoken=next"})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
		t.Fatal("actual message inventory preparation failed")
	}
	must0(s.SealWorkIQMailInventory(ctx, attempt, mailT0))
	return resource
}

func TestWorkIQIncrementalCensusRetainsOldCopiesBeforeNewIdentityMatching(t *testing.T) {
	for _, move := range []bool{false, true} {
		t.Run(map[bool]string{false: "retained-duplicate", true: "explicit-move"}[move], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			first := seedWorkIQMailAttempt(t, s)
			resource := prepareRealMailInventory(t, s, first, []json.RawMessage{graphMailFixture("old", "inbox-id", "one@example.test")})
			_ = must(s.CommitWorkIQMailGroup(ctx, first, "one@example.test"))
			must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
			must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
			var sight string
			must0(s.db.QueryRowContext(ctx, `select server_seen_at from workiq_rows where graph_id='old'`).Scan(&sight))
			run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
			verifyWorkIQTestAttempt(t, s, run)
			values := []json.RawMessage{graphMailFixture("new", "inbox-id", "one@example.test")}
			if move {
				values = append(values, []byte(`{"id":"old","@removed":{}}`))
			}
			prepareRealMailInventory(t, s, run, values)
			if result := must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test")); !result.Pending {
				t.Fatal("incremental group matched before retained copies were carried")
			}
			after, done, err := s.CarryWorkIQMailInventory(ctx, run, 0, 1)
			must0(err)
			if done || after != 1 {
				t.Fatal("bounded retained census failed to advance its canonical row")
			}
			_, done, err = s.CarryWorkIQMailInventory(ctx, run, after, 1)
			must0(err)
			if !done {
				t.Fatal("bounded retained census did not finish")
			}
			result := must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
			want := 2
			if move {
				want = 1
			}
			if rowCount(t, s, `select count(*) from mail_messages`) != want || !move && result.Gaps["workiq_mail_identity_ambiguous"] != 1 {
				t.Fatal("retained incremental duplicate was merged or unique explicit move duplicated")
			}
			if !move {
				var retainedSight string
				must0(s.db.QueryRowContext(ctx, `select server_seen_at from workiq_rows where graph_id='old'`).Scan(&retainedSight))
				if sight != retainedSight {
					t.Fatal("carrying old census data fabricated a new live sighting")
				}
			}
		})
	}
}
