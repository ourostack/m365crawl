package transcripts

import (
	"strings"
	"testing"
	"time"
)

func ms(n int64) *int64 { return &n }

func TestStitchOrdersAndMarksSeams(t *testing.T) {
	fetched := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	one := drivePart("c", "a", "m1", at(10, 0), at(11, 0))
	one.Ordinal, one.TranscribeOnly = 1, true
	two := drivePart("c", "b", "m2", at(10, 2), at(11, 0))
	two.Ordinal = 2
	for _, p := range []*Part{&one, &two} {
		p.Host, p.SiteRoot, p.TranscriptID = "h.example.invalid", "/teams/s", "t"
	}
	segs := Stitch([]StitchPart{
		{Part: two, State: StateOK, FetchedAt: &fetched, Entries: []Entry{{Speaker: "Bo", StartMS: ms(0), EndMS: ms(900), Text: "second"}}},
		{Part: one, State: StateOK, FetchedAt: &fetched, Entries: []Entry{{Speaker: "Ada", Text: "first"}}},
	})
	if len(segs) != 2 || segs[0].Ordinal != 1 || segs[1].Ordinal != 2 {
		t.Fatalf("order %+v", segs)
	}
	a, b := segs[0], segs[1]
	if !a.TranscribeOnly || b.TranscribeOnly || !a.StartsAt.Equal(at(10, 0)) || !b.StartsAt.Equal(at(10, 2)) ||
		a.State != StateOK || a.Reason != "" || a.FetchedAt == nil || len(a.Entries) != 1 || a.Entries[0].Text != "first" || b.Entries[0].Text != "second" {
		t.Fatalf("segments %+v", segs)
	}
}

func TestStitchUnfetchedPartKeepsPlace(t *testing.T) {
	fetched := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	mk := func(ord int, edit func(*Part)) Part {
		p := drivePart("call-x", "i", "m", at(10, ord), at(11, 0))
		p.Ordinal, p.Host, p.SiteRoot, p.TranscriptID = ord, "h.example.invalid", "/teams/s", "t"
		if edit != nil {
			edit(&p)
		}
		return p
	}
	parts := []StitchPart{
		{Part: mk(1, nil), State: StateOK, FetchedAt: &fetched, Entries: []Entry{{Text: "one"}}},
		{Part: mk(2, nil)},
		{Part: mk(3, nil), State: StateNoAccess},
		{Part: mk(4, nil), State: StateNotFound},
		{Part: mk(5, nil), State: StateNoTranscript},
		{Part: mk(6, nil), State: StateTooLarge},
		{Part: mk(7, nil), State: StateFailed},
		{Part: mk(8, func(p *Part) { p.RefQuality = RefShareOnly })},
		{Part: mk(9, func(p *Part) { p.RefQuality = RefAMSOnly })},
		{Part: mk(10, func(p *Part) { p.RefQuality = RefUnresolved })},
		{Part: mk(11, func(p *Part) { p.Host = "bad host" })},
		// Text fetched earlier stays readable after a later attempt was refused.
		{Part: mk(12, nil), State: StateNoAccess, FetchedAt: &fetched, Entries: []Entry{{Text: "kept"}}},
		// A stored ok with no text (the entries were never written) reads as not fetched.
		{Part: mk(13, nil), State: StateOK},
	}
	want := []struct{ state, reason string }{
		{StateOK, ""},
		{StateNotFetched, "not fetched yet; run m365crawl transcripts fetch call-x"},
		{StateNoAccess, "SharePoint refused access (HTTP 403); you may have lost access to this file"},
		{StateNotFound, "the file is gone (HTTP 404)"},
		{StateNoTranscript, "the file has no transcript"},
		{StateTooLarge, "the transcript is larger than 32 MiB"},
		{StateFailed, "the last fetch attempt failed"},
		{StateUnfetchable, "only a sharing link is cached for this part; m365crawl cannot fetch it from ids"},
		{StateUnfetchable, "only a Teams media-service link is cached; it is not a SharePoint file"},
		{StateUnfetchable, "Teams posted a transcript notice but no file reference"},
		{StateUnfetchable, "the cached file reference is malformed"},
		{StateOK, ""},
		{StateNotFetched, "not fetched yet; run m365crawl transcripts fetch call-x"},
	}
	segs := Stitch(parts)
	if len(segs) != len(want) {
		t.Fatalf("%d segments", len(segs))
	}
	for i, w := range want {
		s := segs[i]
		if s.Ordinal != i+1 || s.State != w.state || s.Reason != w.reason {
			t.Errorf("segment %d: %q %q, want %q %q", i+1, s.State, s.Reason, w.state, w.reason)
		}
		if (s.State == StateOK) != (len(s.Entries) > 0 && s.FetchedAt != nil) {
			t.Errorf("segment %d: entries %d, fetched %v in state %s", i+1, len(s.Entries), s.FetchedAt, s.State)
		}
	}
	// Only the wording the fetch ruling gives names the fetch command.
	for _, s := range segs {
		if strings.Contains(s.Reason, "transcripts fetch") != (s.State == StateNotFetched) {
			t.Errorf("segment %d names the fetch command in state %s", s.Ordinal, s.State)
		}
	}
	if len(Stitch(nil)) != 0 {
		t.Fatal("no parts, no segments")
	}
}
