package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/browser"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/transcripts"
)

func TestContentFirst100StructuredPreview(t *testing.T) {
	long := strings.Repeat("☃", 130)
	clock, speaker := "unmapped clock", "Synthetic speaker"
	result := sharepointcontent.Result{Kind: "stream", State: "transcript_observations", Transcript: &sharepointcontent.Transcript{ID: long}}
	for i := 0; i < 101; i++ {
		result.Transcript.Entries = append(result.Transcript.Entries, sharepointcontent.TranscriptEntry{Ordinal: i, ID: long, Text: "text repeated", SpeakerDisplayName: &speaker, StartRaw: &clock})
	}
	current := "other"
	source := store.CloudSource{CurrentTranscriptID: &current, RegistrationID: 2}
	it := cloudItemOf(store.CloudObservationRow{ID: 1, SourceID: 3, Result: result}, source, 4)
	if len(it.EntriesPreview) != 100 || it.EntriesTotal != 101 || !it.PreviewTruncated || it.SelectionMatch != "differs" || !it.SelectedTranscript.NativeIDTruncated || len([]rune(*it.SelectedTranscript.NativeIDPrefix)) != 128 || !it.DescriptiveFieldsTruncated {
		t.Fatal("entry preview loses totals/native prefix/selection")
	}
	for i, entry := range it.EntriesPreview {
		if entry.Ordinal != i || entry.StartMS != nil || !entry.TextTruncated {
			t.Fatal("entry order or raw clocks changed")
		}
	}
	source.CurrentTranscriptID = &long
	if cloudItemOf(store.CloudObservationRow{Result: result}, source, 0).SelectionMatch != "matches" {
		t.Fatal("matching selection")
	}
	result = sharepointcontent.Result{Kind: "page", State: "page_observations"}
	typ := int64(4)
	control := sharepointcontent.PageControl{Ordinal: 0, ID: &long, Type: &typ, State: "text"}
	for i := 0; i < 101; i++ {
		control.Texts = append(control.Texts, "repeat")
	}
	result.PageControls = []sharepointcontent.PageControl{control}
	it = cloudItemOf(store.CloudObservationRow{ID: 1, Result: result}, store.CloudSource{}, 0)
	if len(it.ControlsPreview) != 100 || it.TextOccurrencesTotal != 101 || !it.PreviewTruncated || it.Scope != "partial" {
		t.Fatal("control-text occurrence clipping")
	}
	if cloudSourceFor(nil, 1).RegistrationID != 0 {
		t.Fatal("unknown source")
	}
}

