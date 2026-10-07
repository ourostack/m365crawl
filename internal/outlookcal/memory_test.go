package outlookcal

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
)

// bigStore builds a store of n events, each with its own detail object whose body is about
// bodyBytes long, 20 events to a block. Every id is distinct.
func bigStore(t testing.TB, n, bodyBytes int) *hxstore.Store {
	b := hxbuild.New(hxbuild.Options{})
	body := "<p>" + strings.Repeat("Fixture body text. ", bodyBytes/19) + "</p>"
	var objs []*hxbuild.Object
	for i := 0; i < n; i++ {
		s := baseSpec(1)
		s.ID = hxbuild.GlobalObjectID(0, 0, 0, fmt.Sprintf("FIXTURE-BIG-%06d", i))
		s.DetailKey = uint32(1000 + i)
		objs = append(objs, hxbuild.NewEvent(s), hxbuild.NewDetail(hxbuild.DetailSpec{Key: uint32(1000 + i), JoinLink: "https://example.invalid/join", DialIn: "Fixture dial-in", BodyHTML: body, Lead: 3}))
		if len(objs) == 40 || i == n-1 {
			b.BlockCodec(framed(objs...), hxbuild.CodecMatches)
			objs = nil
		}
	}
	data := b.Bytes()
	s, err := OpenStore(bytesReaderAt(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// live is the heap bytes still reachable.
func live() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// peakLive records the most live heap seen at every 100th step of a read.
type peakLive struct{ base, max uint64 }

func (p *peakLive) step(done int) {
	if done%100 == 0 {
		if l := live() - p.base; l > p.max {
			p.max = l
		}
	}
}

// TestCollectMemoryBounded reads a store whose detail bodies total about 10 MB and checks that
// the heap the reader holds at its peak and the heap the result retains stay bounded.
func TestCollectMemoryBounded(t *testing.T) {
	const n, bodyBytes = 1000, 10_000
	s := bigStore(t, n, bodyBytes)
	var p peakLive
	progress = p.step
	defer func() { progress = func(int) {} }()
	p.base = live()
	res, err := Collect(context.Background(), s, "outlook/Test", Options{})
	if err != nil {
		t.Fatal(err)
	}
	retained := live() - p.base
	runtime.KeepAlive(res)
	if len(res.Events) != n {
		t.Fatalf("%d events", len(res.Events))
	}
	t.Logf("peak live: %d B, retained by the result: %d B (%d events, %d B of bodies)", p.max, retained, n, n*bodyBytes)
	// The result holds each body twice (the HTML and its text). The reader must not hold the
	// detail objects and the result's strings at full size together.
	if retained > uint64(n*bodyBytes)*3 || p.max > uint64(n*bodyBytes)*3 {
		t.Fatalf("peak %d B, retained %d B for %d B of bodies", p.max, retained, n*bodyBytes)
	}
}

// BenchmarkCollect reports allocation, the peak live heap during the read and the bytes the
// result retains.
func BenchmarkCollect(b *testing.B) {
	s := bigStore(b, 1000, 10_000)
	var p peakLive
	progress = p.step
	defer func() { progress = func(int) {} }()
	var kept uint64
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p.base, p.max = live(), 0
		res, err := Collect(context.Background(), s, "outlook/Test", Options{})
		if err != nil {
			b.Fatal(err)
		}
		kept = live() - p.base
		runtime.KeepAlive(res)
	}
	b.ReportMetric(float64(p.max), "peak-live-B")
	b.ReportMetric(float64(kept), "retained-B")
}

// TestCollectSharedAndOrphanDetails: a detail object two events link is kept until the last of
// them is mapped, and one no event links is dropped; both events read the shared body.
func TestCollectSharedAndOrphanDetails(t *testing.T) {
	a, b := baseSpec(1), baseSpec(2)
	a.DetailKey, b.DetailKey = 500, 500
	shared := baseDetail(0)
	orphan := baseDetail(0)
	orphan.Key = 999
	orphan.BodyHTML = "<p>Orphan body</p>"
	s := storeOf(t, framed(hxbuild.NewEvent(a), hxbuild.NewEvent(b), hxbuild.NewDetail(shared), hxbuild.NewDetail(orphan)))
	res := collect(t, s, Options{})
	if len(res.Events) != 2 || res.Notes.DetailMissing != 0 {
		t.Fatalf("%d events, %+v", len(res.Events), res.Notes)
	}
	for _, e := range res.Events {
		if e.BodyHTML != "<p>Fixture body</p>" {
			t.Fatalf("body %q", e.BodyHTML)
		}
	}
}
