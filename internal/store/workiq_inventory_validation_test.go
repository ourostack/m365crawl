package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQInventoryValidationScopeAndPrivateProgress(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	chat := seedWorkIQConversation(t, s)
	for _, bad := range []int64{-1, chat} {
		if _, err := s.PrepareWorkIQMailPage(ctx, bad, 0, 1); err == nil {
			t.Fatal("unverified/non-mail page preparation accepted")
		}
		if err := s.SealWorkIQMailInventory(ctx, bad, mailT0); err == nil {
			t.Fatal("unverified/non-mail inventory sealed")
		}
		if _, _, err := s.CarryWorkIQMailInventory(ctx, bad, 0, 1); err == nil {
			t.Fatal("unverified/non-mail inventory carried")
		}
	}
	if err := s.SealWorkIQMailInventory(ctx, attempt, time.Time{}); err == nil {
		t.Fatal("unknown census completion clock accepted")
	}
	for _, limit := range []int{0, 129} {
		if _, err := s.PrepareWorkIQMailPage(ctx, attempt, 0, limit); err == nil {
			t.Fatal("unbounded preparation accepted")
		}
		if _, _, err := s.CarryWorkIQMailInventory(ctx, attempt, 0, limit); err == nil {
			t.Fatal("unbounded carry accepted")
		}
	}
	if _, _, err := s.CarryWorkIQMailInventory(ctx, attempt, -1, 1); err == nil {
		t.Fatal("negative carry cursor accepted")
	}
	if _, err := s.WorkIQProgressFor(ctx, WorkIQScope{}, "https://invalid.test"); err == nil {
		t.Fatal("foreign progress path accepted")
	}
	for _, bad := range []int64{-1, attempt} {
		if err := s.AdmitWorkIQFrontier(ctx, bad, "https://invalid.test", 0); err == nil {
			t.Fatal("unverified/foreign admission accepted")
		}
		if err := s.RestartWorkIQFrontier(ctx, bad, "https://invalid.test", 0); err == nil {
			t.Fatal("unverified/foreign restart accepted")
		}
	}
	private := WorkIQProgress{Cursor: "opaque-private", PendingCursor: "pending-private"}
	text := fmt.Sprintf("%v %#v", private, private) + string(must(json.Marshal(private)))
	if strings.Contains(text, "opaque-private") || strings.Contains(text, "pending-private") {
		t.Fatal("progress formatting disclosed private cursor bytes")
	}
}

func TestWorkIQInventoryValidationFixedKindsAndRepeatedPreparation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	for _, name := range []string{"inbox", "sentitems", "deleteditems", "drafts", "archive", "junkemail"} {
		id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders/"+name, map[string]any{"id": name, "childFolderCount": 0})
		if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
			t.Fatal("known fixed folder did not prepare")
		}
		before := dump(t, s)
		if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done || dump(t, s) != before {
			t.Fatal("prepared page replay changed retained state")
		}
	}
}

func TestWorkIQInventoryValidationReceiptCASAndWindow(t *testing.T) {
	for _, mode := range []string{"prepare", "admit", "restart", "invalid-window", "window", "terminal-delta"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			resource := "/me/mailFolders/inbox-id/messages/delta"
			if mode == "prepare" {
				id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{}})
				_, err := s.db.ExecContext(ctx, `create trigger block_receipt before update on workiq_pages begin select raise(ignore); end`)
				must0(err)
				if _, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err == nil {
					t.Fatal("missing receipt CAS was accepted")
				}
				return
			}
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{})
			if mode == "terminal-delta" {
				_, err := s.db.ExecContext(ctx, `update workiq_pages set delta_cursor='' where frontier_id=(select id from workiq_frontiers where resource=?)`, resource)
				must0(err)
				if err := s.AdmitWorkIQFrontier(ctx, attempt, resource, 1); err == nil {
					t.Fatal("delta admission without terminal link accepted")
				}
				if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil {
					t.Fatal("census without terminal link sealed")
				}
				return
			}
			if mode == "window" || mode == "invalid-window" {
				window := "bad"
				if mode == "window" {
					window = scopeTime(mailT0)
				}
				_, err := s.db.ExecContext(ctx, `update workiq_attempts set window_start=? where id=?`, window, attempt)
				must0(err)
				if err := s.AdmitWorkIQFrontier(ctx, attempt, resource, 1); err == nil {
					t.Fatal("changed attempt window admitted another scope")
				}
				// A fresh attempt is required to exercise the restart's scope parsing.
				must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "partial", FinishedAt: mailT0}))
				next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
				verifyWorkIQTestAttempt(t, s, next)
				_, err = s.db.ExecContext(ctx, `update workiq_attempts set window_start=? where id=?`, window, next)
				must0(err)
				if err := s.RestartWorkIQFrontier(ctx, next, resource, 1); err == nil {
					t.Fatal("changed restart window retired another scope")
				}
				return
			}
			if mode == "restart" {
				must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "partial", FinishedAt: mailT0}))
				attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
				verifyWorkIQTestAttempt(t, s, attempt)
			}
			_, err := s.db.ExecContext(ctx, `create trigger block_frontier before update on workiq_frontiers begin select raise(ignore); end`)
			must0(err)
			before := dump(t, s)
			if mode == "admit" {
				err = s.AdmitWorkIQFrontier(ctx, attempt, resource, 1)
			} else {
				err = s.RestartWorkIQFrontier(ctx, attempt, resource, 1)
			}
			if err == nil || dump(t, s) != before {
				t.Fatal("lost frontier CAS was accepted or changed retained evidence")
			}
		})
	}
}

