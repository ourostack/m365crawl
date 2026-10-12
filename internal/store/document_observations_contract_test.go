package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func TestDocumentObservationsOrderingNativeDatesAndUnboundedList(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	b := documentFixture("Long shared prefix B", 0)
	a := documentFixture("Long shared prefix A", 1)
	unknown := documentFixture("A comes first only if an unknown date is invented", 2)
	a.Modified = officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: base}
	b.Modified = a.Modified
	attempt := beginDocument(t, s, base)
	admitDocument(t, s, attempt, base, "complete", "ordered", []officedocuments.Document{b, a, unknown})
	rows, more, err := s.DocumentObservations(ctx, DocumentFilter{Limit: 1})
	if err != nil || !more || len(rows) != 1 || rows[0].Document.Title != a.Title {
		t.Fatal("pre-limit ordering chose truncated/insertion title instead of full native title")
	}
	rows, more, err = s.DocumentObservations(ctx, DocumentFilter{Limit: 0})
	if err != nil || more || len(rows) != 3 || rows[2].Document.Title != unknown.Title {
		t.Fatal("unbounded list or unknown-date order inconsistent")
	}
	rows, _, err = s.DocumentObservations(ctx, DocumentFilter{ModifiedSince: base, ModifiedUntil: base.Add(time.Second), Limit: 10})
	if err != nil || len(rows) != 2 {
		t.Fatal("native date window included unknown or excluded a boundary")
	}
	rows, _, err = s.DocumentObservations(ctx, DocumentFilter{ModifiedUntil: base, Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("exclusive upper date boundary not honored")
	}
	rows, _, err = s.DocumentObservations(ctx, DocumentFilter{Query: "document-observation:2", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].ID != 2 {
		t.Fatal("stable native observation ID query did not resolve")
	}
}

func TestDocumentObservationsBoundedReferencesAreNotEventsOrEntities(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	documents := make([]officedocuments.Document, 101)
	for i := range documents {
		documents[i] = documentFixture(fmt.Sprintf("Representation %d", i), i)
	}
	a := beginDocument(t, s, base)
	admitDocument(t, s, a, base, "complete", "many", documents)
	detail, err := s.DocumentObservation(ctx, 1)
	if err != nil || len(detail.References) != 100 || detail.ReferencesTotal != 202 || !detail.ReferencesTruncated {
		t.Fatal("reference expansion is unbounded or counted unique URLs instead of occurrences")
	}
	for i, reference := range detail.References {
		if reference.ObservationID != int64(i/2+1) || reference.AssociationState != "unresolved" {
			t.Fatal("reference identity/order became a URL entity or current winner")
		}
	}
	rows, _, err := s.DocumentObservations(ctx, DocumentFilter{Limit: 0})
	if err != nil || len(rows) != 101 {
		t.Fatal("ordinal/title changes collapsed immutable representations")
	}
	for _, row := range rows {
		if !reflect.DeepEqual(row.Document.Sharing, documents[0].Sharing) {
			t.Fatal("same sharing claim changed across representation copies")
		}
	}
	recent := documentFixture("Same URL in another native namespace", 0)
	recent.Surface = officedocuments.Recent
	recent.SharePoint.TenantID = "another-stated-tenant"
	b, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "office_recent", Platform: "synthetic",
		Locator: "/other/recent.json", At: base}, officedocuments.Recent, "Word")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitDocumentObservations(ctx, b, KnowledgeOutcome{Status: "complete", ContentDigest: "other",
		Qualification: DocumentQualification, At: base}, officedocuments.Result{Documents: []officedocuments.Document{recent}},
		DocumentCaptureMetadata{Surface: officedocuments.Recent, Application: "Word"}); err != nil {
		t.Fatal(err)
	}
	rows, _, err = s.DocumentObservations(ctx, DocumentFilter{SourceID: 2, Limit: 0})
	if err != nil || len(rows) != 1 || rows[0].Document.SharePoint.TenantID != "another-stated-tenant" {
		t.Fatal("same URL or bare native tuple merged unrelated source evidence")
	}
	if locators, err := s.DocumentLocators(ctx, 0); err != nil || len(locators) != 2 {
		t.Fatal("finite kind union omitted or duplicated a registered locator")
	}
}

