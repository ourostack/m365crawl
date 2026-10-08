package transcripts

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var sent = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".xml")) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parse(t *testing.T, name string) Part {
	t.Helper()
	p, ok, err := ParseRecording("m-"+name, "19:meeting_x@thread.v2", fixture(t, name), sent)
	if err != nil || !ok {
		t.Fatalf("%s: ok %v, err %v", name, ok, err)
	}
	return p
}

func TestParseRecordingDriveItem(t *testing.T) {
	p := parse(t, "drive_item")
	want := Part{
		CallID: "call-1", ThreadID: "19:meeting_x@thread.v2", MessageID: "m-drive_item", PartKey: "d:b!driveA/01ITEMA",
		StartsAt: time.Date(2026, 11, 3, 10, 2, 0, 0, time.UTC), DurationSeconds: 3480.5,
		ContentTypes: "Recording+Transcript", ChunkIndex: "0",
		Host: "tenant.sharepoint.example.invalid", SiteRoot: "/teams/site-a", StorageKind: "SharedSharePoint",
		DriveID: "b!driveA", ItemID: "01ITEMA", TranscriptID: "11111111-2222-4333-8444-555555555555",
		ShareURL:   "https://tenant.sharepoint.example.invalid/teams/site-a/Shared%20Documents/Recordings/View%20Only/Weekly-20261103_100200-Meeting%20Recording.mp4?web=1",
		RefQuality: RefDriveItem, MeetingICalUID: "uid-o1", OriginalName: "Weekly-20261103_100200-Meeting Recording.mp4", SentAt: sent,
	}
	if p != want {
		t.Fatalf("part\n got %+v\nwant %+v", p, want)
	}
	if !p.Fetchable() || !p.HasTranscript() {
		t.Fatalf("fetchable %v, transcript %v", p.Fetchable(), p.HasTranscript())
	}
}

func TestParseRecordingPersonalSite(t *testing.T) {
	p := parse(t, "personal")
	if p.SiteRoot != "/personal/ada_example_invalid" || p.DriveID != "b!driveP" || p.ItemID != "01ITEMP" || p.RefQuality != RefDriveItem ||
		p.StorageKind != "MeetingOrganizerOneDrive" || p.DurationSeconds != 3723.25 || p.ChunkIndex != "" || p.TranscribeOnly {
		t.Fatalf("part %+v", p)
	}
}

func TestParseRecordingShareOnly(t *testing.T) {
	p := parse(t, "share_only")
	if p.RefQuality != RefShareOnly || len(p.PartKey) != 18 || p.PartKey[:2] != "u:" ||
		p.ShareURL != "https://tenant.sharepoint.example.invalid/:v:/t/site-a/EabcTOKEN" || p.DriveID != "" || p.Fetchable() {
		t.Fatalf("part %+v", p)
	}
	again := parse(t, "share_only")
	if again.PartKey != p.PartKey {
		t.Fatal("the key of a sharing link is not stable")
	}
}

func TestParseRecordingVideoPathOnly(t *testing.T) {
	p := parse(t, "video_only")
	if p.RefQuality != RefShareOnly || p.ShareURL != "https://tenant.sharepoint.example.invalid/teams/site-a/Shared%20Documents/Recordings/View%20Only/Only.mp4?web=1" || len(p.PartKey) != 18 {
		t.Fatalf("part %+v", p)
	}
}

func TestParseRecordingAMSOnly(t *testing.T) {
	p := parse(t, "ams_only")
	if p.RefQuality != RefAMSOnly || p.PartKey != "m:m-ams_only" || p.ShareURL != "" || p.Fetchable() {
		t.Fatalf("part %+v", p)
	}
}

func TestParseRecordingNonSuccess(t *testing.T) {
	for _, name := range []string{"initial", "chunk_finished"} {
		if p, ok, err := ParseRecording("m", "t", fixture(t, name), sent); ok || err != nil || p != (Part{}) {
			t.Fatalf("%s: %+v, %v, %v", name, p, ok, err)
		}
	}
	// A recording message that is call-log metadata, not a notice, is no part and no failure.
	if _, ok, err := ParseRecording("m", "t", `{"CallId":"x","ThreadId":"y"}`, sent); ok || err != nil {
		t.Fatalf("metadata: %v, %v", ok, err)
	}
}

func TestParseRecordingEscapedEntities(t *testing.T) {
	p := parse(t, "escaped")
	if p.SiteRoot != "/teams/r&d" || p.DriveID != "b!drive%2BE" || p.OriginalName != `R&D "sync"-20261105_090000.mp4` ||
		p.ShareURL != "https://tenant.sharepoint.example.invalid/teams/r&d/Shared%20Documents/x.mp4?web=1&x=2" ||
		!p.StartsAt.Equal(time.Date(2026, 11, 5, 9, 0, 0, 500000000, time.UTC)) || p.TranscribeOnly {
		t.Fatalf("part %+v", p)
	}
}

func TestParseRecordingMalformed(t *testing.T) {
	if _, ok, err := ParseRecording("m", "t", fixture(t, "malformed"), sent); ok || !errors.Is(err, ErrUnparsable) {
		t.Fatalf("truncated markup: %v, %v", ok, err)
	}
	// A Success notice that names no call cannot be keyed.
	noCall := `<URIObject><RecordingStatus status="Success"/></URIObject>`
	if _, ok, err := ParseRecording("m", "t", noCall, sent); ok || !errors.Is(err, ErrUnparsable) {
		t.Fatalf("no call id: %v, %v", ok, err)
	}
	if ErrUnparsable.Error() != "the recording notice cannot be parsed" {
		t.Fatal("the error must not quote the notice")
	}
}

