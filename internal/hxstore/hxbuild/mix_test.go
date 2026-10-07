package hxbuild

import (
	"bytes"
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
)

// Mix writes exactly the objects it says, in blocks of MixBlockObjects, and the same bytes each time.
func TestMixObjectMix(t *testing.T) {
	mix := MixOptions{Events: 40, EventIDs: 16, Details: 4, Messages: 20, Recipients: 50, Filler: 90, FillerNoise: 64, BodyBytes: 100}
	data := Mix(mix)
	if !bytes.Equal(data, Mix(mix)) {
		t.Fatal("Mix is not deterministic")
	}
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Walk(context.Background(), hxstore.WalkOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Objects != mix.MixObjects() || st.ObjectsResynced != 0 || st.BlocksValid != (mix.MixObjects()+MixBlockObjects-1)/MixBlockObjects {
		t.Fatalf("%d objects (%d resynced) in %d blocks, want %d", st.Objects, st.ObjectsResynced, st.BlocksValid, mix.MixObjects())
	}
	want := map[hxstore.Pair]int{
		{Class: ClassEvent, Tag: TagEvent}: 40, {Class: ClassDetail, Tag: TagDetail}: 4,
		{Class: ClassMailHeader, Tag: TagMailHeader}: 20, {Class: ClassMailDetail, Tag: TagMailDetail}: 20,
		{Class: ClassMailBody, Tag: TagMailBody}: 20, {Class: ClassRecipient, Tag: TagRecipient}: 50,
		{Class: ClassFiller, Tag: TagFiller}: 90, {Class: ClassMailFolder, Tag: TagMailFolder}: 1,
	}
	for p, n := range want {
		if st.Pairs[p] != n {
			t.Errorf("%+v: %d objects, want %d", p, st.Pairs[p], n)
		}
	}
	// Details default to one per event, and a mix with no messages still has recipients.
	bare := MixOptions{Events: 3, Recipients: 2}
	if bare.MixObjects() != 3+3+2+1 {
		t.Fatalf("bare mix: %d objects", bare.MixObjects())
	}
	if _, err := hxstore.Open(bytes.NewReader(Mix(bare)), int64(len(Mix(bare)))); err != nil {
		t.Fatal(err)
	}
}

// With FileBodies every other body names a file and a detail body can be of its own size.
func TestMixFileBodiesAndDetailBytes(t *testing.T) {
	mix := MixOptions{Events: 2, Messages: 4, FileBodies: true, BodyBytes: 4000, DetailBytes: 100}
	data := Mix(mix)
	if want := Mix(MixOptions{Events: 2, Messages: 4, BodyBytes: 4000, DetailBytes: 100}); len(data) >= len(want) {
		t.Fatalf("a mix with file bodies is %d B, one with inline bodies %d B", len(data), len(want))
	}
	if MixBodyPath(3) != "~/Files/mix-3.dat" {
		t.Fatal(MixBodyPath(3))
	}
	s, err := hxstore.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Walk(context.Background(), hxstore.WalkOptions{}, nil)
	if err != nil || st.Objects != mix.MixObjects() {
		t.Fatalf("%d objects, %v", st.Objects, err)
	}
}
