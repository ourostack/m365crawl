package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
)

// membershipCounts are the stored membership and relation rows, and the retained rows served.
func membershipCounts(t *testing.T, s *Store) (members, relations, retained int) {
	t.Helper()
	for _, row := range usageRows(t, s, DocumentFilter{}) {
		if row.Retained {
			retained++
		}
	}
	return rowCount(t, s, `select count(*) from document_capture_members`), rowCount(t, s, `select count(*) from document_usage_relations`), retained
}

func membersCapture(t *testing.T, s *Store, capture int64) int {
	t.Helper()
	return rowCount(t, s, `select coalesce(members_capture,0) from document_capture_metadata where capture_id=?`, capture)
}

// Membership is stored once. An unchanged capture of any document family records its attempt and
// capture metadata and names the capture that owns the membership; it adds no membership row and
// no relation row, however often it repeats, and what is current, retained or related is the same.
func TestUnchangedCapturesShareMembership(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := func(hours int) time.Time { return base.Add(time.Duration(hours) * time.Hour) }
	stateA := onedriveusage.Result{
		Recent: []onedriveusage.RecentObservation{usageRecent("native-1"), usageRecent("native-2")},
		Documents: []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Synthetic first"),
			usageDocument("native-1", "variant-b", "Synthetic first"), usageDocument("native-2", "variant-a", "Synthetic second")},
		Collaborators: []onedriveusage.Collaborator{usageCollaborator("person-1")}}
	stateB := onedriveusage.Result{Recent: stateA.Recent[:1], Documents: stateA.Documents[:1]}
	a := beginUsage(t, s, usageLocator, at(1))
	admitUsage(t, s, a, at(1), "complete", "digest-a", stateA)
	members, relations, retained := membershipCounts(t, s)
	if members != 6 || relations != 3 || retained != 0 || membersCapture(t, s, a.ID) != 0 {
		t.Fatalf("first capture: %d members, %d relations, %d retained", members, relations, retained)
	}
	view := func() []DocumentObservationRow { return usageRows(t, s, DocumentFilter{}) }
	before := view()
	var last KnowledgeAttempt
	for i := 2; i <= 4; i++ {
		last = beginUsage(t, s, usageLocator, at(i))
		admitUsage(t, s, last, at(i), "unchanged", "digest-a", onedriveusage.Result{})
		if owner := membersCapture(t, s, last.ID); owner != int(a.ID) {
			t.Fatalf("unchanged capture %d names membership owner %d, want the first capture %d", last.ID, owner, a.ID)
		}
	}
	if m, r, kept := membershipCounts(t, s); m != members || r != relations || kept != 0 {
		t.Fatalf("three unchanged captures left %d members (want %d), %d relations (want %d), %d retained", m, members, r, relations, kept)
	}
	if !reflect.DeepEqual(view(), before) {
		t.Fatal("unchanged captures changed the served rows")
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].AdmittedID != last.ID || sources[0].Status != "unchanged" || !sources[0].AdmittedAt.Equal(at(4).Add(time.Second)) {
		t.Fatalf("unchanged capture did not advance the frontier: %+v %v", sources, err)
	}
	source := sources[0].ID
	recent := usageRow(t, view(), source, UsageTypeRecent, "native-1", "")
	variantB := usageRow(t, view(), source, UsageTypeDocument, "native-1", "variant-b")
	if detail := usageRelations(t, s, recent.ID); detail.UsageRelationsCaptureID != a.ID || detail.UsageRelationsTotal != 2 || len(detail.UsageRelations) != 2 {
		t.Fatalf("relations of a row under a shared membership: %+v", detail)
	}
	// A failed attempt after an unchanged one changes nothing, and an unchanged one after the
	// failure still shares the same owner.
	failed := beginUsage(t, s, usageLocator, at(5))
	if err := s.RecordKnowledgeFailure(ctx, failed, KnowledgeOutcome{Status: "failed", Reason: "onedrive_usage_capture_failed", At: at(5)}); err != nil {
		t.Fatal(err)
	}
	afterFailure := beginUsage(t, s, usageLocator, at(6))
	admitUsage(t, s, afterFailure, at(6), "unchanged", "digest-a", onedriveusage.Result{})
	if m, r, kept := membershipCounts(t, s); m != members || r != relations || kept != 0 || membersCapture(t, s, afterFailure.ID) != int(a.ID) || !reflect.DeepEqual(view(), before) {
		t.Fatalf("unchanged after a failure: %d members, %d relations, %d retained", m, r, kept)
	}
	// A changed capture owns a full membership. The rows only the first capture held are retained
	// although unchanged captures still name the first capture, and none of the current rows is.
	b := beginUsage(t, s, usageLocator, at(7))
	admitUsage(t, s, b, at(7), "complete", "digest-b", stateB)
	onB := beginUsage(t, s, usageLocator, at(8))
	admitUsage(t, s, onB, at(8), "unchanged", "digest-b", onedriveusage.Result{})
	if m, r, kept := membershipCounts(t, s); m != members+2 || r != relations+1 || kept != 4 || membersCapture(t, s, b.ID) != 0 || membersCapture(t, s, onB.ID) != int(b.ID) {
		t.Fatalf("changed capture then unchanged: %d members, %d relations, %d retained", m, r, kept)
	}
	if row := usageRow(t, view(), source, UsageTypeRecent, "native-1", ""); row.Retained || row.ID != recent.ID || row.CaptureID != a.ID {
		t.Fatalf("a row both captures hold was reclassified: %+v", row)
	}
	// A retained row's relations come from the last capture that owned a membership holding it.
	if detail := usageRelations(t, s, variantB.ID); !detail.Observation.Retained || detail.UsageRelationsCaptureID != a.ID || detail.UsageRelationsTotal != 1 {
		t.Fatalf("retained row under a superseded owner: %+v", detail)
	}
	if detail := usageRelations(t, s, recent.ID); detail.UsageRelationsCaptureID != b.ID || detail.UsageRelationsTotal != 1 {
		t.Fatalf("current row's relations after the change: %+v", detail)
	}
	// A-B-A: the first state returns as a changed capture and owns a new full membership.
	again := beginUsage(t, s, usageLocator, at(9))
	admitUsage(t, s, again, at(9), "complete", "digest-a", stateA)
	if m, r, kept := membershipCounts(t, s); m != 2*members+2 || r != 2*relations+1 || kept != 0 || membersCapture(t, s, again.ID) != 0 {
		t.Fatalf("A-B-A: %d members, %d relations, %d retained", m, r, kept)
	}
	if rows := view(); len(rows) != len(before) || usageRow(t, rows, source, UsageTypeDocument, "native-1", "variant-b").ID != variantB.ID {
		t.Fatal("A-B-A did not reuse the first representations")
	}

	// An archive written before the pointer existed holds an unchanged capture with its own copied
	// rows. It reads the same, and the next unchanged capture shares that capture's rows.
	legacy := newStore(t)
	first := beginUsage(t, legacy, usageLocator, at(1))
	admitUsage(t, legacy, first, at(1), "complete", "digest-a", stateA)
	copied := beginUsage(t, legacy, usageLocator, at(2))
	admitUsage(t, legacy, copied, at(2), "unchanged", "digest-a", onedriveusage.Result{})
	pointed := usageRows(t, legacy, DocumentFilter{})
	for _, statement := range []string{
		`insert into document_capture_members(capture_id,observation_rowid) select ` + itoa(int(copied.ID)) + `,observation_rowid from document_capture_members where capture_id=` + itoa(int(first.ID)),
		`insert into document_usage_relations(capture_id,recent_rowid,document_rowid) select ` + itoa(int(copied.ID)) + `,recent_rowid,document_rowid from document_usage_relations where capture_id=` + itoa(int(first.ID)),
		`update document_capture_metadata set members_capture=null where capture_id=` + itoa(int(copied.ID))} {
		if _, err := legacy.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(usageRows(t, legacy, DocumentFilter{}), pointed) {
		t.Fatal("a copied membership and a shared one read differently")
	}
	legacyRecent := usageRow(t, pointed, pointed[0].SourceID, UsageTypeRecent, "native-1", "")
	if detail := usageRelations(t, legacy, legacyRecent.ID); detail.UsageRelationsCaptureID != copied.ID || detail.UsageRelationsTotal != 2 {
		t.Fatalf("relations under a copied membership: %+v", detail)
	}
	m, r, _ := membershipCounts(t, legacy)
	next := beginUsage(t, legacy, usageLocator, at(3))
	admitUsage(t, legacy, next, at(3), "unchanged", "digest-a", onedriveusage.Result{})
	if m2, r2, kept := membershipCounts(t, legacy); m2 != m || r2 != r || kept != 0 || membersCapture(t, legacy, next.ID) != int(copied.ID) {
		t.Fatalf("unchanged after a copied capture: %d members, %d relations, %d retained", m2, r2, kept)
	}

	// Office JSON and the Office registry share the same admission.
	other := newStore(t)
	jsonFirst := beginDocument(t, other, at(1))
	admitDocument(t, other, jsonFirst, at(1), "complete", "json-a", []officedocuments.Document{documentFixture("Synthetic JSON document", 0)})
	registryFirst := beginRegistry(t, other, registryLocator, at(1))
	admitRegistry(t, other, registryFirst, at(1), "complete", "registry-a", officeregistry.Result{Documents: []officeregistry.Observation{registryObservation(12, "Synthetic registry document")}})
	served := usageRows(t, other, DocumentFilter{})
	for i := 2; i <= 4; i++ {
		jsonNext := beginDocument(t, other, at(i))
		admitDocument(t, other, jsonNext, at(i), "unchanged", "json-a", nil)
		registryNext := beginRegistry(t, other, registryLocator, at(i))
		admitRegistry(t, other, registryNext, at(i), "unchanged", "registry-a", officeregistry.Result{})
		if membersCapture(t, other, jsonNext.ID) != int(jsonFirst.ID) || membersCapture(t, other, registryNext.ID) != int(registryFirst.ID) {
			t.Fatal("an unchanged Office JSON or registry capture does not name its membership owner")
		}
	}
	if m, _, kept := membershipCounts(t, other); m != 2 || kept != 0 || !reflect.DeepEqual(usageRows(t, other, DocumentFilter{}), served) {
		t.Fatalf("unchanged Office JSON and registry captures: %d members, %d retained", m, kept)
	}
}