func TestWorkIQInventoryValidationRetainedStatesAndProgress(t *testing.T) {
	for _, state := range []string{"live", "gone", "unknown-clock", "deleted", "restored", "missing-id"} {
		t.Run(state, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			first := seedWorkIQMailAttempt(t, s)
			header := "one@example.test"
			if state == "missing-id" {
				header = ""
			}
			resource := prepareRealMailInventory(t, s, first, []json.RawMessage{graphMailFixture("old", "inbox-id", header)})
			if _, _, err := s.CarryWorkIQMailInventory(ctx, first, 0, 128); err != nil {
				t.Fatal("complete full census did not remain complete")
			}
			_ = must(s.CommitWorkIQMailGroup(ctx, first, header))
			must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
			must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
			before := dump(t, s)
			must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
			if dump(t, s) != before {
				t.Fatal("admitted frontier replay changed source evidence")
			}
			next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
			verifyWorkIQTestAttempt(t, s, next)
			must0(s.StageWorkIQMailFolder(ctx, next, workiq.Folder{ID: "inbox-id"}))
			must0(s.StageWorkIQMailItems(ctx, next, "inbox-id", nil, mailT0))
			if _, _, err := s.CarryWorkIQMailInventory(ctx, next, 0, 128); err == nil {
				t.Fatal("unprepared inventory was carried")
			}
			prepareRealMailInventory(t, s, next, []json.RawMessage{})
			if _, _, err := s.CarryWorkIQMailInventory(ctx, next, 1, 128); err == nil {
				t.Fatal("caller skipped a retained census row")
			}
			var edit string
			switch state {
			case "gone":
				edit = `update workiq_rows set server_gone_at='2026-01-01T00:00:00Z'`
			case "unknown-clock":
				edit = `update workiq_rows set server_seen_at=null`
			case "deleted":
				edit = `update workiq_rows set server_deleted_at='2026-01-01T00:00:00Z',server_deleted_version='2026-01-01T00:00:00Z',server_version='2025-01-01T00:00:00Z'`
			case "restored":
				edit = `update workiq_rows set server_deleted_at='2026-01-01T00:00:00Z',server_deleted_version='2026-01-01T00:00:00Z',server_version='2026-02-01T00:00:00Z'`
			}
			if edit != "" {
				_, err := s.db.ExecContext(ctx, edit)
				must0(err)
			}
			_, done, err := s.CarryWorkIQMailInventory(ctx, next, 0, 128)
			must0(err)
			if !done {
				t.Fatal("bounded retained census did not finish")
			}
			want := 0
			if state == "live" || state == "restored" || state == "missing-id" {
				want = 1
			}
			if rowCount(t, s, `select count(*) from workiq_census_members cm join workiq_censuses c on c.id=cm.census_id where c.attempt_id=? and cm.retained=1`, next) != want {
				t.Fatal("retained inventory confused effective live, historical deletion and absent source states")
			}
			if state == "missing-id" && rowCount(t, s, `select count(*) from workiq_gaps where attempt_id=? and code='workiq_mail_message_id_missing'`, next) != 1 {
				t.Fatal("unknown retained identity failed to block gap-free qualification")
			}
			before = dump(t, s)
			if _, done, err := s.CarryWorkIQMailInventory(ctx, next, 1, 128); err != nil || !done || dump(t, s) != before {
				t.Fatal("completed carry replay changed evidence")
			}
		})
	}
}

func TestWorkIQInventoryValidationMissingAncestryAndProof(t *testing.T) {
	for _, edit := range []string{
		`update workiq_folders set root_listed=0,parent_graph_id='unlisted'`,
		`update workiq_folders set root_listed=0,parent_graph_id=graph_id,child_count=1`,
		`update workiq_folders set child_count=null`,
		`update workiq_frontiers set retired=1 where resource like '%/messages/delta'`,
		`update workiq_pages set prepared_complete=0 where frontier_id=(select id from workiq_frontiers where resource like '%/messages/delta')`,
		`update meta set key='test-retained-index-key' where key like 'workiq_mail_index:%'`,
		`update workiq_census_members set page_id=null`,
	} {
		t.Run(edit, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
				t.Fatal("missing or inconsistent persisted folder/page proof sealed an inventory")
			}
		})
	}
}

func TestWorkIQInventoryValidationMalformedFolderAndMessageResources(t *testing.T) {
	for _, p := range []struct {
		path  string
		value any
	}{
		{"/me/mailFolders//childFolders", map[string]any{"value": []any{}}},
		{"/me/mailFolders/a/childFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "b", "parentFolderId": "foreign"}}}},
		{"/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{}}}},
		{"/me/mailFolders/inbox-id/messages/delta", map[string]any{"value": []any{map[string]any{}}}},
	} {
		t.Run(p.path+fmt.Sprint(p.value), func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			id := stageWorkIQInventoryPage(t, s, attempt, p.path, p.value)
			before := dump(t, s)
			if _, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err == nil || dump(t, s) != before {
				t.Fatal("foreign/malformed staged item or folder resource prepared")
			}
		})
	}
}

func TestWorkIQInventoryValidationPreparedNonterminalRootCannotSeal(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := "/me/mailFolders?includeHiddenFolders=true"
	id := stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{"value": []any{}, "@odata.nextLink": root + "&$skiptoken=remaining"})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
		t.Fatal("actual nonterminal root page did not prepare")
	}
	before := dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("prepared nonterminal folder list sealed as complete")
	}
}
