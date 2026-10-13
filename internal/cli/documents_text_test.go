package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDocumentsTextCoverageNamesParticipatingSources(t *testing.T) {
	for _, sample := range []struct{ scope, want string }{
		{"office_document_metadata", "documents coverage:"},
		{"registered_optional_sources", "notes and documents coverage:"},
	} {
		var out bytes.Buffer
		rt := newRuntime(context.Background(), &Globals{}, &out, &bytes.Buffer{})
		rt.renderNoteCoverage(&noteCoverage{Scope: sample.scope, State: "unknown",
			RefreshError: &syncError{Code: "source_failed", Message: "source refused"}})
		if !strings.Contains(out.String(), sample.want) ||
			!strings.Contains(out.String(), strings.TrimSuffix(sample.want, " coverage:")+" refresh error:") {
			t.Fatalf("source coverage mislabeled: %s", out.String())
		}
	}
}

func TestDocumentsTextProjectionDoesNotInventNativeDetail(t *testing.T) {
	var out bytes.Buffer
	rt := newRuntime(context.Background(), &Globals{}, &out, &bytes.Buffer{})
	if err := rt.renderDocumentDetails(newList([]any{map[string]any{"id": "document-observation:1"}}, false)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("shaped detail fabricated unselected native claims")
	}
}
