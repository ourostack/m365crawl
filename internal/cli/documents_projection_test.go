package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestDocumentsProjectionBoundsDoNotRewriteNativeIDsOrStoredModel(t *testing.T) {
	document := officedocuments.Document{Surface: officedocuments.Recent, Title: "Long title",
		URL: "https://example.invalid/long-native-url", FriendlyPath: "/synthetic/long/path",
		ResourceID: "opaque-long-native-id", Creator: officedocuments.Person{DisplayName: "Long creator", UPN: "creator@example.invalid"},
		Modifier: officedocuments.Person{DisplayName: "Long modifier"}, Sharing: &officedocuments.Sharing{DisplayName: "Long sharer"},
		Activity: &officedocuments.Activity{Users: []officedocuments.Person{{DisplayName: "Long activity user"}}}}
	row := store.DocumentObservationRow{ID: 10, SourceID: 1, CaptureID: 1, Application: "Word", Document: document}
	item := documentItemOf(row, 4, "")
	if !item.TextTruncated || item.Title != "Long…" || item.Native.FriendlyPath != "/syn…" ||
		item.URL != document.URL || item.Native.ResourceID != document.ResourceID ||
		item.Native.Creator.UPN != document.Creator.UPN || item.ModifiedAt != nil {
		t.Fatal("projection lost native IDs, invented time or omitted display bounds")
	}
	if row.Document.Title != "Long title" || row.Document.Sharing.DisplayName != "Long sharer" ||
		row.Document.Activity.Users[0].DisplayName != "Long activity user" {
		t.Fatal("output shaping mutated immutable native representation")
	}
	item = documentItemOf(row, 0, "")
	if item.TextTruncated || item.Title != document.Title {
		t.Fatal("max-text0 changed native output")
	}
}

func TestDocumentsMixedSearchIncludesMetadataWithoutClaimingChatTotal(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	seedNotesCLI(t, e)
	code, stdout, stderr := e.run("--max-age", "0", "search", "Quarterly", "--source", "all", "--limit", "10")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, stdout)
	found := false
	for _, hit := range items(t, doc) {
		if hit["source"] == "documents" && hit["id"] == id {
			found = true
		}
	}
	if !found || doc["total"] != nil || doc["source_coverage"] == nil {
		t.Fatal("mixed metadata source absent or chat total claimed for mixed results")
	}
	for _, flag := range [][]string{{"--account", "tenant/user"}, {"--from", "someone"}, {"--include-system"}, {"--html"}} {
		args := append([]string{"--max-age", "0", "search", "Quarterly", "--source", "all"}, flag...)
		code, stdout, stderr := e.run(args...)
		if code != 0 {
			t.Fatal(stderr)
		}
		if !strings.Contains(stdout, "document metadata was not searched") {
			t.Fatal("unsupported mixed filter silently excluded document claims")
		}
	}
}

func TestDocumentsCLISelectedShowRefreshesOnlyItsSource(t *testing.T) {
	e := newEnv(t)
	id := seedDocumentsCLI(t, e)
	run := runDocumentsSync
	t.Cleanup(func() { runDocumentsSync = run })
	calls := 0
	runDocumentsSync = func(ctx context.Context, options syncer.DocumentOptions) (syncer.DocumentReport, error) {
		calls++
		if options.SourceID != 1 || options.Discover || options.Root != "" {
			t.Fatal("detail refreshed peers/default roots")
		}
		return run(ctx, options)
	}
	code, _, stderr := e.run("--max-age", "0", "docs", "show", id)
	if code != 0 || calls != 1 {
		t.Fatalf("selected show route failed: %s", stderr)
	}
	code, _, _ = e.run("--max-age", "0", "docs", "show", "document-observation:999")
	if code != 2 || calls != 1 {
		t.Fatal("missing detail ID refreshed a source before lookup")
	}
}
