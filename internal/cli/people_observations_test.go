package cli

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleObservationsConnectedListShowAndCoverage(t *testing.T) {
	e := newEnv(t)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	a, err := s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{
		Kind: "outlook_people", Platform: "synthetic", Locator: "/fixture/HxStore.hxd", At: at})
	if err != nil {
		t.Fatal(err)
	}
	err = s.CommitPeopleObservations(context.Background(), a, store.KnowledgeOutcome{
		Status: "complete", ContentDigest: "one", Qualification: store.PeopleQualification, At: at},
		outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, DisplayName: "Fixture Person",
			Email: "fixture@example.invalid", TeamsID: "8:orgid:11111111-1111-1111-1111-111111111111"}}},
		store.PeopleCaptureMetadata{AcquiredAt: &at, SourceObservedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	code, out, _ := e.run("--no-sync", "people", "--query", "fixture")
	if code != 0 {
		t.Fatalf("list exit %d: %s", code, out)
	}
	var list struct {
		Items []struct {
			ID            string `json:"id"`
			IdentityState string `json:"identity_state"`
		} `json:"items"`
		Coverage json.RawMessage `json:"source_coverage"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list.Items) != 1 || list.Items[0].IdentityState != "unresolved" || len(list.Coverage) == 0 {
		t.Fatalf("connected observation list: %s", out)
	}
	code, out, _ = e.run("--no-sync", "people", "show", list.Items[0].ID)
	if code != 0 {
		t.Fatalf("show exit %d: %s", code, out)
	}
	var detail struct {
		Items []struct {
			ID     string            `json:"id"`
			Copies []json.RawMessage `json:"copies"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &detail); err != nil || len(detail.Items) != 1 || detail.Items[0].ID != list.Items[0].ID || len(detail.Items[0].Copies) != 1 {
		t.Fatalf("detail identity: %s", out)
	}
	code, out, stderr := e.run("--no-sync", "people", "--query", "no-match", "--fields", "id")
	if code != 0 || !containsJSONKey(out, "source_coverage") {
		t.Fatalf("zero-hit coverage: %d %s %s", code, out, stderr)
	}
	code, out, _ = e.run("--no-sync", "people", "sources")
	if code != 0 || !containsJSONKey(out, "source_coverage") {
		t.Fatalf("source coverage: %d %s", code, out)
	}
}

func containsJSONKey(raw, key string) bool {
	var value map[string]json.RawMessage
	return json.Unmarshal([]byte(raw), &value) == nil && value[key] != nil
}
