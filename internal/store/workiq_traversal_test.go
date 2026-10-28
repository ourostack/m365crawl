package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQTraversalReadsResumeActualStoredPagesAndOrderedInventory(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	scope := WorkIQScope{Family: "mail", Account: mailAcct}
	root := "/me/mailFolders?includeHiddenFolders=true"
	initial := must(s.WorkIQTraversalProgressFor(ctx, scope, root))
	if initial.ID != 0 || initial.Terminal {
		t.Fatal("an unfetched traversal invented terminal progress")
	}
	next := root + "&$skiptoken=opaque%2bstate"
	first := stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{
		"value": []any{map[string]any{"id": "a", "childFolderCount": 0}}, "@odata.nextLink": next,
	})
	p := must(s.WorkIQTraversalProgressFor(ctx, scope, root))
	if p.ID == 0 || p.Revision != 1 || p.Terminal || p.PendingCursor != next {
		t.Fatal("a partial stored traversal lost its exact pending cursor")
	}
	ids := must(s.WorkIQPendingMailPages(ctx, attempt, root, 0, 1))
	if len(ids) != 1 || ids[0] != first {
		t.Fatal("resumption did not return the exact bounded original page receipt")
	}
	must(s.PrepareWorkIQMailPage(ctx, attempt, first, 128))
	second := stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{
		"value": []any{map[string]any{"id": "b", "childFolderCount": 0}},
	})
	must(s.PrepareWorkIQMailPage(ctx, attempt, second, 128))
	p = must(s.WorkIQTraversalProgressFor(ctx, scope, root))
	if p.Revision != 2 || !p.Terminal || p.PendingCursor != "" {
		t.Fatal("a stored terminal page was indistinguishable from an unfetched frontier")
	}
	ids = must(s.WorkIQPendingMailPages(ctx, attempt, root, first, 1))
	if len(ids) != 1 || ids[0] != second {
		t.Fatal("bounded page resumption repeated or skipped an original receipt")
	}
	folders := must(s.WorkIQMailTraversalFolders(ctx, attempt, "", 1))
	if len(folders) != 1 || folders[0].ID != "a" || folders[0].ChildCount == nil || *folders[0].ChildCount != 0 {
		t.Fatal("traversal lost prepared native folder inventory facts")
	}
	folders = must(s.WorkIQMailTraversalFolders(ctx, attempt, "a", 1))
	if len(folders) != 1 || folders[0].ID != "b" {
		t.Fatal("folder iteration was not stable and bounded")
	}
	if _, err := s.WorkIQMailTraversalGroups(ctx, attempt, nil, 1); err == nil {
		t.Fatal("identity groups were exposed before real whole-run inventory proof")
	}
	for _, folder := range []string{"a", "b"} {
		header := "one@example.test"
		if folder == "b" {
			header = ""
		}
		resource := "/me/mailFolders/" + folder + "/messages/delta()"
		page := stageWorkIQInventoryPage(t, s, attempt, resource, map[string]any{
			"value": []json.RawMessage{graphMailFixture(folder, folder, header)}, "@odata.deltaLink": resource + "?$deltatoken=end",
		})
		must(s.PrepareWorkIQMailPage(ctx, attempt, page, 128))
	}
	_, _, err := s.IndexWorkIQMailCandidates(ctx, attempt, 0, 128)
	must0(err)
	must0(s.SealWorkIQMailInventory(ctx, attempt, mailT0))
	groups := must(s.WorkIQMailTraversalGroups(ctx, attempt, nil, 1))
	if len(groups) != 1 || groups[0] != "" {
		t.Fatal("a missing Message-ID cohort disappeared at the initial cursor")
	}
	empty := ""
	groups = must(s.WorkIQMailTraversalGroups(ctx, attempt, &empty, 1))
	if len(groups) != 1 || groups[0] != "one@example.test" {
		t.Fatal("identity iteration skipped the next complete run-wide group")
	}
	must0(s.AdmitWorkIQFrontier(ctx, attempt, root, 2))
	if pages := must(s.WorkIQPendingMailPages(ctx, attempt, root, 0, 128)); len(pages) != 0 {
		t.Fatal("admitted pages were returned as pending work")
	}
	p = must(s.WorkIQTraversalProgressFor(ctx, scope, root))
	if p.Terminal || p.AdmittedRevision != 2 {
		t.Fatal("a completed older traversal was reused as new fetched data")
	}
}

