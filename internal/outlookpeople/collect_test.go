package outlookpeople

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

func personStore(t *testing.T, objects ...*hxbuild.Object) *hxstore.Store {
	t.Helper()
	b := hxbuild.New(hxbuild.Options{})
	if len(objects) != 0 {
		b.Block(hxbuild.FramedPayload(nil, objects...))
	}
	data := b.Bytes()
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCollectMapsOnlyKnownPeopleClasses(t *testing.T) {
	unrelated := hxbuild.NewObject(0x89, 0xea, 234)
	personString(unrelated, 168, 234, "not-a-person-source@example.test")
	got, err := Collect(context.Background(), personStore(t, unrelated, pairPerson(), relevantPerson()))
	if err != nil || len(got.People) != 2 || got.People[0].Class != 0xd2 || got.People[1].Class != 0x32a {
		t.Fatalf("people = %#v, error %v", got, err)
	}
	if len(got.Losses) != 0 || got.Stats.Objects != 3 {
		t.Fatalf("counts = %#v", got)
	}
}

func TestCollectRetainsUnorderedCopies(t *testing.T) {
	a, b := pairPerson(), pairPerson()
	a.PutU64(112, 0)
	b.PutU64(112, 29)
	reversed, err := Collect(context.Background(), personStore(t, b, a))
	if err != nil {
		t.Fatal(err)
	}
	forward, err := Collect(context.Background(), personStore(t, a, b))
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.People) != 2 || !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("unordered copies depend on file order: %#v, %#v", forward, reversed)
	}
	want := []ObjectKey{{Class: 0x32a, Key: 71}}
	if !reflect.DeepEqual(forward.AmbiguousKeys, want) {
		t.Fatalf("ambiguous keys = %#v, want %#v", forward.AmbiguousKeys, want)
	}
}

func TestCollectIdenticalCopiesAreNotAmbiguous(t *testing.T) {
	got, err := Collect(context.Background(), personStore(t, pairPerson(), pairPerson()))
	if err != nil || len(got.People) != 2 || len(got.AmbiguousKeys) != 0 {
		t.Fatalf("identical observations = %#v, error %v", got, err)
	}
}

func TestCollectOrdersMultipleAmbiguousKeys(t *testing.T) {
	a, b := relevantPerson(), relevantPerson()
	b.PutU64(112, 24)
	c, d := pairPerson(), pairPerson()
	c.PutU32(20, 72)
	d.PutU32(20, 72)
	d.PutU64(112, 24)
	e, f := pairPerson(), pairPerson()
	f.PutU64(112, 24)
	got, err := Collect(context.Background(), personStore(t, d, c, f, e, b, a))
	want := []ObjectKey{{Class: 0xd2, Key: 71}, {Class: 0x32a, Key: 71}, {Class: 0x32a, Key: 72}}
	if err != nil || !reflect.DeepEqual(got.AmbiguousKeys, want) {
		t.Fatalf("ambiguous key order = %#v, error %v", got.AmbiguousKeys, err)
	}
}

func TestCollectReportsMalformedRecord(t *testing.T) {
	bad := pairPerson()
	bad.PutU32(144, 3)
	got, err := Collect(context.Background(), personStore(t, bad, relevantPerson()))
	want := []Loss{{Code: "outlook_people_unmapped", Count: 1}}
	if err != nil || len(got.People) != 1 || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("malformed record = %#v, error %v", got, err)
	}
}

func TestCollectUnknownTargetLayoutRefusesAll(t *testing.T) {
	unknown := hxbuild.NewObject(0xd2, 0x15b, 347)
	got, err := Collect(context.Background(), personStore(t, relevantPerson(), unknown))
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Code != "outlook_people_layout_unsupported" || len(got.People) != 0 {
		t.Fatalf("unknown target = %#v, error %v", got, err)
	}
}

func TestCollectReportsResyncedObservations(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(append([]byte{0x42}, hxbuild.Payload(pairPerson())...))
	data := b.Bytes()
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), s)
	want := []Loss{{Code: "outlook_people_resynced", Count: 1}}
	if err != nil || len(got.People) != 1 || !got.People[0].Resynced || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("resynced = %#v, error %v", got, err)
	}
}

