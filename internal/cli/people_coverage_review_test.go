package cli

import (
	"context"
	"io"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestPeopleReviewedCurrentCoverageErrorsAreExplicit(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	peopleArchiveSQL(t, e.db, "update knowledge_attempts set started_at='bad'")
	rt := &runtime{ctx: context.Background(), g: &Globals{}, stdout: io.Discard}
	if _, err := (&peopleCmd{Limit: 10}).connectedPeopleResult(rt, s, peopleCoverage(nil)); err == nil {
		t.Fatal("current source coverage error hidden")
	}
}

func TestPeopleReviewedLegacyEqualNameTieBreak(t *testing.T) {
	e := newEnv(t)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	_, err = s.ApplyPeople(context.Background(), []teamsdesktop.Person{
		{TenantID: "fixture", ID: "one", DisplayName: "Same"}, {TenantID: "fixture", ID: "two", DisplayName: "Same"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, stdout: io.Discard}
	res, err := (&peopleCmd{Limit: 10}).connectedPeopleResult(rt, s, peopleCoverage(nil))
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("legacy tie: %+v %v", res, err)
	}
}
