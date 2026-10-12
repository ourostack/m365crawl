package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func TestDocumentObservationsUnchangedCannotDiscardNewClaims(t *testing.T) {
	s := newStore(t)
	setupDocumentObservation(t, s)
	attempt := beginDocument(t, s, base.Add(time.Hour))
	err := s.CommitDocumentObservations(context.Background(), attempt, KnowledgeOutcome{
		Status: "unchanged", ContentDigest: "one", Qualification: DocumentQualification, At: base},
		officedocuments.Result{Documents: []officedocuments.Document{documentFixture("New contradiction", 1)}},
		DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied"})
	if err == nil {
		t.Fatal("unchanged admission silently discarded supplied new claims")
	}
}

func TestDocumentObservationsAdmissionEdgeFailuresAreAtomic(t *testing.T) {
	ctx := context.Background()
	for _, fault := range []string{"native-surface", "native-empty", "loss", "loss-code", "binding", "marshal", "no-prior", "digest", "qualification", "loss-json", "source-frontier", "source-mismatch"} {
		t.Run(fault, func(t *testing.T) {
			s := newStore(t)
			if fault != "no-prior" {
				setupDocumentObservation(t, s)
			}
			attempt := beginDocument(t, s, base.Add(time.Hour))
			doc := documentFixture("Changed", 1)
			result := officedocuments.Result{Documents: []officedocuments.Document{doc}}
			capture := DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied"}
			outcome := KnowledgeOutcome{Status: "complete", ContentDigest: "two", Qualification: DocumentQualification, At: base}
			switch fault {
			case "native-surface":
				result.Documents[0].Surface = officedocuments.Recent
			case "native-empty":
				result.Documents[0].URL = ""
			case "loss", "loss-code":
				outcome.Status = "partial"
				result.Losses = []officedocuments.Loss{{Code: "office_document_unmapped", Count: -1}}
				if fault == "loss-code" {
					result.Losses[0] = officedocuments.Loss{Count: 1}
				}
			case "binding":
				capture.Application = "Word"
			case "marshal":
				result.Documents[0].Modified.Value = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "no-prior", "digest", "qualification", "loss-json", "source-mismatch":
				outcome.Status = "unchanged"
				outcome.ContentDigest = "one"
				result = officedocuments.Result{}
				switch fault {
				case "digest":
					outcome.ContentDigest = "not-the-admitted-bytes"
				case "qualification":
					s.qExec(t, "update knowledge_sources set qualification='other'")
				case "loss-json":
					s.qExec(t, "update knowledge_attempts set losses_json='bad' where id=1")
				case "source-mismatch":
					s.qExec(t, "update knowledge_sources set boundary='other'")
				}
			case "source-frontier":
				s.qExec(t, "update knowledge_sources set admitted_attempt=?", attempt.ID+1)
			}
			err := s.CommitDocumentObservations(ctx, attempt, outcome, result, capture)
			if fault == "source-frontier" {
				if !errors.Is(err, ErrStaleKnowledgeAttempt) {
					t.Fatal("reverse source frontier not refused")
				}
			} else if err == nil {
				t.Fatal("invalid admission succeeded")
			}
		})
	}
}

func TestDocumentObservationsRetainedSourceSelectionAndMissingCurrent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := setupDocumentObservation(t, s)
	s.qExec(t, `insert into knowledge_sources(locator_id,boundary,qualification) values(?,'future','future')`, a.LocatorID)
	sources, err := s.DocumentSources(ctx)
	if err != nil || len(sources) != 2 || sources[0].ID != 1 || sources[1].ID != 2 {
		t.Fatal("same-locator source incarnations not stably ordered")
	}
	if _, err := s.DocumentLocators(ctx, 2); err != nil {
		t.Fatal("retained source did not resolve its current locator")
	}
	s.qExec(t, "update knowledge_sources set id=99 where id=1")
	if _, err := s.DocumentLocators(ctx, 2); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("retained source without a current incarnation was guessed")
	}
	if _, err := s.DocumentObservation(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("missing coherent source detail not refused")
	}
	local := newStore(t)
	setupDocumentObservation(t, local)
	local.qExec(t, "update knowledge_locators set kind='notes'")
	if _, err := local.DocumentObservation(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("wrong-kind source detail was attributed as document evidence")
	}
}
