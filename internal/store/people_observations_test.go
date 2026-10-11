package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestPeopleObservationsRetainCopiesAndOriginalClocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 11, 1, 0, 0, 100, time.UTC)
	person := outlookpeople.Person{Class: 0xd2, Key: 7, VersionRaw: math.MaxUint64,
		Email: "fixture@example.invalid", TeamsID: "8:orgid:11111111-1111-1111-1111-111111111111",
		DisplayName: "Fixture", RefreshedAt: t1}
	capture := func(at time.Time, digest, status string, people []outlookpeople.Person) KnowledgeAttempt {
		t.Helper()
		a, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "outlook_people", Platform: "darwin", Locator: "/synthetic/HxStore.hxd", At: at})
		if err != nil {
			t.Fatal(err)
		}
		result := outlookpeople.Result{People: people}
		if status == "partial" {
			result.Losses = []outlookpeople.Loss{{Code: "outlook_blocks_damaged", Count: 1}}
		}
		err = s.CommitPeopleObservations(ctx, a, KnowledgeOutcome{Status: status, ContentDigest: digest, Qualification: PeopleQualification,
			SourceObservedAt: at, At: at.Add(time.Second)}, result,
			PeopleCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at, Reused: status == "unchanged"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a1 := capture(t1, "one", "complete", []outlookpeople.Person{person, person})
	rows, _, err := s.PeopleObservations(ctx, "", 0, 50)
	if err != nil || len(rows) != 1 {
		t.Fatalf("initial rows: %d %v", len(rows), err)
	}
	id := rows[0].ID
	changed := person
	changed.VersionRaw = 2
	changed.TeamsID = "8:orgid:22222222-2222-2222-2222-222222222222"
	capture(t1.Add(time.Hour), "two", "partial", []outlookpeople.Person{changed})
	a3 := capture(t1.Add(2*time.Hour), "two", "unchanged", nil)
	detail, err := s.PeopleObservation(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.CaptureID != a1.ID || detail.CapturedAt == nil || !detail.CapturedAt.Equal(t1) ||
		detail.SourceObservedAt == nil || !detail.SourceObservedAt.Equal(t1) ||
		detail.Person.VersionRaw != math.MaxUint64 || !detail.Person.RefreshedAt.Equal(t1) {
		t.Fatalf("original clock/native evidence changed: %+v", detail.PeopleObservationRow)
	}
	if !detail.Ambiguous || !detail.Conflicting || len(detail.Copies) != 2 || detail.IdentityState != "unresolved" {
		t.Fatalf("contradictions not retained: %+v", detail)
	}
	sources, err := s.KnowledgeSources(ctx, "outlook_people")
	if err != nil || len(sources) != 1 || sources[0].AdmittedID != a3.ID || sources[0].Coverage != "partial" {
		t.Fatalf("source admission: %+v %v", sources, err)
	}
	var physical int
	if err := s.db.QueryRow("select physical_observations from people_capture_metadata where capture_id=?", a1.ID).Scan(&physical); err != nil || physical != 2 {
		t.Fatalf("physical multiplicity: %d %v", physical, err)
	}
}

func TestPeopleObservationsStaleAndWrongKind(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	input := KnowledgeInput{Kind: "outlook_people", Platform: "darwin", Locator: "/fixture/people", At: at}
	old, err := s.BeginKnowledgeAttempt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	current, err := s.BeginKnowledgeAttempt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	outcome := KnowledgeOutcome{Status: "complete", ContentDigest: "fixture", Qualification: PeopleQualification, At: at}
	result := outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "one@example.invalid"}}}
	if err := s.CommitPeopleObservations(ctx, current, outcome, result, PeopleCaptureMetadata{}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPeopleObservations(ctx, old, outcome, result, PeopleCaptureMetadata{}); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("stale admission: %v", err)
	}
	var terminal string
	if err := s.db.QueryRow("select reason from knowledge_attempts where id=?", old.ID).Scan(&terminal); err != nil || terminal != "stale_attempt" {
		t.Fatalf("terminal history: %q %v", terminal, err)
	}
	input.Kind = "notes"
	note, err := s.BeginKnowledgeAttempt(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitPeopleObservations(ctx, note, outcome, result, PeopleCaptureMetadata{}); err == nil {
		t.Fatal("wrong source kind admitted")
	}
	if _, err := s.PeopleObservation(ctx, math.MaxInt64); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown detail: %v", err)
	}
}

func TestPeopleObservationsSourceFrontierRejectsOlderAdmission(t *testing.T) {
	s := newStore(t)
	first := setupPeopleObservation(t, s)
	ctx := context.Background()
	next, err := s.BeginKnowledgeAttempt(ctx, KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/people", At: base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("update knowledge_sources set admitted_attempt=?", next.ID+1); err != nil {
		t.Fatal(err)
	}
	err = s.CommitPeopleObservations(ctx, next, KnowledgeOutcome{Status: "complete", ContentDigest: "new",
		Qualification: PeopleQualification, At: base}, outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 2, Email: "forbidden"}}},
		PeopleCaptureMetadata{})
	if !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("source frontier: %v", err)
	}
	if n := rowCount(t, s, "select count(*) from people_observations where capture_id<>?", first.ID); n != 0 {
		t.Fatal("stale source added claims")
	}
	if n := rowCount(t, s, "select count(*) from knowledge_attempts where id=? and reason='stale_attempt'", next.ID); n != 1 {
		t.Fatal("stale terminal history rolled back")
	}
}
