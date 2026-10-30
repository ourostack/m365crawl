package store

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQMailInventoryCannotSealFixtureReadyFlagsWithoutRealPageProof(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := setupWorkIQMailControl(t, s, false)
	// A producer's arbitrary census flag is not root/child/message traversal proof.
	before := dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("fixture-ready census flags masqueraded as a durable complete folder/page inventory")
	}
	if _, err := s.PrepareWorkIQMailPage(ctx, attempt, -1, 128); err == nil {
		t.Fatal("missing source page was accepted as preparation")
	}
}

func stageWorkIQInventoryPage(t *testing.T, s *Store, attempt int64, resource string, data any) int64 {
	t.Helper()
	ctx := context.Background()
	raw := must(json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200, "data": data}}}}))
	page := must(workiq.DecodePage(ctx, raw))
	progress := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, resource))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: resource, ExpectedRevision: progress.Revision, Page: page, ObservedAt: mailT0}))
	var id int64
	must0(s.db.QueryRowContext(ctx, `select max(id) from workiq_pages`).Scan(&id))
	return id
}

func TestWorkIQMailInventoryUsesActualPreparedTreeAndTerminalPages(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := "/me/mailFolders?includeHiddenFolders=true"
	rootPage := stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{"value": []any{map[string]any{
		"id": "inbox-id", "displayName": "Inbox", "childFolderCount": 0, "totalItemCount": 1,
	}}})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, rootPage, 1); err != nil || !done {
		t.Fatal("root folder page did not prepare its durable bounded inventory")
	}
	delta := "/me/mailFolders/inbox-id/messages/delta"
	cursor := delta + "?$deltatoken=opaque%2bstate"
	page := stageWorkIQInventoryPage(t, s, attempt, delta, map[string]any{"value": []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, "@odata.deltaLink": cursor})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, page, 1); err != nil || !done {
		t.Fatal("message page did not prepare staged cohort from its persisted envelope")
	}
	must0(s.SealWorkIQMailInventory(ctx, attempt, mailT0))
	before := dump(t, s)
	if err := s.AdmitWorkIQFrontier(ctx, attempt, delta, 1); err == nil || dump(t, s) != before {
		t.Fatal("cursor advanced before its canonical mail data was admitted")
	}
	result := must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	if result.Added != 1 {
		t.Fatal("prepared page did not feed canonical identity admission")
	}
	must0(s.AdmitWorkIQFrontier(ctx, attempt, delta, 1))
	progress := must(s.WorkIQProgressFor(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, delta))
	if progress.Cursor != cursor || progress.PendingCursor != cursor || progress.AdmittedRevision != 1 ||
		rowCount(t, s, `select count(*) from workiq_pages where id=? and state='admitted'`, page) != 1 {
		t.Fatal("canonical receipt and byte-exact terminal cursor did not commit together")
	}
	must0(s.AdmitWorkIQFrontier(ctx, attempt, root, 1))
}

func TestWorkIQMailPreparationResumesChunksAndRehydratesEarlierAttempt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, first, 0, 128)
	must0(err)
	root := "/me/mailFolders?includeHiddenFolders=true"
	id := stageWorkIQInventoryPage(t, s, first, root, map[string]any{"value": []any{
		map[string]any{"id": "a", "childFolderCount": 0}, map[string]any{"id": "b", "childFolderCount": 0},
	}})
	if done, err := s.PrepareWorkIQMailPage(ctx, first, id, 1); err != nil || done {
		t.Fatal("bounded page chunk was falsely complete")
	}
	before := dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, first, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("partial root preparation became a complete census")
	}
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "failed", ErrorCode: "workiq_timeout", FinishedAt: mailT0}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	if done, err := s.PrepareWorkIQMailPage(ctx, next, id, 1); err != nil || done {
		t.Fatal("new verified attempt did not rehydrate the earlier staged page")
	}
	if done, err := s.PrepareWorkIQMailPage(ctx, next, id, 1); err != nil || !done {
		t.Fatal("new attempt did not resume the saved page offset")
	}
	for _, folder := range []string{"a", "b"} {
		resource := "/me/mailFolders/" + folder + "/messages/delta"
		page := stageWorkIQInventoryPage(t, s, next, resource, map[string]any{"value": []any{}, "@odata.deltaLink": resource + "?$deltatoken=end"})
		if done, err := s.PrepareWorkIQMailPage(ctx, next, page, 128); err != nil || !done {
			t.Fatal("empty folder baseline did not prepare")
		}
	}
	must0(s.SealWorkIQMailInventory(ctx, next, mailT0))
	if rowCount(t, s, `select count(*) from workiq_censuses where attempt_id=? and complete=1 and full_enumeration=1`, next) != 1 {
		t.Fatal("rehydrated durable traversal failed to establish actual full census proof")
	}
	var original int64
	must0(s.db.QueryRowContext(ctx, `select attempt_id from workiq_pages where id=?`, id).Scan(&original))
	if original != first {
		t.Fatal("resumption rewrote original page attempt attribution")
	}
}

