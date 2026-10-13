package cli

import (
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestDocumentsDisplayBoundsIncludeNativeTitlesAndActivityText(t *testing.T) {
	model := officedocuments.Document{
		SiteTitle: "Long site title", TeamsChannelTitle: "Long channel title",
		Activity: &officedocuments.Activity{MessageFormat: "Long activity description"},
	}
	item := documentItemOf(store.DocumentObservationRow{ID: 1, Document: model}, 4, "")
	if !item.TextTruncated || item.Native.SiteTitle != "Long…" || item.Native.TeamsChannelTitle != "Long…" ||
		item.Native.Activity.MessageFormat != "Long…" {
		t.Fatal("native descriptive fields escaped max-text display bounds")
	}
	if model.SiteTitle != "Long site title" || model.Activity.MessageFormat != "Long activity description" {
		t.Fatal("display projection mutated retained original native claims")
	}
}
