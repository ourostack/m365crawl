package store

import (
	"context"
	"testing"
)

func TestAttendeeSummaryCarriesExactRequest(t *testing.T) {
	s := newStore(t)
	a := nativeAttempt(t, s)
	if err := s.RecordAttendeeFailure(context.Background(), a, KnowledgeOutcome{Status: "failed", At: base}); err != nil {
		t.Fatal(err)
	}
	got, err := s.AttendeeSources(context.Background(), 0)
	if err != nil || len(got.Coverage.Sources) != 1 {
		t.Fatal(err)
	}
	request := got.Coverage.Sources[0].Request
	if request.Root != attendeeFixtureRoot || !request.Discover || request.SourceID != 0 {
		t.Fatal("summary lost exact in-transaction retry request", request)
	}
}
