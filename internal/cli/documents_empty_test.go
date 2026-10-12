package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsCoverageExplainsResponseThatListedNoDocuments(t *testing.T) {
	sources := []store.KnowledgeSource{
		{ID: 1, Kind: "office_recent", Status: "complete", Coverage: "observations", Reason: officedocuments.NoDocumentsListed},
		{ID: 2, Kind: "office_shared", Status: "complete", Coverage: "observations"},
	}
	coverage := documentsCoverage(sources)
	if len(coverage.Notices) != 1 || !strings.Contains(coverage.Notices[0], officedocuments.NoDocumentsListed) ||
		!strings.Contains(coverage.Notices[0], "not whether the account has documents") {
		t.Fatalf("empty response notice = %#v", coverage.Notices)
	}
	if len(documentsCoverage(sources[1:]).Notices) != 0 {
		t.Fatal("notice emitted without an empty response source")
	}
	encoded, err := json.Marshal(coverage)
	if err != nil || !bytes.Contains(encoded, []byte(`"notices":[`)) {
		t.Fatalf("notice missing from JSON coverage: %s, %v", encoded, err)
	}
	var out bytes.Buffer
	rt := newRuntime(context.Background(), &Globals{}, &out, &bytes.Buffer{})
	rt.renderNoteCoverage(coverage)
	if !strings.Contains(out.String(), "notice: "+coverage.Notices[0]) {
		t.Fatalf("notice missing from text coverage: %s", out.String())
	}
	merged := mergedOptionalCoverage(notesCoverage(nil), coverage)
	if len(merged.Notices) != 1 || merged.Notices[0] != coverage.Notices[0] {
		t.Fatalf("merged coverage dropped document notice: %#v", merged.Notices)
	}
}

// A dialog list or supplied file is not an Office service response, so its notice claims only what the file held.
func TestDocumentsCoverageExplainsFileThatListedNoDocumentsNeutrally(t *testing.T) {
	coverage := documentsCoverage([]store.KnowledgeSource{
		{ID: 1, Kind: "office_dialog", Status: "complete", Coverage: "observations", Reason: officedocuments.FileListedNoDocuments},
		{ID: 2, Kind: "office_recent", Status: "complete", Coverage: "observations", Reason: officedocuments.NoDocumentsListed},
	})
	if len(coverage.Notices) != 2 || !strings.HasPrefix(coverage.Notices[1], officedocuments.FileListedNoDocuments+":") ||
		strings.Contains(coverage.Notices[1], "Office response") || !strings.Contains(coverage.Notices[1], "saved Office JSON file") {
		t.Fatalf("file notice = %#v", coverage.Notices)
	}
}

// A never-admitted source has a null source_id, so --source-id cannot target it; every remedy names plain docs sync.
func TestDocumentRemediesNamePlainDocsSync(t *testing.T) {
	for reason, fix := range documentFailureFix {
		if strings.Contains(fix, "docs sync") && !strings.Contains(fix, "m365crawl docs sync") {
			t.Errorf("%s remedy names docs sync without the command: %s", reason, fix)
		}
		if strings.Contains(fix, "--source-id") {
			t.Errorf("%s remedy names --source-id, which a never-admitted source cannot use: %s", reason, fix)
		}
	}
	never := documentsCoverage([]store.KnowledgeSource{{LocatorID: 3, Kind: "office_recent", Status: "failed", Reason: "office_json_cancelled"}})
	if !strings.Contains(never.Sources[0].Fix, "m365crawl docs sync") {
		t.Fatalf("never-admitted fix = %q", never.Sources[0].Fix)
	}
}
