package outlookmail

import (
	"bytes"
	"context"
	"runtime"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// liveHeap is the heap still reachable after a full collection.
func liveHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// A real store holds hundreds of thousands of objects and the read must not keep them. Collect
// holds what its mappers returned, not the objects' bytes: when the walk is done, the heap that
// is reachable must be a fraction of the bytes of the objects the walk kept. (Holding every
// object, as the first version did, kept about as much as the store's payload.)
func TestCollectDoesNotKeepWalkedObjects(t *testing.T) {
	mix := hxbuild.MixOptions{Events: 100, Messages: 1500, Recipients: 8500, Filler: 15000, FillerNoise: 600, BodyBytes: 600, Codec: hxbuild.CodecLiteral}
	data := hxbuild.Mix(mix)
	s, err := hxstore.OpenStore(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	data = nil
	var afterWalkLive, base uint64
	old := afterWalk
	afterWalk = func() { afterWalkLive = liveHeap() - base }
	defer func() { afterWalk = old }()
	base = liveHeap()
	res, err := Collect(context.Background(), s, t.TempDir(), "outlook/Test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(res)
	runtime.KeepAlive(s) // the store holds the file's bytes in this test; they are in both measurements
	if len(res.Messages) != mix.Messages {
		t.Fatalf("%d messages, want %d", len(res.Messages), mix.Messages)
	}
	// The objects the reader keeps are the message classes: header, detail, body, recipient.
	kept := res.Stats.Pairs[hxstore.Pair{Class: ClassHeader, Tag: TagHeader}]*hxbuild.MailHeaderSize +
		res.Stats.Pairs[hxstore.Pair{Class: ClassDetail, Tag: TagDetail}]*hxbuild.MailDetailSize +
		res.Stats.Pairs[hxstore.Pair{Class: ClassBody, Tag: TagBody}]*(hxbuild.MailBodySize+mix.BodyBytes) +
		res.Stats.Pairs[hxstore.Pair{Class: ClassRecipient, Tag: TagRecipient}]*hxbuild.RecipientSize
	t.Logf("after the walk: %d B reachable; the kept objects are %d B", afterWalkLive, kept)
	if afterWalkLive > uint64(kept)*3/5 { //nolint:gosec // a small non-negative test size
		t.Fatalf("%d B reachable after the walk, more than 60%% of the %d B of objects it kept", afterWalkLive, kept)
	}
}
