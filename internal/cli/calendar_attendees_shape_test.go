package cli

import (
	"math"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeNameProjectionRetainsClipMarker(t *testing.T) {
	e := newEnv(t)
	ref := attendeeCLIArchive(t, e, 1, false)
	code, out, stderr := e.run("--max-age", "0", "--fields", "name", "--max-text", "5", "calendar", "attendees", "list", ref)
	if code != 0 {
		t.Fatal(code, out, stderr)
	}
	if row := items(t, decode(t, out))[0]; row["text_truncated"] != true {
		t.Fatal("name projection hid clipping marker", row)
	}
}

func TestAttendeeShapeCopiesNullableLiterals(t *testing.T) {
	name, email := "Long \x00 Native Name", ""
	row := store.AttendeeRow{ID: math.MaxInt64, SourceID: math.MaxInt64, SnapshotID: math.MaxInt64, CaptureID: math.MaxInt64,
		Native: outlookattendees.Attendee{Name: &name, Email: &email, VersionRaw: math.MaxUint64}}
	first := shapeAttendee(row, 5, "/synthetic/archive with spaces.db")
	second := shapeAttendee(row, 0, "/synthetic/archive with spaces.db")
	if !first.TextTruncated || second.TextTruncated || *second.Name != name || *first.Email != "" || first.SourceID != "9223372036854775807" {
		t.Fatal("shaping lost literal/decimal data", first, second)
	}
	*first.Name = "changed"
	*first.Email = "changed"
	if name != "Long \x00 Native Name" || email != "" || *second.Name != name {
		t.Fatal("display projection mutated owned literal pointers")
	}
	if empty := shapeAttendee(store.AttendeeRow{}, 0, "/synthetic/archive.db"); empty.Name != nil || empty.Email != nil {
		t.Fatal("unknown text became known-empty")
	}
}
