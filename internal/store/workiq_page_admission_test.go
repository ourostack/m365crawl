package store

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQFrontierKeepsGoodCursorUntilRealPreparedAdmission(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	resource := "/me/mailFolders/inbox-id/messages/delta()"
	page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],"@odata.nextLink":"/me/mailFolders/inbox-id/messages/delta()?$skiptoken=opaque"}}]}}`)))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: resource, Page: page, ObservedAt: mailT0}))
	scope := WorkIQScope{Family: "mail", Account: mailAcct}
	progress := must(s.WorkIQProgressFor(ctx, scope, resource))
	if progress.Revision != 1 || progress.Cursor != "" || progress.PendingCursor != page.NextCursor || progress.AdmittedRevision != 0 {
		t.Fatal("staged pending cursor was lost or confused with admitted last-good progress")
	}
	before := dump(t, s)
	if err := s.AdmitWorkIQFrontier(ctx, attempt, resource, progress.Revision); err == nil || dump(t, s) != before {
		t.Fatal("unprepared nonterminal page advanced the last-good frontier")
	}
	terminal := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],"@odata.deltaLink":"/me/mailFolders/inbox-id/messages/delta()?$deltatoken=terminal"}}]}}`)))
	must0(s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: resource, ExpectedRevision: 1, Page: terminal, ObservedAt: mailT0}))
	before = dump(t, s)
	if err := s.AdmitWorkIQFrontier(ctx, attempt, resource, 1); err == nil || dump(t, s) != before {
		t.Fatal("stale writer advanced a newer staged frontier")
	}
}
