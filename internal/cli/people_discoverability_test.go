package cli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleDefaultListHelpExposesWorkingOptions(t *testing.T) {
	e := newEnv(t)
	code, out, stderr := e.run("people", "--help")
	if code != 0 || stderr != "" {
		t.Fatalf("help: %d %s %s", code, out, stderr)
	}
	for _, term := range []string{"--query", "--limit", "--fields keys", "identity_state", "last_seen_at", "newest", "unknown"} {
		if !strings.Contains(out, term) {
			t.Errorf("default list help lacks %q:\n%s", term, out)
		}
	}
	peopleFixture(t, e)
	code, out, stderr = e.run("--max-age", "0", "people", "--query", "fixture", "--limit", "1", "--fields", "id,identity_state")
	if code != 0 || !strings.Contains(out, `"identity_state":"unresolved"`) || !strings.Contains(out, `"truncated":true`) {
		t.Fatalf("advertised options: %d %s %s", code, out, stderr)
	}
}

func TestPeopleZeroHitExplainsCoverageWithoutChangingArchive(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "text"} {
		code, out, stderr := e.run("--format", format, "--max-age", "0", "people", "--query", "absent", "--fields", "id")
		if code != 0 || !strings.Contains(out, "matched") || !strings.Contains(out, "coverage") {
			t.Fatalf("empty %s explanation: %d %s %s", format, code, out, stderr)
		}
		if format == "json" {
			m := decode(t, out)
			if m["count"] != float64(0) || m["source_coverage"] == nil || m["note"] == nil {
				t.Fatalf("projected empty coverage: %s", out)
			}
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil || string(before) != string(after) {
		t.Fatalf("read changed archive: %v", err)
	}
}

func TestOverviewRoutesRegisteredPeopleWithoutSyncOrInventedCounts(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	before, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "text"} {
		code, out, stderr := e.run("--format", format, "--max-age", "1ns")
		if code != 0 || !strings.Contains(out, "people sources") || !strings.Contains(out, "unresolved") {
			t.Fatalf("registered people overview %s: %d %s %s", format, code, out, stderr)
		}
		if format == "json" {
			var res overviewResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatal(err)
			}
			if len(res.Sources) != len(overviewKinds) || res.Sources[6].Source != "people" || res.Sources[6].Observations == nil {
				t.Fatalf("people row missing from the overview: %s", out)
			}
			found := strings.Contains(res.Sources[6].Note, "m365crawl --max-age 0 people sources")
			if !found {
				t.Fatal("overview lacks executable people coverage route")
			}
		}
	}
	after, err := os.ReadFile(e.db)
	if err != nil || string(before) != string(after) {
		t.Fatalf("overview changed archive: %v", err)
	}
}

func TestPeopleEmptyCoverageAcrossSourceStates(t *testing.T) {
	for _, kind := range []string{"unknown", "empty", "partial", "failed", "mixed", "account"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			if kind == "mixed" {
				e.sync()
			}
			peopleFixture(t, e)
			switch kind {
			case "empty":
				peopleArchiveSQL(t, e.db, "delete from people_observations")
			case "unknown":
				s, err := store.Open(context.Background(), e.db)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.BeginKnowledgeAttempt(context.Background(), store.KnowledgeInput{
					Kind: "outlook_people", Platform: "synthetic", Locator: "/unknown/HxStore.hxd", At: noteT0})
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			case "partial", "failed":
				peopleArchiveSQL(t, e.db, "update knowledge_attempts set outcome='"+kind+"',reason='synthetic_fixture_loss'")
			}
			args := []string{"--max-age", "0", "people", "--fields", "id"}
			if kind != "empty" {
				args = append(args, "--query", "absent")
			}
			if kind == "account" {
				args = append([]string{"--account", "tenant/user"}, args...)
			}
			code, out, stderr := e.run(args...)
			if code != 0 {
				t.Fatalf("empty read: %d %s %s", code, out, stderr)
			}
			m := decode(t, out)
			note, _ := m["note"].(string)
			if m["count"] != float64(0) || m["source_coverage"] == nil ||
				!strings.Contains(note, "matched") || !strings.Contains(note, "completeness") {
				t.Fatalf("empty state meaning: %s", out)
			}
			if kind == "account" && !strings.Contains(out, "excluded") {
				t.Fatalf("exclusion notice lost: %s", out)
			}
			if (kind == "partial" || kind == "failed") && !strings.Contains(out, kind) {
				t.Fatalf("source failure state lost: %s", out)
			}
		})
	}
}

func TestOverviewPeopleRegistrationFaultIsNotEmptySuccess(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	peopleArchiveSQL(t, e.db, "update knowledge_attempts set started_at='invalid'")
	if code, out, stderr := e.run("--max-age", "0"); code == 0 || !strings.Contains(stderr, "db_error") {
		t.Fatalf("source-read fault hidden: %d %s %s", code, out, stderr)
	}
}

func TestOverviewPeopleRouteIsRegistrationScoped(t *testing.T) {
	for _, state := range []string{"unregistered", "without-optional-schema", "partial", "failed", "unknown", "account"} {
		t.Run(state, func(t *testing.T) {
			e := newEnv(t)
			if state == "unregistered" || state == "without-optional-schema" {
				e.sync()
				if state == "without-optional-schema" {
					peopleArchiveSQL(t, e.db, "drop table note_flags")
				}
			} else {
				peopleFixture(t, e)
				switch state {
				case "partial", "failed":
					peopleArchiveSQL(t, e.db, "update knowledge_attempts set outcome='"+state+"'")
				case "unknown":
					peopleArchiveSQL(t, e.db, "update knowledge_locators set latest_attempt=null,last_admitted=null")
				}
			}
			args := []string{"--max-age", "0"}
			if state == "account" {
				args = append(args, "--account", "tenant/user")
			}
			code, out, stderr := e.run(args...)
			if code != 0 {
				t.Fatalf("overview %s: %d %s %s", state, code, out, stderr)
			}
			m := decode(t, out)
			hasPeople := strings.Contains(out, "people sources")
			wantPeople := state != "unregistered" && state != "without-optional-schema"
			if hasPeople != wantPeople || len(m["sources"].([]any)) != len(overviewKinds) {
				t.Fatalf("registration scope: %s", out)
			}
			if state == "account" && !strings.Contains(out, "omit --account") {
				t.Fatalf("unusable subject-account route: %s", out)
			}
		})
	}
}
