package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func prepareUnsealedWorkIQInventory(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	root := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{map[string]any{"id": "inbox-id", "childFolderCount": 0}}})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, root, 128); err != nil || !done {
		t.Fatal("actual root did not prepare")
	}
	path := "/me/mailFolders/inbox-id/messages/delta()"
	page := stageWorkIQInventoryPage(t, s, attempt, path, map[string]any{"value": []any{}, "@odata.deltaLink": path + "?$deltatoken=end"})
	if done, err := s.PrepareWorkIQMailPage(ctx, attempt, page, 128); err != nil || !done {
		t.Fatal("actual folder baseline did not prepare")
	}
	return attempt
}

func TestWorkIQMailInventoryInspectionIsReadOnlyThenCommitsInShortRole(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := prepareUnsealedWorkIQInventory(t, s)
	path := s.cs.Path()
	held := must(AcquireLock(path))
	defer held()
	reader := must(OpenReadOnly(ctx, path))
	before := dump(t, s)
	proof, err := reader.InspectWorkIQMailInventory(ctx, attempt)
	must0(err)
	must0(reader.Close())
	if dump(t, s) != before {
		t.Fatal("heavy inventory inspection wrote readiness/progress while another archive writer held the role")
	}
	held()
	must0(WithWorkIQWriter(ctx, path, func(ctx context.Context, writer *Store) error {
		return writer.SealWorkIQMailInventoryProof(ctx, attempt, proof, mailT0)
	}))
	if rowCount(t, s, `select count(*) from workiq_censuses where attempt_id=? and inventory_ready=1 and full_enumeration=1 and complete=1`, attempt) != 1 {
		t.Fatal("validated read-only inventory proof did not produce its short canonical receipt")
	}
	before = dump(t, s)
	must0(s.SealWorkIQMailInventoryProof(ctx, attempt, proof, mailT0.Add(time.Hour)))
	if dump(t, s) != before {
		t.Fatal("same inventory proof replay refreshed completion/carry state")
	}
}

func TestWorkIQMailSealedIncrementalInventoryRejectsLateStages(t *testing.T) {
	for _, folder := range []bool{false, true} {
		t.Run(map[bool]string{false: "item", true: "folder"}[folder], func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			first := seedWorkIQMailAttempt(t, s)
			resource := prepareRealMailInventory(t, s, first, []json.RawMessage{})
			must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
			must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
			must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
			next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
			verifyWorkIQTestAttempt(t, s, next)
			prepareRealMailInventory(t, s, next, []json.RawMessage{})
			before := dump(t, s)
			var err error
			if folder {
				err = s.StageWorkIQMailFolder(ctx, next, workiq.Folder{ID: "late", Name: "Late"})
			} else {
				err = s.StageWorkIQMailItems(ctx, next, "inbox-id", []json.RawMessage{graphMailFixture("late", "inbox-id", "late@example.test")}, mailT0)
			}
			if err == nil || dump(t, s) != before {
				t.Fatal("settled inventory accepted unproven late source data before incremental carry")
			}
		})
	}
}

func TestWorkIQMailInventoryProofRejectsChangedStagingAndForeignScope(t *testing.T) {
	for _, mode := range []string{"folder", "item", "page", "qualification", "attempt", "archive", "index", "ready-conflict", "receipt-cas"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := prepareUnsealedWorkIQInventory(t, s)
			proof := must(s.InspectWorkIQMailInventory(ctx, attempt))
			target := s
			run := attempt
			switch mode {
			case "folder":
				must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Changed"}))
			case "item":
				must0(s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("late", "inbox-id", "late@example.test")}, mailT0))
			case "page":
				_ = stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{}})
			case "qualification":
				_, err := s.db.ExecContext(ctx, `update workiq_attempts set qualification='different' where id=?`, attempt)
				must0(err)
			case "attempt":
				run = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
				verifyWorkIQTestAttempt(t, s, run)
			case "archive":
				target = newStore(t)
				run = prepareUnsealedWorkIQInventory(t, target)
			case "index":
				_, err := s.db.ExecContext(ctx, `update meta set key='test-retained-index-key' where key=?`, workIQMailIndexPrefix+mailAcct)
				must0(err)
			case "ready-conflict":
				_, err := s.db.ExecContext(ctx, `update workiq_censuses set inventory_ready=1,enumeration_key='foreign'`)
				must0(err)
			case "receipt-cas":
				_, err := s.db.ExecContext(ctx, `create trigger block_inventory before update on workiq_censuses begin select raise(ignore); end`)
				must0(err)
			}
			before := dump(t, target)
			if err := target.SealWorkIQMailInventoryProof(ctx, run, proof, mailT0); err == nil || dump(t, target) != before {
				t.Fatal("stale/foreign inventory proof or lost receipt CAS was accepted")
			}
		})
	}
}

func TestWorkIQMailInventoryProofFaultsLeaveNoReadOrWriteReceipt(t *testing.T) {
	for _, mode := range []string{"inspect", "seal", "empty-seal"} {
		t.Run(mode, func(t *testing.T) {
			var attempt int64
			var proof WorkIQMailInventoryProof
			setup := func(t *testing.T, s *Store) {
				ctx := context.Background()
				if mode == "empty-seal" {
					attempt = seedWorkIQMailAttempt(t, s)
					_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
					must0(err)
					id := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{"value": []any{}})
					_, err = s.PrepareWorkIQMailPage(ctx, attempt, id, 128)
					must0(err)
				} else {
					attempt = prepareUnsealedWorkIQInventory(t, s)
				}
				if mode != "inspect" {
					proof = must(s.InspectWorkIQMailInventory(ctx, attempt))
				}
			}
			op := func(ctx context.Context, s *Store) error {
				if mode == "inspect" {
					_, err := s.InspectWorkIQMailInventory(ctx, attempt)
					return err
				}
				return s.SealWorkIQMailInventoryProof(ctx, attempt, proof, mailT0)
			}
			sweepFaults(t, setup, op)
			s := newStore(t)
			setup(t, s)
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, "rowsaffected", true, op, nil)
		})
	}
}

