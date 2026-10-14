package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestLocalPreviewDoesNotMutateStoredProjection(t *testing.T) {
	gen := "9007199254740993"
	modified := time.Unix(2, 0).UTC()
	row := store.LocalTextRow{ID: 9007199254740993, SourceID: 2, CaptureID: 3,
		Snapshot: store.LocalFileSnapshot{Evidence: officecapture.FileEvidence{FileID: gen, NativeGenerationRaw: &gen, ModifiedAt: &modified}},
		Capture: store.LocalTextCapture{RequestedKind: "xlsx", Result: documenttext.Result{Kind: "xlsx", State: "text_observations", Partial: true,
			Losses: []documenttext.Loss{{Code: "formula_without_cache", Count: 7}}}}}
	for i := 0; i < 101; i++ {
		row.Capture.Result.Cells = append(row.Capture.Result.Cells, documenttext.Cell{Part: "xl/worksheets/sheet1.xml", Reference: "A1", Type: "n", Value: "long repeated value", FormulaCached: i%2 == 0})
	}
	before, _ := json.Marshal(row)
	item := shapeLocalText(row, 4)
	after, _ := json.Marshal(row)
	if string(before) != string(after) || len(item.Cells) != 100 || item.OccurrencesTotal != 101 || !item.PreviewTruncated || !item.TextTruncated ||
		item.ID != "local-text-observation:9007199254740993" || item.Snapshot.Evidence.FileID != gen || !item.Partial || !reflect.DeepEqual(item.Losses, row.Capture.Result.Losses) ||
		!item.Cells[0].FormulaCached || item.Cells[1].FormulaCached {
		t.Fatalf("preview aliases source: %#v", item)
	}
	item.Cells[0].Value = "mutated"
	item.Losses[0].Count = 99
	*item.Snapshot.Evidence.NativeGenerationRaw = "changed"
	if row.Capture.Result.Cells[0].Value != "long repeated value" || row.Capture.Result.Losses[0].Count != 7 || *row.Snapshot.Evidence.NativeGenerationRaw != gen {
		t.Fatal("preview owns no copy")
	}
}

func TestLocalFileURLStrictSelection(t *testing.T) {
	for _, url := range []string{
		"https://fixture.sharepoint.com/sites/a/file.docx", "file://server/file.docx", "file://user@localhost/a.docx",
		"file:///a.docx?q=1", "file:///a.docx#fragment", "file:///a/../b.docx", "file:///a/%2e%2e/b.docx",
		"file:relative.docx", "file:////server/file.docx", "file:///a.docx:stream", "file:///a%252fb.docx", "file:///a%00b.docx",
	} {
		if _, err := localFileURL(url, goruntime.GOOS); err == nil {
			t.Fatalf("unsafe locator accepted: %s", url)
		}
	}
	validURL := "file://localhost/synthetic/a%20b.docx"
	if goruntime.GOOS == "windows" {
		validURL = "file://localhost/C:/synthetic/a%20b.docx"
	}
	path, err := localFileURL(validURL, goruntime.GOOS)
	if err != nil || !strings.Contains(path, "a b.docx") {
		t.Fatalf("valid local URL: %s %v", path, err)
	}
}

func TestLocalCaptureSelectionRejectsBeforeArchive(t *testing.T) {
	e := cloudCLIEnv(t)
	for _, args := range [][]string{
		{"content", "capture"}, {"content", "capture", "--file="}, {"content", "capture", "--from="},
		{"content", "capture", "--file", "a.docx", "--from", "document-observation:1"},
		{"content", "capture", "--file", "relative.docx"}, {"content", "capture", "--file", "/synthetic/a.docx", "--kind="},
		{"content", "capture", "--file", "/synthetic/a.pdf"}, {"content", "capture", "--from", "document-observation:0"},
	} {
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatal("invalid capture admitted")
		}
	}
	if _, _, err := localTextInput(context.Background(), nil, contentCaptureCmd{File: filepath.Join(filepath.VolumeName(e.db)+string(filepath.Separator), "synthetic", "a.docx")}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPreviewAndCoverageContract(t *testing.T) {
	row := store.LocalTextRow{ID: 7, Retained: true, Capture: store.LocalTextCapture{RequestedKind: "docx", Result: documenttext.Result{State: "text_observations", Partial: true,
		Paragraphs: []documenttext.Paragraph{{Part: "word/document.xml", Index: 9007199254740993, Text: "old"}},
		Losses:     []documenttext.Loss{{Code: "embedded_content_unmapped", Count: 2}}}}}
	item := shapeLocalText(row, 0)
	b, _ := json.Marshal(item)
	if !strings.Contains(string(b), "9007199254740993") || !item.Retained || !item.Partial || item.Losses[0].Count != 2 || item.OccurrencesTotal != 1 {
		t.Fatal("own retained qualification lost")
	}
	for _, id := range []string{"local-text-observation:0", "local-text-observation:-1", "local-text-observation:1;sql", "local-text-observation:9223372036854775808", "cloud-observation:1"} {
		if _, err := localObservationID(id); err == nil {
			t.Fatal("invalid local ID")
		}
	}
	if id, err := localObservationID("local-text-observation:7"); err != nil || id != 7 {
		t.Fatal("positive local ID")
	}
}

func TestLocalSharedSearchTieOrder(t *testing.T) {
	hits := []searchHit{
		{source: searchContent, localText: true, cloudLabel: "a", cloudSourceID: 1, cloudObservationID: 10},
		{source: searchContent, cloudLabel: "z", cloudObservationID: 1},
		{source: searchMail}, {source: searchChats},
		{source: searchContent, localText: true, cloudLabel: "a", cloudSourceID: 1, cloudObservationID: 2},
	}
	sortSearchHits(hits)
	if hits[0].source != searchMail || hits[1].source != searchChats || hits[2].localText || hits[3].cloudObservationID != 2 || hits[4].cloudObservationID != 10 {
		t.Fatal("legacy/cloud ties reordered or lexical local ID")
	}
}