func TestWorkIQMailInventoryRejectsCyclicFolderEvidence(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	pages := []struct {
		resource string
		data     any
	}{
		{"/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "a", "childFolderCount": 1}}}},
		{"/me/mailFolders/a/childFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "b", "parentFolderId": "a", "childFolderCount": 1}}}},
		{"/me/mailFolders/b/childFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "a", "parentFolderId": "b", "childFolderCount": 1}}}},
	}

	for _, p := range pages {
		id := stageWorkIQInventoryPage(t, s, attempt, p.resource, p.data)
		if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
			t.Fatal("staged cycle metadata could not be retained as evidence")
		}
	}
	for _, folder := range []string{"a", "b"} {
		resource := "/me/mailFolders/" + folder + "/messages/delta"
		id := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []any{}, "@odata.deltaLink": resource + "?$deltatoken=end"})
		if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
			t.Fatal("empty folder baseline did not prepare")
		}
	}
	before := dump(t, s)
	if err := s.SealWorkIQMailInventory(ctx, attempt, mailT0); err == nil || dump(t, s) != before {
		t.Fatal("a cyclic folder graph masqueraded as a gap-free whole-account inventory")
	}
}

func TestWorkIQMailInventoryRequiresStableDeclaredChildCount(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
			must0(err)
			pages := []struct {
				resource string
				data     any
			}{
				{"/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "a", "childFolderCount": count}}}},
				{"/me/mailFolders/a/childFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "b", "parentFolderId": "a", "childFolderCount": 0}}}},
			}
			for _, p := range pages {
				id := stageWorkIQInventoryPage(t, s, attempt, p.resource, p.data)
				if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
					t.Fatal("actual folder page did not prepare")
				}
			}
			for _, folder := range []string{"a", "b"} {
				resource := "/me/mailFolders/" + folder + "/messages/delta"
				id := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{"value": []any{}, "@odata.deltaLink": resource + "?$deltatoken=end"})
				if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
					t.Fatal("actual empty folder baseline did not prepare")
				}
			}
			before := dump(t, s)
			err = s.SealWorkIQMailInventory(ctx, attempt, mailT0)
			if count == 1 {
				must0(err)
			} else if err == nil || dump(t, s) != before {
				t.Fatal("changed/incomplete child inventory falsely sealed as gap-free")
			}
		})
	}
}

func TestWorkIQMailListingPreservesFixedFolderKind(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders/inbox", map[string]any{"id": "inbox-id", "childFolderCount": 0})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, id, 128); err != nil || !done {
		t.Fatal("fixed inbox lookup did not prepare")
	}
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{})
	var kind string
	must0(s.db.QueryRowContext(ctx, `select m.kind from mail_folders m join workiq_folders f on f.account_id=m.account and f.folder_key=m.folder_key where f.graph_id='inbox-id'`).Scan(&kind))
	if kind != "inbox" {
		t.Fatal("generic listing erased the independently verified well-known folder kind")
	}
}
