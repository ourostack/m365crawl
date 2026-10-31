package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestWorkIQInventoryControlsAtomicProofAndReceiptFaults(t *testing.T) {
	for _, mode := range []string{"seal", "seal-empty", "seal-child", "admit", "carry", "carry-unknown", "restart"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			resource := "/me/mailFolders/inbox-id/messages/delta"
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				attempt = seedWorkIQMailAttempt(t, s)
				if mode == "seal-empty" || mode == "seal-child" {
					_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
					must0(err)
					values := []any{}
					if mode == "seal-child" {
						values = append(values, map[string]any{"id": "a", "childFolderCount": 1})
					}
					id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": values})
					_, err = s.PrepareWorkIQMailPage(ctx, attempt, id, 128)
					must0(err)
					if mode == "seal-child" {
						id = stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders/a/childFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "b", "parentFolderId": "a", "childFolderCount": 0}}})
						_, err = s.PrepareWorkIQMailPage(ctx, attempt, id, 128)
						must0(err)
						for _, folder := range []string{"a", "b"} {
							path := "/me/mailFolders/" + folder + "/messages/delta"
							id = stageWorkIQInventoryPage(t, s, attempt, path, map[string]any{"value": []any{}, "@odata.deltaLink": path + "?$deltatoken=end"})
							_, err = s.PrepareWorkIQMailPage(ctx, attempt, id, 128)
							must0(err)
						}
					}
					return
				}
				header := "one@example.test"
				if mode == "carry-unknown" {
					header = ""
				}
				prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("old", "inbox-id", header)})
				if mode == "seal" {
					return
				}
				_ = must(s.CommitWorkIQMailGroup(ctx, attempt, header))
				if mode == "admit" {
					return
				}
				must0(s.AdmitWorkIQFrontier(ctx, attempt, resource, 1))
				must0(s.AdmitWorkIQFrontier(ctx, attempt, "/me/mailFolders?includeHiddenFolders=true", 1))
				must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
				attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
				verifyWorkIQTestAttempt(t, s, attempt)
				if mode == "carry" || mode == "carry-unknown" {
					prepareRealMailInventory(t, s, attempt, []json.RawMessage{})
				}
			}
			op := func(ctx context.Context, s *Store) error {
				switch mode {
				case "seal", "seal-empty", "seal-child":
					return s.SealWorkIQMailInventory(ctx, attempt, mailT0)
				case "admit":
					return s.AdmitWorkIQFrontier(ctx, attempt, resource, 1)
				case "restart":
					return s.RestartWorkIQFrontier(ctx, attempt, resource, 1)
				default:
					_, _, err := s.CarryWorkIQMailInventory(ctx, attempt, 0, 128)
					return err
				}
			}
			sweepFaults(t, setup, op)
			s := newStore(t)
			setup(t, s)
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, "rowsaffected", true, op, nil)
		})
	}
}

func TestWorkIQInventoryControlsRejectCorruptRetainedCarry(t *testing.T) {
	for _, edit := range []string{
		`update workiq_rows set server_projection='{'`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.ID','foreign')`,
		`update workiq_rows set server_seen_at='bad'`,
		`update workiq_rows set server_deleted_at='bad'`,
		`update workiq_rows set server_version='bad'`,
		`update workiq_rows set server_deleted_version='bad'`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			resource := prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("old", "inbox-id", "one@example.test")})
			_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
			must0(s.AdmitWorkIQFrontier(ctx, attempt, resource, 1))
			must0(s.AdmitWorkIQFrontier(ctx, attempt, "/me/mailFolders?includeHiddenFolders=true", 1))
			next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
			verifyWorkIQTestAttempt(t, s, next)
			prepareRealMailInventory(t, s, next, []json.RawMessage{})
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, _, err := s.CarryWorkIQMailInventory(ctx, next, 0, 128); err == nil || dump(t, s) != before {
				t.Fatal("corrupt source evidence was carried or partially committed")
			}
		})
	}
}