func TestWorkIQMailInventoryProofFileIdentityAndPrivateRendering(t *testing.T) {
	for _, mode := range []string{"inspect-before", "inspect-after", "inspect-changed", "seal-stat", "seal-changed"} {
		t.Run(mode, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := prepareUnsealedWorkIQInventory(t, s)
			proof := must(s.InspectWorkIQMailInventory(ctx, attempt))
			other := newStore(t)
			otherInfo := must(os.Stat(other.cs.Path()))
			original := workIQInventoryStat
			t.Cleanup(func() { workIQInventoryStat = original })
			calls := 0
			workIQInventoryStat = func(path string) (os.FileInfo, error) {
				calls++
				if mode == "inspect-before" || mode == "seal-stat" || mode == "inspect-after" && calls == 2 {
					return nil, os.ErrNotExist
				}
				if mode == "seal-changed" || mode == "inspect-changed" && calls == 2 {
					return otherInfo, nil
				}
				return original(path)
			}
			before := dump(t, s)
			if strings.HasPrefix(mode, "inspect") {
				if _, err := s.InspectWorkIQMailInventory(ctx, attempt); err == nil {
					t.Fatal("lost/replaced archive file produced a reusable inventory proof")
				}
			} else if err := s.SealWorkIQMailInventoryProof(ctx, attempt, proof, mailT0); err == nil {
				t.Fatal("proof was committed after archive identity changed")
			}
			if dump(t, s) != before {
				t.Fatal("archive identity failure changed readiness")
			}
			text := fmt.Sprintf("%v %#v", proof, proof) + string(must(json.Marshal(proof)))
			if strings.Contains(text, s.cs.Path()) || strings.Contains(text, proof.enumeration) {
				t.Fatal("opaque inventory proof formatting exposed private source state")
			}
		})
	}
}

func TestWorkIQMailInventoryProofScopeAndCompletedCarryReplay(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := prepareUnsealedWorkIQInventory(t, s)
	proof := must(s.InspectWorkIQMailInventory(ctx, first))
	if err := s.SealWorkIQMailInventoryProof(ctx, first, WorkIQMailInventoryProof{}, mailT0); err == nil {
		t.Fatal("zero opaque proof was accepted")
	}
	if err := s.SealWorkIQMailInventoryProof(ctx, first, proof, time.Time{}); err == nil {
		t.Fatal("unknown completion time was accepted")
	}
	chat := seedWorkIQConversation(t, s)
	if _, err := s.InspectWorkIQMailInventory(ctx, chat); err == nil {
		t.Fatal("chat owner obtained a mail proof")
	}
	if err := s.SealWorkIQMailInventoryProof(ctx, chat, proof, mailT0); err == nil {
		t.Fatal("chat owner sealed a mail proof")
	}
	must0(s.SealWorkIQMailInventoryProof(ctx, first, proof, mailT0))
	resource := "/me/mailFolders/inbox-id/messages/delta()"
	must0(s.AdmitWorkIQFrontier(ctx, first, resource, 1))
	must0(s.AdmitWorkIQFrontier(ctx, first, "/me/mailFolders?includeHiddenFolders=true", 1))
	must0(s.CompleteWorkIQAttempt(ctx, first, WorkIQSummary{Status: "ok", FinishedAt: mailT0}))
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	verifyWorkIQTestAttempt(t, s, next)
	prepareRealMailInventory(t, s, next, []json.RawMessage{})
	proof = must(s.InspectWorkIQMailInventory(ctx, next))
	_, done, err := s.CarryWorkIQMailInventory(ctx, next, 0, 128)
	must0(err)
	if !done {
		t.Fatal("empty retained carry did not complete")
	}
	before := dump(t, s)
	must0(s.SealWorkIQMailInventoryProof(ctx, next, proof, mailT0.Add(time.Hour)))
	if dump(t, s) != before || rowCount(t, s, `select count(*) from workiq_censuses where attempt_id=? and complete=1 and carry_complete=1`, next) != 1 {
		t.Fatal("idempotent inventory receipt reset a completed incremental carry")
	}
}

func TestWorkIQMailInventoryStageMutationCountsAreTransactional(t *testing.T) {
	for _, folder := range []bool{false, true} {
		t.Run(fmt.Sprint(folder), func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			attempt := seedWorkIQMailAttempt(t, s)
			if !folder {
				must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id"}))
			}
			op := func(ctx context.Context, s *Store) error {
				if folder {
					return s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "inbox-id", Name: "Inbox"})
				}
				return s.StageWorkIQMailItems(ctx, attempt, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0)
			}
			inj := injectFaults(t, s)
			sweepKind(t, s, inj, "rowsaffected", true, op, nil)
			before := dump(t, s)
			must0(op(ctx, s))
			if dump(t, s) != before {
				t.Fatal("exact folder/item replay changed the staging revision or source data")
			}
		})
	}
}
