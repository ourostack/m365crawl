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
	return storeWithBodies(t, n, "<p>"+strings.Repeat("Fixture body text. ", bodyBytes/19)+"</p>")
}

// meetingBody is an invitation body of about size bytes: long HTML with join links, dial-in
// numbers and the values the scrubber looks for (a signed link, a token-shaped string, a
// credential name in a script block).
func meetingBody(size int) string {
	const block = `<div style="font-family:Segoe UI"><p>Join the meeting now: <a href="https://teams.example.invalid/l/meetup-join/19%3ameeting_FIXTURE%40thread.v2/0?context=%7b%22Tid%22%3a%22fixture%22%7d">Click here to join</a></p>` +
		`<p>Dial-in: +1 555 0100,,123456789# (Fixture City) | Conference ID: 123 456 789#</p>` +
		`<p>Safe link: <a href="https://safe.example.invalid/?url=https%3a%2f%2fexample.invalid/?sig=FIXTURESIGNATURE0123">example</a></p>` +
		`<p>Token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmaXh0dXJlIn0.fixture_signature_-</p>` +
		`<script>var o={"password":"fixture","name":"fixture"};</script><p>Fixture agenda text for the meeting and its notes. </p></div>`
	return "<html><body>" + strings.Repeat(block, max(size/len(block), 1)) + "</body></html>"
}

// storeWithBodies builds a store of n events, each with its own detail object holding body.
func storeWithBodies(t testing.TB, n int, body string) *hxstore.Store {
	b := hxbuild.New(hxbuild.Options{})
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

// BenchmarkCollectMeetingBodies maps events with realistic invitation bodies, most of which hold
// nothing the scrubber removes, and reports the bytes allocated for each event.
func BenchmarkCollectMeetingBodies(b *testing.B) {
	s := storeWithBodies(b, 200, meetingBody(20_000))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Collect(context.Background(), s, "outlook/Test", Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

// Mapping an event allocates a small multiple of its body: the HTML, its scrubbed copy where
// something was removed, and the plain text. A scrub that copied the body once per rule, or
// encoded it twice, was several times that.
func TestCollectMeetingBodiesAllocation(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector allocates for every access; the count means nothing under it")
	}
	const n, size = 200, 20_000
	for _, c := range []struct {
		name  string
		body  string
		limit uint64 // times the bodies' bytes
		redac bool
	}{
		{"with secrets", meetingBody(size), 40, true},
		{"plain", strings.NewReplacer("sig=", "id=", "eyJ", "xyz", "password", "pass", "&", "+").Replace(meetingBody(size)), 20, false},
	} {
		s := storeWithBodies(t, n, c.body)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		res, err := Collect(context.Background(), s, "outlook/Test", Options{})
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Events) != n || (res.Notes.Redacted >= n*3) != c.redac {
			t.Fatalf("%s: %d events, %d redactions", c.name, len(res.Events), res.Notes.Redacted)
		}
		got := after.TotalAlloc - before.TotalAlloc
		t.Logf("%s: %d B allocated for %d events with %d B bodies (%.1f times the bodies)", c.name, got, n, len(c.body), float64(got)/float64(n*len(c.body)))
		if got > uint64(n*len(c.body))*c.limit { //nolint:gosec // small test sizes
			t.Fatalf("%s: %d B allocated, more than %d times the %d B of bodies", c.name, got, c.limit, n*len(c.body))
		}
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

// Text that names a credential but holds none comes back as it went in, with nothing counted.
func TestScrubKeepsTextThatOnlyMentionsASecret(t *testing.T) {
	var n MapNotes
	r := reader{notes: &n}
	for _, s := range []string{"Please reset your password before the call", "the sig= parameter", "plain"} {
		if got := r.scrub(s); got != s {
			t.Errorf("scrub(%q) = %q", s, got)
		}
	}
}
