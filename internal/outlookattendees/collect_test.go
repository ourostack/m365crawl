package outlookattendees

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func source(t *testing.T, objects ...*hxbuild.Object) *hxstore.Store {
	t.Helper()
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, objects...))
	raw := b.Bytes()
	s, err := hxstore.Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func detail(key uint32) *hxbuild.Object {
	o := hxbuild.NewObject(0x6c, 0x348, 840)
	o.PutU32(20, key)
	return o
}

func event(key, link uint32) *hxbuild.Object {
	o := hxbuild.NewObject(0x6b, 0x455, 1109)
	o.PutU32(20, key)
	o.PutU32(180, link)
	return o
}

func TestCollectCopiesAndGeometry(t *testing.T) {
	first := attendeeObject(3, 8, text("First"), nil)
	second := attendeeObject(1, 9, nil, text("a@example.test"))
	conflict := attendeeObject(3, 8, text("Changed"), nil)
	got, err := Collect(context.Background(), source(t, detail(8), detail(8), event(6, 8), first, second, first, conflict))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Attendees) != 4 || got.Attendees[0].Key != 3 || got.Attendees[1].Key != 1 ||
		got.Attendees[2].Key != 3 || *got.Attendees[3].Name != "Changed" ||
		got.Attendees[0].PayloadPos >= got.Attendees[2].PayloadPos {
		t.Fatalf("physical source copies/order lost: %+v", got)
	}
	if !reflect.DeepEqual(got.AmbiguousKeys, []uint32{3}) ||
		!reflect.DeepEqual(got.Evidence, []Evidence{{DetailKey: 8, DetailCopies: 2, EventLinks: 1}, {DetailKey: 9}}) ||
		!reflect.DeepEqual(got.Losses, []Loss{{Code: "outlook_attendees_detail_missing", Count: 1}, {Code: "outlook_attendees_event_link_missing", Count: 1}}) {
		t.Fatalf("geometry/ambiguity qualification lost: %+v", got)
	}
}

func TestCollectIdentityConflictDimensions(t *testing.T) {
	for _, edit := range []func(*hxbuild.Object){
		func(o *hxbuild.Object) { o.PutU32(32, 4) },
		func(o *hxbuild.Object) { o.PutU64(112, 99) },
		func(o *hxbuild.Object) { o.PutU32(156, 0) },
		func(o *hxbuild.Object) { o.PutU32(164, 0) },
	} {
		a := attendeeObject(1, 2, text(""), text(""))
		b := attendeeObject(1, 2, text(""), text(""))
		edit(b)
		got, err := Collect(context.Background(), source(t, a, b))
		if err != nil || !reflect.DeepEqual(got.AmbiguousKeys, []uint32{1}) {
			t.Fatalf("differing copy falsely selected: %+v,%v", got, err)
		}
	}
}

func TestCollectMalformedAndWrongTag(t *testing.T) {
	for _, class := range []uint16{0x71, 0x6c, 0x6b} {
		o := hxbuild.NewObject(class, 1, 184)
		got, err := Collect(context.Background(), source(t, o))
		var guard *Error
		if !errors.As(err, &guard) || guard.Code != "outlook_attendees_layout_unsupported" || len(got.Attendees) != 0 {
			t.Fatalf("unqualified selected layout admitted: %+v,%v", got, err)
		}
	}
	shortDetail := hxbuild.NewObject(0x6c, 0x348, 24)
	shortDetail.PutU32(20, 2)
	shortEvent := hxbuild.NewObject(0x6b, 0x455, 184)
	shortEvent.PutU32(20, 3)
	shortEvent.PutU32(180, 2)
	got, err := Collect(context.Background(), source(t, detail(0), event(0, 2), shortDetail, shortEvent,
		attendeeObject(0, 2, nil, nil), attendeeObject(1, 2, nil, nil), event(4, 0)))
	if err != nil || len(got.Attendees) != 1 || got.Evidence[0].DetailCopies != 0 || got.Evidence[0].EventLinks != 0 {
		t.Fatalf("invalid geometry stated: %+v,%v", got, err)
	}
	want := []Loss{{"outlook_attendees_detail_missing", 1}, {"outlook_attendees_event_link_missing", 1}, {"outlook_attendees_geometry_unmapped", 2}, {"outlook_attendees_resynced", 1}, {"outlook_attendees_unmapped", 1}}
	if !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("fixed malformed evidence losses: %+v", got.Losses)
	}
}

