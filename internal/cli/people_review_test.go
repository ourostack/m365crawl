package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestPeopleReviewedLimitedPrefixMatchesFullOrder(t *testing.T) {
	e := newEnv(t)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	a, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture", At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	people := []outlookpeople.Person{}
	for i := 1; i <= 12; i++ {
		people = append(people, outlookpeople.Person{Class: 0xd2, Key: uint32(i), DisplayName: "Same Name", Email: fmt.Sprintf("fixture%d@example.invalid", i)})
	}
	if err := s.CommitPeopleObservations(context.Background(), a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: store.PeopleQualification, At: time.Now()},
		outlookpeople.Result{People: people}, store.PeopleCaptureMetadata{}); err != nil {
		t.Fatal(err)
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{MaxText: 4}}
	all, err := (&peopleCmd{Limit: 20}).connectedPeopleResult(rt, s, peopleCoverage(nil))
	if err != nil {
		t.Fatal(err)
	}
	small, err := (&peopleCmd{Limit: 3}).connectedPeopleResult(rt, s, peopleCoverage(nil))
	if err != nil {
		t.Fatal(err)
	}
	for i := range small.Items {
		if small.Items[i].(peopleObservationItem).ID != all.Items[i].(peopleObservationItem).ID {
			t.Fatal("limited list is not a prefix of full ordering")
		}
	}
}

func TestPeopleReviewedCoverageFollowsSameCommandLegacyRefresh(t *testing.T) {
	e := newEnv(t)
	e.sync()
	peopleFixture(t, e)
	e.exec("delete from sync_runs")
	oldSync, oldPeople := runSync, runPeopleSync
	t.Cleanup(func() { runSync, runPeopleSync = oldSync, oldPeople })
	runPeopleSync = func(ctx context.Context, options syncer.PeopleOptions) (syncer.PeopleReport, error) {
		s, err := store.OpenReadOnly(ctx, e.db)
		if err != nil {
			return syncer.PeopleReport{}, err
		}
		defer func() { _ = s.Close() }()
		sources, err := s.KnowledgeSources(ctx, "outlook_people")
		return syncer.PeopleReport{Sources: sources}, err
	}
	runSync = func(ctx context.Context, options syncer.Options) (syncer.Report, []syncer.Change, error) {
		s, err := store.Open(ctx, e.db)
		if err != nil {
			return syncer.Report{}, nil, err
		}
		defer func() { _ = s.Close() }()
		sources, err := s.KnowledgeKindLocators(ctx, "outlook_people", 0)
		if err != nil {
			return syncer.Report{}, nil, err
		}
		a, err := s.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "outlook_people", Platform: sources[0].Platform, Locator: sources[0].Locator, At: time.Now()})
		if err != nil {
			return syncer.Report{}, nil, err
		}
		err = s.CommitPeopleObservations(ctx, a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "two", Qualification: store.PeopleQualification, At: time.Now()},
			outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 99, DisplayName: "Fresh Native"}}}, store.PeopleCaptureMetadata{})
		return syncer.Report{Status: "ok"}, nil, err
	}
	code, out, stderr := e.run("people", "--query", "Fresh Native")
	if code != 0 {
		t.Fatalf("mixed refresh: %s %s", out, stderr)
	}
	var doc struct {
		Items []struct {
			CaptureID int64 `json:"capture_id"`
		} `json:"items"`
		Coverage struct {
			Sources []struct {
				AdmittedID int64 `json:"admitted_id"`
			} `json:"sources"`
		} `json:"source_coverage"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Items) != 1 || doc.Items[0].CaptureID != doc.Coverage.Sources[0].AdmittedID {
		t.Fatalf("stale same-command coverage: %s", out)
	}
}