func TestParseRecordingTranscribeOnly(t *testing.T) {
	if p := parse(t, "transcribe_only"); !p.TranscribeOnly || p.ChunkIndex != "10001" || p.ContentTypes != "Transcript" {
		t.Fatalf("part %+v", p)
	}
	if p := parse(t, "drive_item"); p.TranscribeOnly {
		t.Fatal("a recorded part with chunk 0 is not transcribe-only")
	}
	// Chunk 10001 marks a transcribe-only part whatever the content types say.
	both := `<URIObject><Identifiers><Id type="callId" value="c"/><Id type="chunkIndex" value="10001"/></Identifiers><RecordingStatus status="Success"/><RecordingContent contentTypes="Recording+Transcript"/></URIObject>`
	if p, ok, err := ParseRecording("m", "t", both, sent); !ok || err != nil || !p.TranscribeOnly {
		t.Fatalf("%+v, %v, %v", p, ok, err)
	}
}

// Identifiers and items count only under their own parent, and the first link wins.
func TestParseRecordingIgnoresStrayElements(t *testing.T) {
	stray := `<URIObject><Id type="callId" value="stray"/><item type="onedriveForBusinessVideo" uri="https://stray.example.invalid/x"/>` +
		`<Identifiers><Id type="callId" value="real"/></Identifiers><RecordingStatus status="Success"/>` +
		`<a href="https://one.example.invalid/a">1</a><a href="https://two.example.invalid/b">2</a></URIObject>`
	p, ok, err := ParseRecording("m", "t", stray, sent)
	if !ok || err != nil || p.CallID != "real" || p.ShareURL != "https://one.example.invalid/a" {
		t.Fatalf("%+v, %v, %v", p, ok, err)
	}
}

func TestParseStampAndDuration(t *testing.T) {
	if got := parseStamp("2026-11-03T10:02:00.1234567"); !got.Equal(time.Date(2026, 11, 3, 10, 2, 0, 123456700, time.UTC)) {
		t.Fatalf("zoneless stamp = %v", got)
	}
	if got := parseStamp("2026-11-03T12:02:00+02:00"); got.Location() != time.UTC || got.Hour() != 10 {
		t.Fatalf("offset stamp = %v", got)
	}
	if !parseStamp("yesterday").IsZero() || !parseStamp("").IsZero() {
		t.Fatal("a value that is no time must be zero")
	}
	for in, want := range map[string]float64{"0:00:01.500": 1.5, "2:03:04": 7384, "": 0, "90": 0, "a:b:c": 0, "0:-1:00": 0, "1:xx:00": 0, "1:00:x": 0} {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseTranscriptNotice(t *testing.T) {
	escaped := `{\"callId\":\"call-9\",\"iCalUid\":\"uid\",\"isExportedToOdsp\":true}`
	if id, ok := ParseTranscriptNotice(escaped); !ok || id != "call-9" {
		t.Fatalf("escaped: %q, %v", id, ok)
	}
	if id, ok := ParseTranscriptNotice(`{"callId":"call-8"}`); !ok || id != "call-8" {
		t.Fatalf("plain: %q, %v", id, ok)
	}
}

func TestParseTranscriptNoticeMalformed(t *testing.T) {
	for _, in := range []string{"", "<p>hello</p>", `{\"callId\":`, `{"iCalUid":"u"}`, `{"callId":""}`} {
		if id, ok := ParseTranscriptNotice(in); ok || id != "" {
			t.Errorf("%q: %q, %v", in, id, ok)
		}
	}
}

func TestPartRefValidation(t *testing.T) {
	good := parse(t, "drive_item")
	if !good.ValidRef() {
		t.Fatal("a plain reference must pass")
	}
	upper := good
	upper.Host = "Tenant.SharePoint.Example.Invalid"
	sites := good
	sites.SiteRoot = "/sites/site-b"
	for _, p := range []Part{upper, sites} {
		if !p.ValidRef() || !p.Fetchable() {
			t.Errorf("%+v must pass", p)
		}
	}
	for name, edit := range map[string]func(*Part){
		"host with a path":        func(p *Part) { p.Host = "evil.example.invalid/x" },
		"host with a port":        func(p *Part) { p.Host = "evil.example.invalid:8443" },
		"host with credentials":   func(p *Part) { p.Host = "u@evil.example.invalid" },
		"empty host":              func(p *Part) { p.Host = "" },
		"unknown site kind":       func(p *Part) { p.SiteRoot = "/other/site-a" },
		"site root with a query":  func(p *Part) { p.SiteRoot = "/teams/site-a?x=1" },
		"site root with a deeper": func(p *Part) { p.SiteRoot = "/teams/site-a/b" },
		"drive id with a slash":   func(p *Part) { p.DriveID = "b!x/y" },
		"item id with a quote":    func(p *Part) { p.ItemID = `01"x` },
		"transcript id with js":   func(p *Part) { p.TranscriptID = "x');alert(1" },
		"empty drive id":          func(p *Part) { p.DriveID = "" },
	} {
		p := good
		edit(&p)
		if p.ValidRef() || p.Fetchable() {
			t.Errorf("%s must fail", name)
		}
	}
	share := parse(t, "share_only")
	if share.Fetchable() {
		t.Fatal("a sharing link is not fetchable")
	}
}
