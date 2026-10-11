package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func peopleFixture(t *testing.T, e *env) {
	t.Helper()
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	at := time.Now().UTC()
	a, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/HxStore.hxd", At: at})
	if err != nil {
		t.Fatal(err)
	}
	result := outlookpeople.Result{People: []outlookpeople.Person{
		{Class: 0xd2, Key: 1, Email: "fixture@example.invalid", DisplayName: "Fixture Person", TeamsID: "8:orgid:one", RefreshedAt: at},
		{Class: 0xd2, Key: 1, Email: "fixture@example.invalid", DisplayName: "Fixture Person", TeamsID: "8:orgid:two", Resynced: true},
	}}
	if err := s.CommitPeopleObservations(context.Background(), a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "one", Qualification: store.PeopleQualification, At: at},
		result, store.PeopleCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

func TestPeopleObservationsHelpValidationAndFilteredScope(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	for _, args := range [][]string{
		{"people", "--help"}, {"people", "sync", "--help"}, {"people", "show", "--help"}, {"people", "sources", "--help"},
	} {
		if code, _, _ := e.run(args...); code != 0 {
			t.Fatalf("help: %v", args)
		}
	}
	for _, args := range [][]string{
		{"--max-age", "0", "people", "show", "mail:fixture@example.invalid"},
		{"--max-age", "0", "people", "show", "person-observation:999"},
		{"--max-age", "0", "people", "show", "person-observation:1", "--fields", "wrong"},
		{"--max-age", "0", "people", "sources", "--fields", "wrong"},
		{"people", "--limit", "0"},
	} {
		if code, _, _ := e.run(args...); code != 2 {
			t.Fatalf("validation: %v exit%d", args, code)
		}
	}
	code, out, _ := e.run("--max-age", "0", "people", "--limit", "1", "--fields", "id,identity_state")
	if code != 0 || !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("merged truncation: %s", out)
	}
	code, out, _ = e.run("--max-age", "0", "people", "show", "person-observation:1")
	if code != 0 || !strings.Contains(out, `"conflicts":[{`) {
		t.Fatalf("detail conflicts: %s", out)
	}
	if code, _, _ := e.run("--max-age", "0", "people", "--query", "person-observation:1"); code != 0 {
		t.Fatal("exact stable ID query failed")
	}
}

func TestPeopleObservationsRefreshFailureServesRetainedEvidence(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	old := runPeopleSync
	t.Cleanup(func() { runPeopleSync = old })
	runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
		return syncer.PeopleReport{}, errors.New("synthetic refresh failure")
	}
	if code, out, _ := e.run("people", "--query", "fixture"); code != 0 || !strings.Contains(out, `"refresh_error"`) {
		t.Fatalf("retained refresh failure: %d %s", code, out)
	}
	runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
		return syncer.PeopleReport{}, context.Canceled
	}
	if code, _, _ := e.run("people", "sources"); code == 0 {
		t.Fatal("cancellation swallowed")
	}
}

func TestPeopleObservationsSyncUsesExplicitRegistration(t *testing.T) {
	e := newEnv(t)
	old := runPeopleSync
	t.Cleanup(func() { runPeopleSync = old })
	runPeopleSync = func(_ context.Context, options syncer.PeopleOptions) (syncer.PeopleReport, error) {
		if !options.Explicit || !options.Discover || options.Root != "/synthetic" {
			t.Fatal("sync did not explicitly register selected root")
		}
		return syncer.PeopleReport{Captured: 1, Sources: []store.KnowledgeSource{{Kind: "outlook_people"}}}, nil
	}
	if code, out, _ := e.run("people", "sync", "--root", "/synthetic"); code != 0 || !containsJSONKey(out, "source_coverage") {
		t.Fatalf("explicit sync: %d %s", code, out)
	}
	runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
		return syncer.PeopleReport{}, errors.New("sync")
	}
	if code, _, _ := e.run("people", "sync"); code == 0 {
		t.Fatal("explicit sync error hidden")
	}
}

func TestPeopleObservationsMetadataAdvertisesConnectedCommands(t *testing.T) {
	m := manifest()
	for _, name := range []string{"people", "people sync", "people show", "people sources"} {
		if _, ok := m.Commands[name]; !ok {
			t.Fatalf("manifest lacks %s", name)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(raw), "outlook_people") {
		t.Fatal("manifest privacy scope missing")
	}
}
