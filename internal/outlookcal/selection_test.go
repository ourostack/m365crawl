package outlookcal

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func TestAttendeeSelectionActualWinner(t *testing.T) {
	old, newest := baseSpec(1), baseSpec(1)
	old.DetailKey, newest.DetailKey = 501, 502
	newest.LastModified = at(3, 8)
	newest.Stamp = 99
	d1, d2 := baseDetail(1), baseDetail(2)
	different := d2
	different.BodyHTML = "<p>Different synthetic copy</p>"
	bad := hxbuild.NewEvent(baseSpec(3))
	bad.PutU64(584, 0)
	s := storeOf(t, framed(hxbuild.NewEvent(newest), hxbuild.NewDetail(d2)),
		framed(hxbuild.NewEvent(old), hxbuild.NewDetail(d1), hxbuild.NewDetail(different), bad))
	r := collect(t, s, Options{AttendeeSelections: true})
	if len(r.Events) != 1 || r.AttendeeSelections == nil || r.AttendeeSelections.Err != nil || len(r.AttendeeSelections.Rows) != 1 {
		t.Fatalf("receipts must follow successful actual mapping: %+v", r.AttendeeSelections)
	}
	got := r.AttendeeSelections.Rows[0]
	if got.SourceID != strings.ToLower(hxbuild.HexID(newest.ID)) || got.DetailKey != 502 ||
		got.LastModifiedRaw != hxbuild.Ticks(at(3, 8)) || got.StampRaw != 99 ||
		got.DetailCopies != 2 || got.DetailResyncedCopies != 0 || !got.DetailPresent || !got.DetailDiffering ||
		got.BlockOffset <= 0 || got.PayloadPos <= 0 {
		t.Fatalf("wrong winner/detail provenance: %+v", got)
	}
	plain := collect(t, s, Options{})
	if plain.AttendeeSelections != nil {
		t.Fatal("unrequested receipts allocated")
	}
	r.AttendeeSelections = nil
	if !reflect.DeepEqual(r, plain) {
		t.Fatal("optional receipts changed legacy output")
	}
}

func TestAttendeeSelectionActualWinnerRawIDsAndTies(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second hxbuild.EventSpec
		want          uint32
	}{
		{"last_modified", copySpec("first", 3, 1), copySpec("second", 2, 99), 501},
		{"stamp", copySpec("first", 2, 9), copySpec("second", 2, 4), 501},
		{"position", copySpec("first", 2, 9), copySpec("second", 2, 9), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.first.DetailKey, tc.second.DetailKey = 501, 502
			r := collect(t, storeOf(t, framed(hxbuild.NewEvent(tc.first), hxbuild.NewEvent(tc.second))), Options{AttendeeSelections: true})
			if len(r.AttendeeSelections.Rows) != 1 || r.AttendeeSelections.Rows[0].DetailKey != tc.want || r.AttendeeSelections.Rows[0].DetailPresent {
				t.Fatalf("receipt selected independently of mapper: %+v", r.AttendeeSelections)
			}
		})
	}
	first := hxbuild.NewEvent(baseSpec(1))
	second := hxbuild.NewEvent(baseSpec(1))
	lower := strings.ToLower(hxbuild.HexID(baseSpec(1).ID))
	for i := range len(lower) {
		second.PutU8(evFixed+2*i, lower[i])
	}
	second.PutU32(evDetailLink, 502)
	r := collect(t, storeOf(t, framed(first, second)), Options{AttendeeSelections: true})
	if len(r.Events) != 2 || len(r.AttendeeSelections.Rows) != 2 ||
		r.AttendeeSelections.Rows[0].SourceID != r.AttendeeSelections.Rows[1].SourceID ||
		r.AttendeeSelections.Rows[0].DetailKey == r.AttendeeSelections.Rows[1].DetailKey {
		t.Fatalf("distinct raw IDs were collapsed by public UID: %+v", r.AttendeeSelections)
	}
}

func TestAttendeeSelectionOptionalBounds(t *testing.T) {
	s := storeOf(t, framed(hxbuild.NewEvent(baseSpec(1)), hxbuild.NewEvent(baseSpec(2))))
	cap := defaultSelectionLimits()
	cap.receipts = 2
	r, err := collectSelections(context.Background(), s, "outlook/Test", Options{AttendeeSelections: true}, cap)
	if err != nil || r.AttendeeSelections.Err != nil || len(r.AttendeeSelections.Rows) != 2 {
		t.Fatalf("inclusive receipt bound refused: %+v %v", r.AttendeeSelections, err)
	}
	cap.receipts = 1
	r, err = collectSelections(context.Background(), s, "outlook/Test", Options{AttendeeSelections: true}, cap)
	if err != nil || len(r.Events) != 2 || r.AttendeeSelections.Err == nil ||
		r.AttendeeSelections.Err.Error() != "outlook_attendee_selection_too_large" || r.AttendeeSelections.Rows != nil {
		t.Fatalf("overflow clipped receipt or dropped legacy event: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Collect(ctx, s, "outlook/Test", Options{AttendeeSelections: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled traversal succeeded: %v", err)
	}
}

