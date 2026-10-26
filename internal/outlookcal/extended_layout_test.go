package outlookcal

import (
	"context"
	"encoding/binary"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func extendedEvent(s hxbuild.EventSpec) *hxbuild.Object {
	old := obj(hxbuild.NewEvent(s)).Raw
	raw := append([]byte(nil), old[:708]...)
	raw = append(raw, 0x12, 0x34, 0x56, 0x78)
	raw = append(raw, old[708:]...)
	binary.LittleEndian.PutUint16(raw[2:], 0x459)
	o := hxbuild.NewObject(0x6b, 0x459, len(raw))
	for i, b := range raw {
		if i < 4 || i >= 8 {
			o.PutU8(i, b)
		}
	}
	return o
}

func TestExtendedEventMapsEveryEstablishedField(t *testing.T) {
	for _, extra := range []int{0, 772, 980} {
		s := baseSpec(1)
		s.EventType, s.ShowAs, s.Response = 3, 1, 1
		s.Online, s.Cancelled = true, true
		s.ExtraWord, s.ExtraString = extra, "Fixture extra"
		want, wantNotes := mapOK(t, ev(s), det(baseDetail(1)))
		o := obj(extendedEvent(s))
		got, notes := mapOK(t, o, det(baseDetail(1)))
		if !reflect.DeepEqual(got, want) || notes != wantNotes || !HasID(o) {
			t.Fatalf("extended field mapping differs at extra %d: %+v, %+v", extra, got, notes)
		}
	}
}

func TestCollectMixedEventLayoutsSelectsLatestCopy(t *testing.T) {
	old := baseSpec(1)
	old.Stamp = 1
	latest := old
	latest.Stamp, latest.Subject = 2, "Fixture latest"
	r, err := Collect(context.Background(), storeOf(t, framed(hxbuild.NewEvent(old), extendedEvent(latest), hxbuild.NewDetail(baseDetail(1)))),
		"outlook/Test", Options{AttendeeSelections: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(r.Events) != 1 || r.Events[0].Subject != "Fixture latest" || len(r.UnknownLayouts) != 0 ||
		r.Notes.SupersededCopies != 1 || r.AttendeeSelections == nil || len(r.AttendeeSelections.Rows) != 1 ||
		r.AttendeeSelections.Rows[0].DetailKey != 501 || r.AttendeeSelections.Rows[0].StampRaw != 2 {
		t.Fatalf("mixed-layout selection lost identity or detail binding: %+v", r)
	}
}

func TestUnknownAccountLayoutDoesNotPublishPartialIdentity(t *testing.T) {
	r := collect(t, storeOf(t, framed(extendedEvent(baseSpec(1)),
		hxbuild.NewAccount(hxbuild.AccountSpec{Address: "fixture@example.invalid"}),
		hxbuild.NewObject(0x49, 0x1a12, 6674))), Options{})
	if !r.IdentityUnknown || r.AccountAddresses != nil || len(r.Events) != 1 ||
		len(r.UnknownLayouts) != 1 || r.UnknownLayouts[0].Class != 0x49 || r.UnknownLayouts[0].Tag != 0x1a12 {
		t.Fatalf("unknown layout published a confirmed identity: %+v", r)
	}
}
