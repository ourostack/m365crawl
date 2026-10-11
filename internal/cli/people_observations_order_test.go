package cli

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleObservationsEqualNamesOrderBySource(t *testing.T) {
	e := newEnv(t)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, path := range []string{"/fixture/one", "/fixture/two"} {
		a, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{
			Kind: "outlook_people", Platform: "synthetic", Locator: path, At: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CommitPeopleObservations(context.Background(), a, store.KnowledgeOutcome{
			Status: "complete", ContentDigest: path, Qualification: store.PeopleQualification, At: time.Now()},
			outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, DisplayName: "Same Name"}}},
			store.PeopleCaptureMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, stdout: io.Discard}
	res, err := (&peopleCmd{Limit: 10}).connectedPeopleResult(rt, s, peopleCoverage(nil))
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("equal names: %+v %v", res, err)
	}
	rt.renderPeopleDetails(newList([]any{personItem{}}, false))
	// An archive-open error after a successful optional coverage refresh remains explicit.
	old := openPeopleRead
	t.Cleanup(func() { openPeopleRead = old })
	openPeopleRead = func(context.Context, string) (*store.Store, error) { return nil, io.ErrUnexpectedEOF }
	rt.dbPath = e.db
	if err := rt.peopleRead("people sources", func(*store.Store) (*listResult, error) { t.Fatal("open error must stop"); return nil, nil }); err == nil {
		t.Fatal("open failure hidden")
	}
}