func TestWorkIQTraversalReadsRejectInvalidBoundsOwnersAndCorruption(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQMailAttempt(t, s)
	root := "/me/mailFolders?includeHiddenFolders=true"
	scope := WorkIQScope{Family: "mail", Account: mailAcct}
	for _, invalid := range []WorkIQScope{{}, {Family: "other", Account: mailAcct}} {
		if _, err := s.WorkIQTraversalProgressFor(ctx, invalid, root); err == nil {
			t.Fatal("invalid source scope produced a usable frontier")
		}
	}
	for _, op := range []func() error{
		func() error {
			_, err := s.WorkIQTraversalProgressFor(ctx, scope, "https://other.invalid/private")
			return err
		},
		func() error { _, err := s.WorkIQPendingMailPages(ctx, attempt, root, -1, 1); return err },
		func() error { _, err := s.WorkIQPendingMailPages(ctx, attempt, root, 0, 0); return err },
		func() error {
			_, err := s.WorkIQPendingMailPages(ctx, attempt, "https://other.invalid/private", 0, 1)
			return err
		},
		func() error { _, err := s.WorkIQMailTraversalFolders(ctx, attempt, "", 129); return err },
		func() error { _, err := s.WorkIQMailTraversalGroups(ctx, attempt, nil, 0); return err },
	} {
		if err := op(); err == nil {
			t.Fatal("an invalid bounded traversal was admitted")
		}
	}
	invalidID := " <MIXED@EXAMPLE.TEST> "
	if _, err := s.WorkIQMailTraversalGroups(ctx, attempt, &invalidID, 1); err == nil {
		t.Fatal("a non-normalized identity cursor was accepted")
	}
	must0(s.StageWorkIQMailFolder(ctx, attempt, workiq.Folder{ID: "unknown-count"}))
	folders := must(s.WorkIQMailTraversalFolders(ctx, attempt, "", 128))
	if len(folders) != 1 || folders[0].ChildCount != nil || folders[0].TotalCount != nil {
		t.Fatal("unknown native counts were fabricated as zero")
	}
	for _, edit := range []string{
		`update workiq_folders set child_count=-1`,
		`update workiq_folders set child_count=0,total_items=-1`,
		`update workiq_folders set total_items=3,parent_graph_id=graph_id`,
	} {
		_, err := s.db.ExecContext(ctx, edit)
		must0(err)
		if _, err := s.WorkIQMailTraversalFolders(ctx, attempt, "", 128); err == nil {
			t.Fatal("corrupt folder inventory became a network traversal target")
		}
	}
	_, err := s.db.ExecContext(ctx, `update workiq_folders set parent_graph_id='',child_count=0,total_items=3`)
	must0(err)
	folders = must(s.WorkIQMailTraversalFolders(ctx, attempt, "", 128))
	if folders[0].TotalCount == nil || *folders[0].TotalCount != 3 {
		t.Fatal("known native folder totals were lost")
	}
	chat := seedWorkIQConversation(t, s)
	if _, err := s.WorkIQPendingMailPages(ctx, chat, root, 0, 1); err == nil {
		t.Fatal("chat owner supplied mail traversal pages")
	}
	if _, err := s.WorkIQMailTraversalFolders(ctx, chat, "", 1); err == nil {
		t.Fatal("chat owner supplied mail folders")
	}
	if _, err := s.WorkIQMailTraversalGroups(ctx, chat, nil, 1); err == nil {
		t.Fatal("chat owner supplied mail identities")
	}
}

