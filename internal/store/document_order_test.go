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

// A mixed archive: one Office JSON row with a modification time and one without, three registry
// rows (two opened times and none), and usage rows of every type.
func mixedDocuments(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	dated := documentFixture("Dated JSON document", 0)
	dated.Modified = officedocuments.Timestamp{Raw: "2026-10-05T08:00:00Z", Value: time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)}
	dated.URL, dated.WebURL = "https://example.invalid/dated", "https://example.invalid/dated"
	admitDocument(t, s, beginDocument(t, s, base), base, "complete", "json", []officedocuments.Document{dated, documentFixture("Undated JSON document", 1)})
	opened := func(node int64, title, raw string) officeregistry.Observation {
		observation := registryObservation(node, title)
		observation.OpenedRaw = raw
		return observation
	}
	admitRegistry(t, s, beginRegistry(t, s, registryLocator, base), base, "complete", "registry", officeregistry.Result{Documents: []officeregistry.Observation{
		opened(1, "Opened earlier", "2026-10-07T09:00:00Z"), opened(2, "Opened later", "2026-10-09T09:00:00+02:00"),
		opened(3, "Opened unknown", "last week"), opened(4, "Also opened later", "2026-10-09T07:00:00Z")}})
	zoneless := usageRecent("native-1")
	zoneless.FileModifiedRaw = "2026-10-09 12:34:56"
	admitUsage(t, s, beginUsage(t, s, usageLocator, base), base, "complete", "usage", onedriveusage.Result{
		Recent:        []onedriveusage.RecentObservation{zoneless},
		Documents:     []onedriveusage.DocumentObservation{usageDocument("native-1", "variant-a", "Usage document")},
		Collaborators: []onedriveusage.Collaborator{usageCollaborator("person-1")}})
	return s
}

// --order-by opened_at: rows that state an opened time come first, newest first; rows without one
// follow in the default order. The default order is unchanged and every order is total.
func TestDocumentOrderByOpened(t *testing.T) {
	s := mixedDocuments(t)
	titles := func(filter DocumentFilter) (out []string) {
		t.Helper()
		for _, row := range usageRows(t, s, filter) {
			switch {
			case row.Registry != nil:
				out = append(out, row.Registry.Title)
			case row.Usage != nil:
				out = append(out, "usage "+row.Usage.Type)
			default:
				out = append(out, row.Document.Title)
			}
		}
		return out
	}
	byDefault := titles(DocumentFilter{})
	if want := []string{"usage document", "Dated JSON document", "usage collaborator", "usage recent", "Also opened later", "Opened earlier", "Opened later", "Opened unknown", "Undated JSON document"}; !reflect.DeepEqual(byDefault, want) {
		t.Fatalf("default order = %q", byDefault)
	}
	if got := titles(DocumentFilter{OrderBy: DocumentOrderModified}); !reflect.DeepEqual(got, byDefault) {
		t.Fatalf("explicit modified_at order = %q", got)
	}
	byOpened := titles(DocumentFilter{OrderBy: DocumentOrderOpened})
	if want := []string{"Also opened later", "Opened later", "Opened earlier", "usage document", "Dated JSON document", "usage collaborator", "usage recent", "Opened unknown", "Undated JSON document"}; !reflect.DeepEqual(byOpened, want) {
		t.Fatalf("opened order = %q", byOpened)
	}
	// Two rows opened at the same instant keep one order: title, then source and row.
	if got := titles(DocumentFilter{OrderBy: DocumentOrderOpened, Application: RegistryDocumentSource, Limit: 2}); !reflect.DeepEqual(got, byOpened[:2]) {
		t.Fatalf("limited opened order = %q", got)
	}
	if _, _, err := s.DocumentObservations(context.Background(), DocumentFilter{OrderBy: "captured_at"}); err == nil {
		t.Fatal("an unknown order was accepted")
	}
}

// A date window excludes every row without a modification time. The snapshot counts them by the
// kind of row, among the rows the other filters select, and only when a window was asked for.
func TestDocumentSnapshotCountsUndatedRows(t *testing.T) {
	s := mixedDocuments(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	snapshot, err := s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: since})
	want := map[string]int{DocumentVariantJSON: 1, RegistryDocumentKind: 4, UsageDocumentKind + "_" + UsageTypeRecent: 1, UsageDocumentKind + "_" + UsageTypeCollaborator: 1}
	if err != nil || len(snapshot.Rows) != 2 || !reflect.DeepEqual(snapshot.Undated, want) {
		t.Fatalf("window: %d rows, undated %v, %v", len(snapshot.Rows), snapshot.Undated, err)
	}
	snapshot, err = s.DocumentSnapshot(ctx, DocumentFilter{ModifiedUntil: since, Application: RegistryDocumentSource, Query: "later"})
	if err != nil || len(snapshot.Rows) != 0 || !reflect.DeepEqual(snapshot.Undated, map[string]int{RegistryDocumentKind: 2}) {
		t.Fatalf("filtered window: %d rows, undated %v, %v", len(snapshot.Rows), snapshot.Undated, err)
	}
	if snapshot, err = s.DocumentSnapshot(ctx, DocumentFilter{Application: RegistryDocumentSource}); err != nil || len(snapshot.Rows) != 4 || snapshot.Undated != nil {
		t.Fatalf("no window: %d rows, undated %v, %v", len(snapshot.Rows), snapshot.Undated, err)
	}
	if snapshot, err = s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: since, Application: "Word"}); err != nil || len(snapshot.Undated) != 0 {
		t.Fatalf("window with no undated row: %v %v", snapshot.Undated, err)
	}
}

func TestDocumentOrderAndUndatedReadFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) { setupUsageObservation(t, s) }
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: base.Add(-time.Hour), OrderBy: DocumentOrderOpened, Limit: 1})
		return err
	})
	// Archives from before the variant and usage columns count every undated row as Office JSON,
	// or by variant alone, and order by opened time only where one is stored.
	ctx := context.Background()
	s := mixedDocuments(t)
	for _, statement := range []string{"alter table document_observations drop column usage_json", "drop table document_usage_relations"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("delete from document_observations where variant='onedrive_usage'"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: base.AddDate(-1, 0, 0), OrderBy: DocumentOrderOpened})
	if err != nil || !reflect.DeepEqual(snapshot.Undated, map[string]int{DocumentVariantJSON: 1, RegistryDocumentKind: 4}) {
		t.Fatalf("schema 14 undated counts: %v %v", snapshot.Undated, err)
	}
	if _, err := s.db.Exec("delete from document_observations where variant<>'office_json'"); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{"alter table document_observations drop column variant", "alter table document_observations drop column registry_json",
		"alter table document_observations drop column opened_at"} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err = s.DocumentSnapshot(ctx, DocumentFilter{ModifiedSince: base.AddDate(-1, 0, 0), OrderBy: DocumentOrderOpened})
	if err != nil || len(snapshot.Rows) != 1 || !reflect.DeepEqual(snapshot.Undated, map[string]int{DocumentVariantJSON: 1}) {
		t.Fatalf("schema 13 undated counts: %d rows, %v %v", len(snapshot.Rows), snapshot.Undated, err)
	}
}
