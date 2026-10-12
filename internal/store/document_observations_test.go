package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func documentFixture(title string, ordinal int) officedocuments.Document {
	zero := int64(0)
	no := false
	return officedocuments.Document{Surface: officedocuments.Shared, Ordinal: ordinal,
		Title: title, URL: "https://example.invalid/shared-document", WebURL: "https://example.invalid/shared-document",
		ResourceID: "opaque-resource", DriveID: "native-drive", ItemID: "native-item", Size: &zero, Pinned: &no,
		SharePoint: officedocuments.SharePoint{TenantID: "stated-tenant", SiteID: "native-site"},
		Modified:   officedocuments.Timestamp{Raw: "unknown source time"},
		Sharing: &officedocuments.Sharing{DisplayName: "Synthetic sharer", Email: "sharer@example.invalid",
			At: officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: base}, Type: &zero}}
}

func beginDocument(t *testing.T, s *Store, at time.Time) KnowledgeAttempt {
	t.Helper()
	attempt, err := s.BeginDocumentAttempt(context.Background(), KnowledgeInput{
		Kind: "office_shared", Platform: "synthetic", Locator: "/fixture/shared.json", At: at},
		officedocuments.Shared, "supplied")
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func admitDocument(t *testing.T, s *Store, attempt KnowledgeAttempt, at time.Time, status, digest string, documents []officedocuments.Document) {
	t.Helper()
	result := officedocuments.Result{Documents: documents}
	if status == "partial" {
		result.Losses = []officedocuments.Loss{{Code: "office_document_unmapped", Count: 1}}
	}
	if err := s.CommitDocumentObservations(context.Background(), attempt, KnowledgeOutcome{
		Status: status, ContentDigest: digest, Qualification: DocumentQualification, At: at.Add(time.Second),
		SourceObservedAt: at}, result, DocumentCaptureMetadata{
		Surface: officedocuments.Shared, Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentObservationsImmutableRepresentationsAndClocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := documentFixture("Original title", 0)
	a1 := beginDocument(t, s, base)
	admitDocument(t, s, a1, base, "complete", "bytes-one", []officedocuments.Document{first, first})
	rows, truncated, err := s.DocumentObservations(ctx, DocumentFilter{Limit: 10})
	if err != nil || truncated || len(rows) != 1 || !reflect.DeepEqual(rows[0].Document, first) {
		t.Fatalf("native representation changed: %v", err)
	}
	id := rows[0].ID
	changed := documentFixture("Changed title", 1)
	a2 := beginDocument(t, s, base.Add(time.Hour))
	admitDocument(t, s, a2, base.Add(time.Hour), "partial", "bytes-two", []officedocuments.Document{changed})
	a3 := beginDocument(t, s, base.Add(2*time.Hour))
	admitDocument(t, s, a3, base.Add(2*time.Hour), "unchanged", "bytes-two", nil)
	detail, err := s.DocumentObservation(ctx, id)
	if err != nil || detail.Observation.CaptureID != a1.ID || !detail.Observation.Retained ||
		detail.Observation.CapturedAt == nil || !detail.Observation.CapturedAt.Equal(base) ||
		detail.Observation.SourceObservedAt == nil || !detail.Observation.SourceObservedAt.Equal(base) ||
		!reflect.DeepEqual(detail.Observation.Document, first) {
		t.Fatalf("original capture evidence was overwritten: %v", err)
	}
	if detail.Source.AdmittedID != a3.ID || detail.Source.Coverage != "partial" ||
		detail.ReferencesTotal != 4 || detail.ReferencesTruncated || len(detail.References) != 4 {
		t.Fatal("current source or attributed references inconsistent")
	}
	for _, reference := range detail.References {
		if reference.AssociationState != "unresolved" || reference.URL != first.URL {
			t.Fatal("reference became an asserted entity association")
		}
	}
	rows, _, err = s.DocumentObservations(ctx, DocumentFilter{Query: "Synthetic", Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatalf("source sharing metadata not searchable: %v", err)
	}
	var physical int
	if err := s.db.QueryRow("select physical_observations from document_capture_metadata where capture_id=?", a1.ID).Scan(&physical); err != nil || physical != 2 {
		t.Fatal("physical duplicates confused with unique rows")
	}
}

func TestDocumentObservationsWrongBindingAndStaleTerminal(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	old := beginDocument(t, s, base)
	current := beginDocument(t, s, base.Add(time.Hour))
	admitDocument(t, s, current, base.Add(time.Hour), "complete", "current", []officedocuments.Document{documentFixture("Current", 0)})
	result := officedocuments.Result{Documents: []officedocuments.Document{documentFixture("Old", 0)}}
	outcome := KnowledgeOutcome{Status: "complete", ContentDigest: "old", Qualification: DocumentQualification, At: base}
	capture := DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied"}
	if err := s.CommitDocumentObservations(ctx, old, outcome, result, capture); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("old attempt was admitted: %v", err)
	}
	var reason string
	if err := s.db.QueryRow("select reason from knowledge_attempts where id=?", old.ID).Scan(&reason); err != nil || reason != "stale_attempt" {
		t.Fatal("stale history not terminalized")
	}
	input := KnowledgeInput{Kind: "office_shared", Platform: "synthetic", Locator: "/fixture/shared.json", At: base}
	if _, err := s.BeginDocumentAttempt(ctx, input, officedocuments.Shared, "Word"); err == nil {
		t.Fatal("immutable application attribution changed")
	}
	surface, app, err := s.DocumentLocatorInput(ctx, current.LocatorID)
	if err != nil || surface != officedocuments.Shared || app != "supplied" {
		t.Fatal("rootless descriptor not persisted")
	}
	capture.Surface = officedocuments.Recent
	if err := s.CommitDocumentObservations(ctx, current, outcome, result, capture); err == nil {
		t.Fatal("wrong surface binding admitted")
	}
}