func TestCollectCountsDamageWithoutCallingOtherBlockTypesDamage(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.BlockType(9, []byte{0, 0, 0, 0})
	b.Block(hxbuild.FramedPayload(nil, relevantPerson()))
	b.Block(hxbuild.FramedPayload(nil, pairPerson()))
	data := b.Bytes()
	data[len(data)-1] ^= 1
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), s)
	want := []Loss{{Code: "outlook_blocks_damaged", Count: 1}}
	if err != nil || len(got.People) != 1 || !reflect.DeepEqual(got.Losses, want) {
		t.Fatalf("damage = %#v, error %v", got, err)
	}
}

func TestCollectRefusesIncompleteWalk(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(append(hxbuild.Payload(relevantPerson()), make([]byte, 2000)...))
	data := b.Bytes()
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(context.Background(), s)
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Code != "outlook_people_layout_unsupported" ||
		guard.Detail != "walk_coverage" || len(got.People) != 0 {
		t.Fatalf("incomplete walk = %#v, error %v", got, err)
	}
}

func TestCollectOrdersCleanAndResyncedCopies(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		b := hxbuild.New(hxbuild.Options{})
		clean := hxbuild.FramedPayload(nil, pairPerson())
		resynced := append([]byte{0x42}, hxbuild.Payload(pairPerson())...)
		if reverse {
			b.Block(resynced)
			b.Block(clean)
		} else {
			b.Block(clean)
			b.Block(resynced)
		}
		data := b.Bytes()
		s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Collect(context.Background(), s)
		if err != nil || len(got.People) != 2 || got.People[0].Resynced || !got.People[1].Resynced {
			t.Fatalf("resynced ordering = %#v, error %v", got, err)
		}
	}
}

func TestCollectBoundedRetention(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits collectLimits
	}{
		{"observations", collectLimits{people: 1, stringBytes: 1 << 20}},
		{"strings", collectLimits{people: 10, stringBytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := collect(context.Background(), personStore(t, relevantPerson(), pairPerson()), tc.limits)
			var guard *hxstore.GuardError
			if !errors.As(err, &guard) || guard.Code != "outlook_people_too_large" || len(got.People) != 0 {
				t.Fatalf("over limit = %#v, error %v", got, err)
			}
		})
	}
}

func TestCollectEmptyNilAndCancellation(t *testing.T) {
	got, err := Collect(context.Background(), personStore(t))
	if err != nil || len(got.People) != 0 || len(got.Losses) != 0 {
		t.Fatalf("empty = %#v, error %v", got, err)
	}
	if _, err := Collect(context.Background(), nil); err == nil {
		t.Fatal("nil store returned success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, personStore(t, relevantPerson())); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled collect = %v", err)
	}
}

type failingReader struct {
	r    io.ReaderAt
	fail bool
}

func (r *failingReader) ReadAt(b []byte, off int64) (int, error) {
	if r.fail {
		return 0, io.ErrUnexpectedEOF
	}
	return r.r.ReadAt(b, off)
}

type cancellingReader struct {
	r      io.ReaderAt
	cancel context.CancelFunc
}

func (r *cancellingReader) ReadAt(b []byte, off int64) (int, error) {
	n, err := r.r.ReadAt(b, off)
	if off >= 104 {
		r.cancel()
	}
	return n, err
}

func TestCollectCancellationAfterBlockReadDiscardsPeople(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, relevantPerson()))
	data := b.Bytes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &cancellingReader{r: bytes.NewReader(data), cancel: cancel}
	s, err := hxstore.Open(r, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Collect(ctx, s)
	if !errors.Is(err, context.Canceled) || len(got.People) != 0 {
		t.Fatalf("cancel after block = %#v, error %v", got, err)
	}
}

func TestCollectPropagatesReadFailure(t *testing.T) {
	b := hxbuild.New(hxbuild.Options{})
	b.Block(hxbuild.FramedPayload(nil, relevantPerson()))
	data := b.Bytes()
	r := &failingReader{r: bytes.NewReader(data)}
	s, err := hxstore.Open(r, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	r.fail = true
	got, err := Collect(context.Background(), s)
	if err == nil || len(got.People) != 0 {
		t.Fatalf("read failure = %#v, error %v", got, err)
	}
}