func TestWorkIQTraversalReadsSurfaceEveryDriverFailure(t *testing.T) {
	ctx := context.Background()
	var attempt int64
	root := "/me/mailFolders?includeHiddenFolders=true"
	scope := WorkIQScope{Family: "mail", Account: mailAcct}
	setup := func(t *testing.T, s *Store) {
		attempt = seedWorkIQMailAttempt(t, s)
		prepareRealMailInventory(t, s, attempt, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	}
	ops := map[string]func(context.Context, *Store) error{
		"progress": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQTraversalProgressFor(ctx, scope, root)
			return err
		},
		"pages": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQPendingMailPages(ctx, attempt, root, 0, 128)
			return err
		},
		"folders": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQMailTraversalFolders(ctx, attempt, "", 128)
			return err
		},
		"groups": func(ctx context.Context, s *Store) error {
			_, err := s.WorkIQMailTraversalGroups(ctx, attempt, nil, 128)
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, setup, op) })
	}
	for _, kind := range []string{"begin", "commit"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			inj := injectFaults(t, s)
			inj.arm(kind, 1)
			p, err := s.WorkIQTraversalProgressFor(ctx, scope, root)
			if fired := inj.disarm(); !fired || !errors.Is(err, errInjected) || p.ID != 0 || p.Terminal {
				t.Fatal("failed traversal snapshot returned usable progress")
			}
		})
	}
	for name, op := range ops {
		t.Run("closed-"+name, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			must0(s.Close())
			if err := op(ctx, s); err == nil {
				t.Fatal("closed archive masqueraded as empty traversal")
			}
		})
	}
}

func TestWorkIQTraversalReadsRejectContradictoryStoredProgress(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	root := "/me/mailFolders?includeHiddenFolders=true"
	scope := WorkIQScope{Family: "mail", Account: mailAcct}
	stageWorkIQInventoryPage(t, s, attempt, root, map[string]any{"value": []any{}})
	_, err := s.db.ExecContext(ctx, `update workiq_frontiers set admitted_revision=revision+1`)
	must0(err)
	if _, err := s.WorkIQTraversalProgressFor(ctx, scope, root); err == nil {
		t.Fatal("invalid retained admission order became terminal progress")
	}
	archive := newStore(t)
	other := seedWorkIQMailAttempt(t, archive)
	prepareRealMailInventory(t, archive, other, []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")})
	_, err = archive.db.ExecContext(ctx, `update workiq_census_members set normalized_id=' <ONE@EXAMPLE.TEST> '`)
	must0(err)
	if _, err := archive.WorkIQMailTraversalGroups(ctx, other, nil, 128); err == nil {
		t.Fatal("corrupt retained identity normalization became an admission key")
	}
}

func TestWorkIQTraversalSiblingListsDoNotSkipLateEarlierIDs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	page := stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders?includeHiddenFolders=true", map[string]any{
		"value": []any{map[string]any{"id": "z-root", "childFolderCount": 1}},
	})
	must(s.PrepareWorkIQMailPage(ctx, attempt, page, 128))
	page = stageWorkIQInventoryPage(t, s, attempt, "/me/mailFolders/z-root/childFolders?includeHiddenFolders=true", map[string]any{
		"value": []any{map[string]any{"id": "a-child", "parentFolderId": "z-root", "childFolderCount": 0}},
	})
	must(s.PrepareWorkIQMailPage(ctx, attempt, page, 128))
	roots := must(s.WorkIQMailTraversalFolders(ctx, attempt, "", 128, ""))
	children := must(s.WorkIQMailTraversalFolders(ctx, attempt, "", 128, "z-root"))
	if len(roots) != 1 || roots[0].ID != "z-root" || len(children) != 1 || children[0].ID != "a-child" {
		t.Fatal("a child's lexical position skipped traversal or contaminated the root inventory")
	}
	if _, err := s.WorkIQMailTraversalFolders(ctx, attempt, "", 128, "", "z-root"); err == nil {
		t.Fatal("an ambiguous sibling cursor was accepted")
	}
}