func TestContentOfflineAbsentAndInvalidContracts(t *testing.T) {
	e := cloudCLIEnv(t)
	for _, args := range [][]string{{"content", "list"}, {"content", "search", "word"}, {"content", "sources"}, {"content", "sources", "--source-id", "1"}, {"content", "show", "cloud-observation:1"}} {
		code, _, _ := e.run(args...)
		if (len(args) == 2 || args[1] == "search") && code != 0 {
			t.Fatal("optional absent archive read failed")
		}
	}
	for _, args := range [][]string{
		{"content", "search", " "}, {"content", "search", "word", "--limit", "0"},
		{"content", "list", "--source-id=-1"}, {"content", "list", "--modified-until", "invalid"},
		{"content", "list", "--source-id", "999"}, {"content", "sources", "--source-id=-1"},
		{"content", "sources", "--fields", "bad"}, {"content", "show", "cloud-observation:1", "--fields", "bad"},
		{"content", "show", "wrong"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("invalid offline args admitted")
		}
	}
	if _, err := os.Stat(e.db); !os.IsNotExist(err) {
		t.Fatal("offline absent created archive")
	}
	installCloudFixture(t, nil)
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, stderr := e.run("content", "sources"); code != 0 {
		t.Fatal(stderr)
	}
	if code, _, _ := e.run("content", "sources", "--source-id", "999"); code == 0 {
		t.Fatal("wrong family/source accepted")
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db, stdout: io.Discard}
	if err := rt.contentRead("content list", func(*store.Store) (*listResult, error) { return nil, errors.New("synthetic") }); err == nil {
		t.Fatal("callback failure masked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt.ctx = ctx
	if err := rt.cloudWrite(ctx, func(*store.Store) error { t.Fatal("cancelled write ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled write")
	}
	old := openBrowser
	t.Cleanup(func() { openBrowser = old })
	request, _ := sharepointcontent.NormalizeRequest(sharepointcontent.Request{Kind: "page", URL: "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"})
	if _, err := captureCloud(ctx, browser.LaunchOptions{}, request); !errors.Is(err, context.Canceled) {
		t.Fatal("cancel capture")
	}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return nil, nil, errors.New("private")
	}
	if _, err := captureCloud(context.Background(), browser.LaunchOptions{}, request); err == nil {
		t.Fatal("launch failure")
	}
}

func TestContentColdOfflineReadAndPreview(t *testing.T) {
	e := cloudCLIEnv(t)
	installCloudFixture(t, nil)
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	installCloudFixture(t, func() { t.Fatal("offline read opened browser") })
	for _, format := range []string{"json", "text", "log"} {
		code, out, stderr := e.run("--format", format, "--max-age", "1ns", "content", "search", "searchableword")
		if code != 0 || !strings.Contains(out, "cloud-observation:") {
			t.Fatalf("offline read: %s", stderr)
		}
	}
	if code, out, stderr := e.run("--format", "text", "search", "searchableword", "--source", "content"); code != 0 || !strings.Contains(out, "content show cloud-observation:") {
		t.Fatalf("shared offline text followup: %s", stderr)
	}
	code, out, stderr := e.run("content", "list", "--fields", "id", "--max-text", "4")
	if code != 0 {
		t.Fatal(stderr)
	}
	m := decode(t, out)
	if m["content_coverage"] == nil || len(items(t, m)) != 1 {
		t.Fatal("coverage shaped away")
	}
	id := items(t, m)[0]["id"].(string)
	code, out, stderr = e.run("content", "show", id)
	if code != 0 {
		t.Fatal(stderr)
	}
	it := items(t, decode(t, out))[0]
	if it["full_text_sql"] == nil || it["source_id"] == nil {
		t.Fatal("safe followup absent")
	}
	if code, out, stderr := e.run("sql", it["full_text_sql"].(string)); code != 0 || !strings.Contains(out, "searchableword") {
		t.Fatalf("full archive-only SQL: %s", stderr)
	}
	for _, bad := range []string{"cloud-observation:0", "cloud-observation:-1", "cloud-observation:+1", "cloud-observation:1;select", "cloud-observation:9223372036854775808", "cloud-observation:999"} {
		if code, _, _ := e.run("content", "show", bad); code == 0 {
			t.Fatal("bad ID accepted")
		}
	}
	if code, _, _ := e.run("--account", "tenant/user", "content", "list"); code == 0 {
		t.Fatal("unresolved ownership filtered")
	}
	if code, _, _ := e.run("content", "list", "--fields", "bad"); code == 0 {
		t.Fatal("unknown fields")
	}
	if code, _, _ := e.run("content", "list", "--modified-since", "invalid"); code == 0 {
		t.Fatal("bad native time")
	}
}

func TestCloudZeroHitCoverageShared(t *testing.T) {
	e := cloudCLIEnv(t)
	installCloudFixture(t, nil)
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	for _, query := range []string{"absent", "searchableword"} {
		code, out, stderr := e.run("--no-sync", "search", query, "--source", "content", "--fields", "id")
		if code != 0 {
			t.Fatal(stderr)
		}
		m := decode(t, out)
		if m["content_coverage"] == nil || m["sources"].(map[string]any)["content"] == nil {
			t.Fatal("shared coverage lost")
		}
	}
	if code, _, _ := e.run("search", "word", "--source", "content", "--from", "author"); code == 0 {
		t.Fatal("unsupported filter")
	}
}

func TestCloudMergePreservesEqualTimeLegacyOrder(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	legacy := []searchHit{{at: at, source: searchChats, item: "z"}, {at: at, source: searchMail, item: "a"}, {at: at, source: searchNotes, item: "b"}}
	for _, cloud := range []bool{false, true} {
		hits := append([]searchHit{}, legacy...)
		if cloud {
			hits = append(hits, searchHit{at: at, source: searchContent, cloudLabel: "0", cloudSourceID: 1, cloudObservationID: 1})
		}
		sortSearchHits(hits)
		for i := range legacy {
			if hits[i].item != legacy[i].item {
				t.Fatal("legacy tie order changed")
			}
		}
		if hits[:1][0].item != "z" {
			t.Fatal("limit1 legacy winner changed")
		}
	}
}
func TestCloudMergeTiesUseFullNativeKeys(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prefix := strings.Repeat("x", 130)
	hits := []searchHit{
		{at: at, source: searchContent, cloudLabel: prefix + "b", cloudSourceID: 1, cloudObservationID: 1},
		{at: at, source: searchContent, cloudLabel: prefix + "a", cloudSourceID: 2, cloudObservationID: 10},
		{at: at, source: searchContent, cloudLabel: prefix + "a", cloudSourceID: 1, cloudObservationID: 10},
		{at: at, source: searchContent, cloudLabel: prefix + "a", cloudSourceID: 1, cloudObservationID: 2},
	}
	sortSearchHits(hits)
	want := []string{"2", "10", "10", "1"}
	for i, h := range hits {
		if strconv.FormatInt(h.cloudObservationID, 10) != want[i] {
			t.Fatal("clipped or stringified ID sort")
		}
	}
	hits = []searchHit{{at: at, source: searchContent}, {at: at, source: searchChats, item: "legacy"}, {at: at.Add(time.Hour), source: searchMail, item: "newer"}}
	sortSearchHits(hits)
	if hits[0].item != "newer" || hits[1].item != "legacy" {
		t.Fatal("timestamp/legacy precedes cloud")
	}
}

func TestCloudZeroHitCoverageAfterGlobalClip(t *testing.T) {
	e := cloudCLIEnv(t)
	e.sync()
	code, before, stderr := e.run("--no-sync", "search", "Hello", "--limit", "1", "--fields", "id,sent_at")
	if code != 0 {
		t.Fatal(stderr)
	}
	legacy := items(t, decode(t, before))
	if len(legacy) != 1 {
		t.Fatal("synthetic legacy hit absent")
	}
	installCloudFixture(t, nil)
	raw := strings.ReplaceAll(cloudFixtureRaw, "searchableword", "Hello")
	// Equal the actual synthetic legacy clock rather than a guessed preview/sort value.
	var native map[string]any
	if err := json.Unmarshal([]byte(raw), &native); err != nil {
		t.Fatal(err)
	}
	native["file"].(map[string]any)["ModifiedRaw"] = legacy[0]["sent_at"]
	encoded, _ := json.Marshal(native)
	closer := &cloudFixtureCloser{}
	openBrowser = func(context.Context, browser.LaunchOptions) (transcripts.PageDriver, io.Closer, error) {
		return &cloudFixturePage{host: "fixture.sharepoint.com", raw: string(encoded)}, closer, nil
	}
	if code, _, stderr := e.run("content", "fetch", "page", "https://fixture.sharepoint.com/sites/sample/SitePages/Page.aspx"); code != 0 {
		t.Fatal(stderr)
	}
	code, out, stderr := e.run("--no-sync", "search", "Hello", "--limit", "1", "--fields", "id")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, out)
	if items(t, doc)[0]["id"] != legacy[0]["id"] {
		t.Fatal("equal-time legacy winner changed")
	}
	content := doc["sources"].(map[string]any)["content"].(map[string]any)
	if content["returned"] != float64(0) || doc["content_coverage"] == nil {
		t.Fatal("globally clipped cloud coverage lost")
	}
}
