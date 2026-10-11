package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

func setupDocumentObservation(t *testing.T, s *Store) KnowledgeAttempt {
	t.Helper()
	attempt := beginDocument(t, s, base)
	admitDocument(t, s, attempt, base, "complete", "one", []officedocuments.Document{documentFixture("Original", 0)})
	return attempt
}

func TestDocumentObservationsAtomicRegistrationAndAdmissionFaults(t *testing.T) {
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.BeginDocumentAttempt(ctx, KnowledgeInput{Kind: "office_shared", Platform: "synthetic",
			Locator: "/fixture/shared.json", At: base}, officedocuments.Shared, "supplied")
		return err
	})
	for _, status := range []string{"complete", "partial", "unchanged"} {
		t.Run(status, func(t *testing.T) {
			var attempt KnowledgeAttempt
			setup := func(t *testing.T, s *Store) {
				setupDocumentObservation(t, s)
				attempt = beginDocument(t, s, base.Add(time.Hour))
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				result := officedocuments.Result{Documents: []officedocuments.Document{documentFixture("Changed", 1)}}
				outcome := KnowledgeOutcome{Status: status, ContentDigest: "two", Qualification: DocumentQualification, At: base}
				if status == "partial" {
					result.Losses = []officedocuments.Loss{{Code: "office_document_unmapped", Count: 1}}
				}
				if status == "unchanged" {
					result = officedocuments.Result{}
					outcome.ContentDigest = "one"
				}
				return s.CommitDocumentObservations(ctx, attempt, outcome, result,
					DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied"})
			})
		})
	}
}

func TestDocumentObservationsReadFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) { setupDocumentObservation(t, s) }
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentObservations(ctx, DocumentFilter{Query: "Synthetic", SourceID: 1, Limit: 1})
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.DocumentObservation(ctx, 1)
		return err
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.DocumentLocatorInput(ctx, 1)
		return err
	})
}

func TestDocumentObservationsOldSchemaIsReadOnlyOptionalAbsence(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{8, 9, 10} {
		t.Run("schema-"+strconv.Itoa(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "data", "archive.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"document_fts", "document_capture_metadata", "document_capture_members", "document_observations", "document_locator_metadata"} {
				if _, err := s.db.Exec("drop table " + table); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec("update schema_migrations set version=?", version); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenReadOnly(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Error(err)
				}
			}()
			rows, truncated, err := s.DocumentObservations(ctx, DocumentFilter{Limit: 50})
			if err != nil || len(rows) != 0 || truncated {
				t.Fatal("old optional absence produced documents or a schema error")
			}
			if sources, err := s.DocumentSources(ctx); err != nil || len(sources) != 0 {
				t.Fatal("old optional absence discovered sources")
			}
			if snapshot, err := s.DocumentSnapshot(ctx, DocumentFilter{Limit: 50}); err != nil ||
				len(snapshot.Rows) != 0 || len(snapshot.Sources) != 0 || snapshot.Truncated {
				t.Fatal("old optional absence became populated or writable snapshot")
			}
			if _, err := s.DocumentObservation(ctx, 1); !errors.Is(err, ErrDocumentObservationNotFound) {
				t.Fatal("absent old-schema document detail did not report not found")
			}
			if needed, err := s.NeedsUpgrade(ctx); err != nil || needed {
				t.Fatal("optional absence changed legacy freshness")
			}
		})
	}
}

func TestDocumentObservationsInvalidInputsAndCorruptEvidence(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := setupDocumentObservation(t, s)
	for _, outcome := range []KnowledgeOutcome{
		{Status: "failed", At: base},
		{Status: "complete", At: base},
		{Status: "complete", ContentDigest: "one", Qualification: "other", At: base},
		{Status: "partial", ContentDigest: "one", Qualification: DocumentQualification, At: base},
	} {
		if err := s.CommitDocumentObservations(ctx, a, outcome, officedocuments.Result{},
			DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied"}); err == nil {
			t.Fatal("unqualified document outcome admitted")
		}
	}
	for _, filter := range []DocumentFilter{
		{Limit: -1}, {Limit: math.MaxInt}, {Query: "document-observation:0"},
		{Query: "document-observation:bad"}, {Query: `"`, Limit: 1}, {SourceID: -1}, {SourceID: 999},
	} {
		if _, _, err := s.DocumentObservations(ctx, filter); err == nil {
			t.Fatal("invalid query or limit accepted")
		}
	}
	if _, err := s.DocumentObservation(ctx, 0); !errors.Is(err, ErrDocumentObservationNotFound) {
		t.Fatal("invalid detail ID accepted")
	}
	if _, _, err := s.DocumentLocatorInput(ctx, 0); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatal("invalid source locator accepted")
	}
	for _, mutation := range []string{
		"update document_observations set document_json='bad'",
		"update document_capture_metadata set acquired_at='bad'",
		"update document_observations set application='unknown'",
	} {
		t.Run(mutation, func(t *testing.T) {
			local := newStore(t)
			setupDocumentObservation(t, local)
			if _, err := local.db.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if _, err := local.DocumentObservation(ctx, 1); err == nil {
				t.Fatal("corrupt native evidence silently returned")
			}
		})
	}
}
