package store

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func TestDocumentObservationsApplicationAndSurfaceFilters(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, binding := range []struct {
		surface     officedocuments.Surface
		application string
	}{{officedocuments.Recent, "Word"}, {officedocuments.Shared, "Excel"}, {officedocuments.Recent, "Excel"}} {
		locator := "/fixture/" + binding.application + "/" + string(binding.surface)
		a, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: DocumentKind(binding.surface), Platform: "synthetic",
			Locator: locator, At: base}, binding.surface, binding.application)
		if err != nil {
			t.Fatal(err)
		}
		doc := documentFixture(binding.application+" "+string(binding.surface), 0)
		doc.Surface = binding.surface
		if err := s.CommitDocumentObservations(ctx, a, KnowledgeOutcome{Status: "complete", ContentDigest: locator,
			Qualification: DocumentQualification, At: base}, officedocuments.Result{Documents: []officedocuments.Document{doc}},
			DocumentCaptureMetadata{Surface: binding.surface, Application: binding.application}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		filter DocumentFilter
		want   int
	}{
		{DocumentFilter{}, 3},
		{DocumentFilter{Application: "Excel"}, 2},
		{DocumentFilter{Surface: officedocuments.Recent}, 2},
		{DocumentFilter{Application: "Excel", Surface: officedocuments.Shared}, 1},
		{DocumentFilter{Application: "PowerPoint"}, 0},
	} {
		rows, _, err := s.DocumentObservations(ctx, tt.filter)
		if err != nil || len(rows) != tt.want {
			t.Fatalf("filter %+v = %d rows, %v; want %d", tt.filter, len(rows), err, tt.want)
		}
		for _, row := range rows {
			if (tt.filter.Application != "" && row.Application != tt.filter.Application) ||
				(tt.filter.Surface != "" && row.Document.Surface != tt.filter.Surface) {
				t.Fatalf("filter %+v returned %s/%s", tt.filter, row.Application, row.Document.Surface)
			}
		}
	}
}
