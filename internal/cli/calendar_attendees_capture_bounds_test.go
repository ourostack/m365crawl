package cli

import (
	"math"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeOwnCaptureRecoveryIsBoundedAndPositive(t *testing.T) {
	e := newEnv(t)
	rt := &runtime{g: &Globals{}, dbPath: e.db}
	snapshot := store.AttendeeSnapshot{}
	snapshot.Rows = append(snapshot.Rows, store.AttendeeRow{SnapshotID: 0}, store.AttendeeRow{SnapshotID: -1})
	for i := int64(1); i <= 201; i++ {
		snapshot.Rows = append(snapshot.Rows, store.AttendeeRow{SnapshotID: i}, store.AttendeeRow{SnapshotID: i})
	}
	coverage := rt.attendeeCoverage(snapshot)
	if len(coverage.OwnCaptures) != 200 {
		t.Fatal("own recovery must deduplicate and stay bounded by the two100-element previews", len(coverage.OwnCaptures))
	}
	for _, capture := range coverage.OwnCaptures {
		if capture.SnapshotID == "0" || capture.SnapshotID == "-1" || capture.SnapshotID == "201" {
			t.Fatal("invalid or overflow capture route escaped bounded output", capture)
		}
	}
	large, valid := shapeAttendeeCapture(math.MaxInt64, e.db)
	if !valid || large.FullCaptureSQL != "SELECT * FROM outlook_attendee_snapshots WHERE rowid=9223372036854775807" ||
		!strings.Contains(large.FullCaptureCommand, "--max-text 0") {
		t.Fatal("own snapshot ID rounded, interpolated or clipped", large)
	}
}