func TestCollectEmptyAndNil(t *testing.T) {
	got, err := Collect(context.Background(), source(t, hxbuild.NewObject(1, 12, 12)))
	if err != nil || len(got.Attendees) != 0 || len(got.Evidence) != 0 || len(got.Losses) != 0 {
		t.Fatalf("scoped empty refused: %+v,%v", got, err)
	}
	_, err = Collect(context.Background(), nil)
	if err == nil {
		t.Fatal("nil source accepted")
	}
}

func TestCollectLimits(t *testing.T) {
	base := limits{attendees: 10, selected: 20, keys: 10, scalar: 10, stringBytes: 100}
	for _, tc := range []struct {
		name string
		cap  limits
		objs []*hxbuild.Object
	}{
		{"attendees", limits{attendees: 1, selected: 20, keys: 10, scalar: 10, stringBytes: 100}, []*hxbuild.Object{attendeeObject(1, 2, nil, nil), attendeeObject(2, 2, nil, nil)}},
		{"selected", limits{attendees: 10, selected: 1, keys: 10, scalar: 10, stringBytes: 100}, []*hxbuild.Object{detail(2), detail(2)}},
		{"event-selected", limits{attendees: 10, selected: 1, keys: 10, scalar: 10, stringBytes: 100}, []*hxbuild.Object{event(1, 0), event(2, 0)}},
		{"keys", limits{attendees: 10, selected: 20, keys: 1, scalar: 10, stringBytes: 100}, []*hxbuild.Object{detail(1), event(2, 2)}},
		{"attendee-parent-keys", limits{attendees: 10, selected: 20, keys: 1, scalar: 10, stringBytes: 100}, []*hxbuild.Object{attendeeObject(1, 2, nil, nil), attendeeObject(2, 3, nil, nil)}},
		{"scalar", limits{attendees: 10, selected: 20, keys: 10, scalar: 3, stringBytes: 100}, []*hxbuild.Object{attendeeObject(1, 2, text("four"), nil)}},
		{"strings", limits{attendees: 10, selected: 20, keys: 10, scalar: 10, stringBytes: 3}, []*hxbuild.Object{attendeeObject(1, 2, text("xx"), text("xx"))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collect(context.Background(), source(t, tc.objs...), tc.cap)
			var guard *Error
			if !errors.As(err, &guard) || guard.Code != "outlook_attendees_too_large" || len(got.Attendees) != 0 ||
				len(got.Evidence) != 0 || len(got.AmbiguousKeys) != 0 || len(got.Losses) != 0 {
				t.Fatalf("excess admitted or candidate leaked: %+v,%v", got, err)
			}
			if _, err := collect(context.Background(), source(t, tc.objs...), base); err != nil {
				t.Fatalf("same valid source refused in budget: %v", err)
			}
		})
	}
	exact := limits{attendees: 1, selected: 3, keys: 1, scalar: 2, stringBytes: 4}
	got, err := collect(context.Background(), source(t, detail(2), event(1, 2), attendeeObject(3, 2, text("xx"), text("xx"))), exact)
	if err != nil || len(got.Attendees) != 1 || got.Evidence[0].DetailCopies != 1 || got.Evidence[0].EventLinks != 1 {
		t.Fatalf("exact shared union and scalar bounds refused: %+v,%v", got, err)
	}
}

func TestCollectCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Collect(ctx, source(t, attendeeObject(1, 2, nil, nil)))
	if !errors.Is(err, context.Canceled) || len(got.Attendees) != 0 {
		t.Fatalf("cancelled input published: %+v,%v", got, err)
	}
}

type failingReader struct {
	raw *bytes.Reader
	err error
}

func (r failingReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= hxstore.FileHeaderSize {
		return 0, r.err
	}
	return r.raw.ReadAt(p, off)
}

func TestCollectPrivateReadError(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, attendeeObject(1, 2, nil, nil)))
	raw := b.Bytes()
	cause := errors.New("synthetic-private-source-value")
	s, err := hxstore.Open(failingReader{raw: bytes.NewReader(raw), err: cause}, int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), s)
	if err == nil || strings.Contains(err.Error(), "synthetic-private") || !errors.Is(err, cause) || len(got.Attendees) != 0 {
		t.Fatalf("read failure privacy/propagation failed: %+v,%v", got, err)
	}
}
