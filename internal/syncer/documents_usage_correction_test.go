package syncer

import (
	"context"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestUsageNativeTupleCollisionRoundTrip(t *testing.T) {
	file := usageFileFixture(t, "native-tuple.db",
		"DELETE FROM recent_files_formatted_spo; DELETE FROM recent_files_spo; DELETE FROM top_collaborators;",
		"INSERT INTO recent_files_spo(ID) VALUES('native'),('native'||char(31)||'x');",
		usageFormattedSQL(t, "native", "x\x1fy", "Synthetic tuple"),
		usageFormattedSQL(t, "native\x1fx", "y", "Synthetic tuple"))
	ctx := context.Background()
	var native onedriveusage.Result
	err := officecapture.WithOneDriveUsageCapture(ctx, officecapture.UsageInput{Path: file}, func(c *officecapture.UsageCapture) error {
		var err error
		native, err = c.ReadUsage(ctx)
		return err
	})
	if err != nil || len(native.Recent) != 2 || len(native.Documents) != 2 || len(native.Losses) != 0 {
		t.Fatalf("literal tuples did not qualify with zero loss: %+v %v", native, err)
	}
	archive := newDB(t)
	report, err := SyncDocuments(ctx, DocumentOptions{DBPath: archive, UsageFile: file, Explicit: true, Discover: true})
	if err != nil || report.Captured != 1 || report.Failed != 0 || len(report.Sources) != 1 || len(report.Sources[0].Losses) != 0 {
		t.Fatalf("qualified tuples not admitted losslessly: %+v %v", report, err)
	}
	s, err := store.OpenReadOnly(ctx, archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, _, err := s.DocumentObservations(ctx, store.DocumentFilter{})
	if err != nil || len(rows) != 4 {
		t.Fatalf("archived tuples: %+v %v", rows, err)
	}
	documents, recent := map[[2]string]onedriveusage.DocumentObservation{}, map[string]onedriveusage.RecentObservation{}
	for _, row := range rows {
		switch row.Usage.Type {
		case store.UsageTypeDocument:
			d := *row.Usage.Document
			documents[[2]string{d.ID, d.Format}] = d
		case store.UsageTypeRecent:
			r := *row.Usage.Recent
			recent[r.ID] = r
		default:
			t.Fatalf("unexpected native row: %+v", row)
		}
		detail, err := s.DocumentObservation(ctx, row.ID)
		if err != nil || detail.UsageRelationsTotal != 1 || detail.UsageRelationsCaptureID != report.Sources[0].AdmittedID {
			t.Fatalf("capture-owned tuple relation: %+v %v", detail, err)
		}
	}
	for _, d := range native.Documents {
		if !reflect.DeepEqual(documents[[2]string{d.ID, d.Format}], d) {
			t.Fatalf("native document payload lost: %+v", d)
		}
	}
	for _, r := range native.Recent {
		if !reflect.DeepEqual(recent[r.ID], r) {
			t.Fatalf("native recent payload lost: %+v", r)
		}
	}
	limited, clipped, err := s.DocumentObservations(ctx, store.DocumentFilter{Query: "Synthetic tuple", Limit: 1})
	if err != nil || !clipped || len(limited) != 1 || limited[0].Usage.Document == nil ||
		limited[0].Usage.Document.ID != "native" || limited[0].Usage.Document.Format != "x\x1fy" {
		t.Fatalf("literal tuple order before limit: %+v %v", limited, err)
	}
}
