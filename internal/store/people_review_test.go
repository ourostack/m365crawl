package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestPeopleReviewedMissingIdentityHalvesAreNotConflicts(t *testing.T) {
	for _, email := range []string{"", " ", "fixture@example.invalid"} {
		s := newStore(t)
		a, err := s.BeginKnowledgeAttempt(context.Background(), KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture", At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		people := []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: email}, {Class: 0x32a, Key: 2, Email: email, TeamsID: "other"}}
		if err := s.CommitPeopleObservations(context.Background(), a, KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: PeopleQualification, At: time.Now()},
			outlookpeople.Result{People: people}, PeopleCaptureMetadata{}); err != nil {
			t.Fatal(err)
		}
		detail, err := s.PeopleObservation(context.Background(), 1)
		if err != nil || detail.Conflicting || len(detail.Conflicts) != 0 {
			t.Fatalf("missing identity fabricated conflict: %+v %v", detail, err)
		}
	}
}
