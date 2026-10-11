package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func setupPeopleObservation(t *testing.T, s *Store) KnowledgeAttempt {
	t.Helper()
	ctx := context.Background()
	a, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
	if err != nil {
		t.Fatal(err)
	}
	result := outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "one@example.invalid", TeamsID: "8:orgid:fixture"}}}
	err = s.CommitPeopleObservations(ctx, a, KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: PeopleQualification, At: base},
		result, PeopleCaptureMetadata{AcquiredAt: &base, SourceObservedAt: &base, Link: PeopleCaptureLink{ProfileAccount: "outlook/Fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestPeopleObservationsAtomicFaults(t *testing.T) {
	for _, status := range []string{"complete", "partial", "unchanged"} {
		t.Run(status, func(t *testing.T) {
			var attempt KnowledgeAttempt
			setup := func(t *testing.T, s *Store) {
				setupPeopleObservation(t, s)
				var err error
				attempt, err = s.BeginKnowledgeAttempt(context.Background(), KnowledgeInput{
					Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
				if err != nil {
					t.Fatal(err)
				}
			}
			sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
				result := outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, VersionRaw: 2,
					Email: "one@example.invalid", TeamsID: "other", Resynced: true}},
					AmbiguousKeys: []outlookpeople.ObjectKey{{Class: 0xd2, Key: 1}}}
				outcome := KnowledgeOutcome{Status: status, ContentDigest: "two", Qualification: PeopleQualification, At: base}
				if status == "partial" {
					result.Losses = []outlookpeople.Loss{{Code: "outlook_blocks_damaged", Count: 1}}
				}
				if status == "unchanged" {
					outcome.ContentDigest = "one"
					result = outlookpeople.Result{}
				}
				return s.CommitPeopleObservations(ctx, attempt, outcome, result, PeopleCaptureMetadata{})
			})
		})
	}
}

func TestPeopleObservationsReadFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) { setupPeopleObservation(t, s) }
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, _, err := s.PeopleObservations(ctx, "one", 1, 1)
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.PeopleObservation(ctx, 1)
		return err
	})
}

func TestPeopleObservationsInvalidAndCorruptEvidence(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := setupPeopleObservation(t, s)
	for _, outcome := range []KnowledgeOutcome{
		{Status: "failed", At: base},
		{Status: "complete", At: base},
		{Status: "complete", ContentDigest: "one", Qualification: "other", At: base},
		{Status: "partial", ContentDigest: "one", Qualification: PeopleQualification, At: base},
	} {
		if err := s.CommitPeopleObservations(ctx, a, outcome, outlookpeople.Result{}, PeopleCaptureMetadata{}); err == nil {
			t.Fatal("invalid admission accepted")
		}
	}
	if _, _, err := s.PeopleObservations(ctx, "", 0, 0); err == nil {
		t.Fatal("zero limit accepted")
	}
	if _, _, err := s.PeopleObservations(ctx, "person-observation:bad", 0, 1); err == nil {
		t.Fatal("bad ID accepted")
	}
	if _, _, err := s.PeopleObservations(ctx, "person-observation:1", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.KnowledgeKindLocators(ctx, "outlook_people", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("update people_observations set version_raw='bad'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeopleObservation(ctx, 1); err == nil {
		t.Fatal("corrupt version hidden")
	}
	if _, err := s.db.Exec("update people_observations set version_raw='1',refreshed_at='bad'"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PeopleObservations(ctx, "", 0, 1); err == nil {
		t.Fatal("corrupt refresh hidden")
	}
	if _, err := s.db.Exec("update people_observations set refreshed_at=null"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("update people_capture_links set linked_at='bad'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeopleObservation(ctx, 1); err == nil {
		t.Fatal("corrupt link clock hidden")
	}
}

func TestPeopleObservationsOldSchemaReadOnly(t *testing.T) {
	s := newStore(t)
	for _, table := range []string{"people_capture_links", "people_capture_metadata", "people_group_ambiguities", "people_capture_members", "people_observations"} {
		if _, err := s.db.Exec("drop table " + table); err != nil {
			t.Fatal(err)
		}
	}
	if rows, _, err := s.PeopleObservations(context.Background(), "", 0, 1); err != nil || len(rows) != 0 {
		t.Fatalf("optional absence: %v", err)
	}
	if _, err := s.PeopleObservation(context.Background(), 1); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("optional absent detail: %v", err)
	}
}

func TestPeopleObservationsUnchangedQualificationFailures(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"missing", "digest", "losses", "source"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			if kind != "missing" {
				setupPeopleObservation(t, s)
			}
			a, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "losses":
				if _, err := s.db.Exec("update knowledge_attempts set losses_json='bad' where id=1"); err != nil {
					t.Fatal(err)
				}
			case "source":
				if _, err := s.db.Exec("update knowledge_sources set boundary='old'"); err != nil {
					t.Fatal(err)
				}
			}
			digest := "one"
			if kind == "digest" {
				digest = "different"
			}
			err = s.CommitPeopleObservations(ctx, a, KnowledgeOutcome{Status: "unchanged", ContentDigest: digest,
				Qualification: PeopleQualification, At: base}, outlookpeople.Result{}, PeopleCaptureMetadata{})
			if err == nil {
				t.Fatal("unqualified reuse admitted")
			}
		})
	}
}

func TestPeopleObservationsInvalidNativeTimeAndTruncation(t *testing.T) {
	s := newStore(t)
	a, err := s.BeginKnowledgeAttempt(context.Background(), KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
	if err != nil {
		t.Fatal(err)
	}
	people := []outlookpeople.Person{{Class: 0xd2, Key: 1, RefreshedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	err = s.CommitPeopleObservations(context.Background(), a, KnowledgeOutcome{Status: "complete", ContentDigest: "one",
		Qualification: PeopleQualification, At: base}, outlookpeople.Result{People: people}, PeopleCaptureMetadata{})
	if err == nil {
		t.Fatal("invalid native time serialized")
	}
	people = []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "one"}, {Class: 0xd2, Key: 2, Email: "two"}}
	if err := s.CommitPeopleObservations(context.Background(), a, KnowledgeOutcome{Status: "complete", ContentDigest: "one",
		Qualification: PeopleQualification, At: base}, outlookpeople.Result{People: people}, PeopleCaptureMetadata{}); err != nil {
		t.Fatal(err)
	}
	if rows, truncated, err := s.PeopleObservations(context.Background(), "", 0, 1); err != nil || !truncated || len(rows) != 1 {
		t.Fatalf("merged limit: %d %t %v", len(rows), truncated, err)
	}
}
