package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
)

func TestAttendeeCollectorContracts(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root := attendeeTestRoot(t, false)
	info, cleanup, err := outlookSnapshot(ctx, filepath.Join(root, "Main", "HxStore.hxd"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cal, err := collectOutlookSelections(ctx, info, "outlook/Main", false, true)
	if err != nil || cal.AttendeeSelections == nil {
		t.Fatal(err)
	}
	for _, selection := range []*outlookcal.AttendeeSelections{nil, {Err: errors.New("outlook_attendee_selection_too_large")}} {
		if _, _, _, err := collectAttendeeCapture(ctx, info, selection); err == nil {
			t.Fatal("unqualified selections admitted")
		}
	}
	if _, _, _, err := collectAttendeeCapture(ctx, outlookdesktop.Info{Path: filepath.Join(t.TempDir(), "absent")}, cal.AttendeeSelections); err == nil {
		t.Fatal("absent private capture read")
	}
	duplicate := *cal.AttendeeSelections
	duplicate.Rows = append(append([]outlookcal.SelectionReceipt{}, duplicate.Rows...), duplicate.Rows[0])
	if _, _, _, err := collectAttendeeCapture(ctx, info, &duplicate); err == nil {
		t.Fatal("invalid built owned model admitted")
	}
}
