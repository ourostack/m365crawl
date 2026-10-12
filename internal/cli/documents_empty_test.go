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