func TestAttendeeSelectionAuxiliaryBounds(t *testing.T) {
	s := storeOf(t, framed(hxbuild.NewDetail(baseDetail(1)), hxbuild.NewDetail(baseDetail(2)), hxbuild.NewEvent(baseSpec(1))))
	for _, dimension := range []string{"keys", "selected"} {
		t.Run(dimension, func(t *testing.T) {
			cap := defaultSelectionLimits()
			if dimension == "keys" {
				cap.keys = 2
			} else {
				cap.selected = 3
			}
			r, err := collectSelections(context.Background(), s, "outlook/Test", Options{AttendeeSelections: true}, cap)
			if err != nil || r.AttendeeSelections.Err != nil || len(r.Events) != 1 {
				t.Fatalf("inclusive auxiliary bound refused: %+v %v", r.AttendeeSelections, err)
			}
			if dimension == "keys" {
				cap.keys = 1
			} else {
				cap.selected = 2
			}
			r, err = collectSelections(context.Background(), s, "outlook/Test", Options{AttendeeSelections: true}, cap)
			if err != nil || len(r.Events) != 1 || r.AttendeeSelections.Err == nil || r.AttendeeSelections.Rows != nil {
				t.Fatalf("unused details escaped auxiliary budget: %+v %v", r.AttendeeSelections, err)
			}
		})
	}
}

func TestAttendeeSelectionOptionalLiteralBounds(t *testing.T) {
	makeState := func() *selectionState {
		return &selectionState{result: &AttendeeSelections{}, cap: defaultSelectionLimits(), details: map[uint32]detailCounts{}}
	}
	o := ev(baseSpec(1))
	for _, tc := range []struct {
		name  string
		count int
		id    string
		extra string
	}{
		{"receipts", 65536, "a", "a"},
		{"scalar", 1, strings.Repeat("a", 262144), strings.Repeat("a", 262145)},
		{"id_total", 32, strings.Repeat("a", 262144), "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := makeState()
			for range tc.count {
				s.emit(tc.id, o, 0, false, false)
			}
			if s.result.Err != nil || len(s.result.Rows) != tc.count {
				t.Fatalf("exact literal cap refused: %v", s.result.Err)
			}
			s.emit(tc.extra, o, 0, false, false)
			if s.result.Err == nil || s.result.Rows != nil || s.details != nil || s.idBytes != 0 || s.selected != 0 {
				t.Fatal("literal cap+1 retained optional state")
			}
			s.emit("ignored", o, 0, false, false)
			s.observe(o)
			if s.result.Rows != nil || s.details != nil {
				t.Fatal("refused optional state resumed accumulating")
			}
		})
	}
}

func TestAttendeeSelectionAuxiliaryLiteralBounds(t *testing.T) {
	s := &selectionState{result: &AttendeeSelections{}, cap: defaultSelectionLimits(), details: map[uint32]detailCounts{}}
	raw := make([]byte, tagDetail)
	o := hxstore.Object{Raw: raw, Class: classDetail, Tag: tagDetail, Resynced: true}
	for i := uint32(1); i <= 65536; i++ {
		binary.LittleEndian.PutUint32(raw[dtKey:], i)
		s.observe(o)
	}
	if len(s.details) != 65536 || s.details[65536] != (detailCounts{total: 1, resynced: 1}) {
		t.Fatal("exact unused/resync detail-key budget refused")
	}
	binary.LittleEndian.PutUint32(raw[dtKey:], 65537)
	s.observe(o)
	if s.result.Err == nil || s.details != nil {
		t.Fatal("unused resync key+1 escaped budget")
	}
	s = &selectionState{result: &AttendeeSelections{}, cap: defaultSelectionLimits(), details: map[uint32]detailCounts{}}
	unknown := hxstore.Object{Class: 0x71, Tag: 1}
	for range 524288 {
		s.observe(unknown)
	}
	if s.result.Err != nil {
		t.Fatal("exact pre-filter selected-object budget refused")
	}
	s.observe(unknown)
	if s.result.Err == nil || s.details != nil {
		t.Fatal("wrong-tag selected observation+1 escaped budget")
	}
}

func TestAttendeeSelectionCancellationDuringMapping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := progress
	defer func() { progress = previous }()
	progress = func(n int) {
		if n == 1 {
			cancel()
		}
	}
	r, err := Collect(ctx, storeOf(t, framed(hxbuild.NewEvent(baseSpec(1)))), "outlook/Test", Options{AttendeeSelections: true})
	if !errors.Is(err, context.Canceled) || r.AttendeeSelections == nil || len(r.AttendeeSelections.Rows) != 0 {
		t.Fatalf("cancelled mapping published optional success: %+v %v", r.AttendeeSelections, err)
	}
}

func TestAttendeeSelectionZeroOrUnreadableDetailKey(t *testing.T) {
	s := &selectionState{result: &AttendeeSelections{}, cap: defaultSelectionLimits(), details: map[uint32]detailCounts{}}
	s.observe(hxstore.Object{Class: classDetail, Tag: tagDetail})
	s.observe(hxstore.Object{Class: classDetail, Tag: tagDetail, Raw: make([]byte, tagDetail)})
	if len(s.details) != 0 || s.result.Err != nil {
		t.Fatal("unknown/zero key allocated detail counters")
	}
}