func TestDocumentObservationsRollingAbsenceAndUnchangedMembership(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	first := documentFixture("Retained old item", 0)
	a1 := beginDocument(t, s, base)
	admitDocument(t, s, a1, base, "complete", "one", []officedocuments.Document{first})
	a2 := beginDocument(t, s, base.Add(time.Hour))
	admitDocument(t, s, a2, base.Add(time.Hour), "complete", "empty", nil)
	a3 := beginDocument(t, s, base.Add(2*time.Hour))
	admitDocument(t, s, a3, base.Add(2*time.Hour), "unchanged", "empty", nil)
	rows, _, err := s.DocumentObservations(ctx, DocumentFilter{Limit: 10})
	if err != nil || len(rows) != 1 || !rows[0].Retained || rows[0].CaptureID != a1.ID {
		t.Fatal("rolling absence removed or freshened old observations")
	}
	var count int
	if err := s.db.QueryRow("select count(*) from document_capture_members where capture_id=?", a3.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("unchanged empty input invented current membership")
	}
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Coverage != "observations" || sources[0].AdmittedID != a3.ID {
		t.Fatal("retained rows erased current empty capture coverage")
	}
}

func TestDocumentObservationsAllKindsAndBindingFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, surface := range []officedocuments.Surface{officedocuments.Recent, officedocuments.Shared, officedocuments.Recommended, officedocuments.Dialog} {
		input := KnowledgeInput{Kind: DocumentKind(surface), Platform: "synthetic", Locator: "/fixture/" + string(surface), At: base}
		a, err := s.BeginDocumentAttempt(ctx, input, surface, "supplied")
		if err != nil {
			t.Fatal(err)
		}
		doc := documentFixture("Native "+string(surface), 0)
		doc.Surface = surface
		if err := s.CommitDocumentObservations(ctx, a, KnowledgeOutcome{Status: "complete", ContentDigest: string(surface),
			Qualification: DocumentQualification, At: base}, officedocuments.Result{Documents: []officedocuments.Document{doc}},
			DocumentCaptureMetadata{Surface: surface, Application: "supplied"}); err != nil {
			t.Fatal(err)
		}
	}
	if locators, err := s.DocumentLocators(ctx, 0); err != nil || len(locators) != 4 {
		t.Fatal("finite four-kind source union wrong")
	}
	if locators, err := s.DocumentLocators(ctx, 3); err != nil || len(locators) != 1 || locators[0].Kind != "office_recommended" {
		t.Fatal("source selection refreshed peers or wrong kind")
	}
	if DocumentKind("unknown") != "" {
		t.Fatal("unknown kind accepted")
	}
	if _, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "notes", Platform: "synthetic", Locator: "/fixture", At: base},
		officedocuments.Shared, "supplied"); err == nil {
		t.Fatal("wrong source kind registered")
	}
	if _, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "office_shared", Platform: "synthetic", Locator: "/bad", At: base},
		officedocuments.Shared, "guessed-app"); err == nil {
		t.Fatal("unknown application registered")
	}
	for _, mutation := range []string{
		"update document_locator_metadata set surface='unknown' where locator_id=1",
		"update knowledge_locators set kind='notes' where id=1",
	} {
		t.Run(mutation, func(t *testing.T) {
			local := newStore(t)
			a := setupDocumentObservation(t, local)
			if _, err := local.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, _, err := local.DocumentLocatorInput(ctx, a.LocatorID); err == nil {
				t.Fatal("corrupt stored binding inferred a source")
			}
		})
	}
	if _, err := s.DocumentLocators(ctx, 999); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("missing selected source returned empty success")
	}
}
