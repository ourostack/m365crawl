package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func TestWorkIQPreparationControlsFaultsRemainResumable(t *testing.T) {
	for _, mode := range []string{"folder", "message"} {
		for _, kind := range append(append([]string{}, faultKinds...), "rowsaffected") {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				for at := 1; at < 500; at++ {
					finished := false
					t.Run(fmt.Sprint(at), func(t *testing.T) {
						ctx := context.Background()
						s := newStore(t)
						attempt := seedWorkIQMailAttempt(t, s)
						resource := "/me/mailFolders?includeHiddenFolders=true"
						id := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}})
						if mode == "message" {
							if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
								t.Fatal("root setup failed")
							}
							resource = "/me/mailFolders/inbox-id/messages/delta()"
							id = stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, "@odata.deltaLink": resource + "?$deltatoken=end"})
						}
						inj := injectFaults(t, s)
						inj.arm(kind, at)
						done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128)
						fired := inj.disarm()
						if !fired {
							must0(err)
							if !done {
								t.Fatal("unfaulted preparation not complete")
							}
							finished = true
							return
						}
						if err == nil {
							t.Fatalf("fault %s/%d was swallowed", kind, at)
						}
						if kind != "scan" && !errors.Is(err, errInjected) {
							t.Fatalf("fault cause lost: %v", err)
						}
						p := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
						if p.Cursor != "" || p.AdmittedRevision != 0 || rowCount(t, s, `select count(*) from mail_messages`) != 0 {
							t.Fatal("failed preparation published canonical data or advanced the last-good cursor")
						}
						if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
							t.Fatal("partially staged evidence was not safely resumable")
						}
					})
					if finished {
						return
					}
				}
				t.Fatal("preparation fault sweep did not terminate")
			})
		}
	}
}

func TestWorkIQPreparationControlsRejectMalformedStoredProof(t *testing.T) {
	for _, edit := range []string{
		`update workiq_pages set observed_at=''`,
		`update workiq_pages set observed_at='bad'`,
		`update workiq_pages set data_json='{'`,
		`update workiq_pages set data_json='{"structuredContent":{"results":[{"statusCode":200,"data":{}}]}}'`,
		`update workiq_pages set state='admitted'`,
		`update workiq_frontiers set qualification='different'`,
		`update workiq_frontiers set scope='2026-01-01'`,
		`update workiq_frontiers set resource='https://invalid.test/'`,
		`update workiq_frontiers set resource='/me/messages'`,
		`update workiq_pages set prepared_attempt_id=1,prepared_offset=-1`,
		`update workiq_pages set prepared_attempt_id=1,prepared_offset=99`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}})
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err == nil || dump(t, s) != before {
				t.Fatal("malformed persisted proof was admitted or modified")
			}
		})
	}
}
