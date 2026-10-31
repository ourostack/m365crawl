package cli

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func peopleArchiveSQL(t *testing.T, path, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestPeopleObservationsMixedLegacyOrdering(t *testing.T) {
	e := newEnv(t)
	e.sync()
	peopleFixture(t, e)
	if code, out, stderr := e.run("--no-sync", "people", "--limit", "3"); code != 0 || !containsJSONKey(out, "source_coverage") {
		t.Fatalf("mixed read: %d %s %s", code, out, stderr)
	}
	code, out, _ := e.run("--no-sync", "people", "--query", "no-matches")
	if code != 0 || !containsJSONKey(out, "source_coverage") {
		t.Fatalf("mixed zero matches: %s", out)
	}
}

func TestPeopleObservationsOpenAndQueryFaults(t *testing.T) {
	for _, stage := range []string{"first-open", "second-open", "legacy-query", "native-query", "detail-query", "sources-query", "refresh-query"} {
		t.Run(stage, func(t *testing.T) {
			e := newEnv(t)
			peopleFixture(t, e)
			old := openPeopleRead
			t.Cleanup(func() { openPeopleRead = old })
			calls := 0
			openPeopleRead = func(ctx context.Context, path string) (*store.Store, error) {
				calls++
				if stage == "first-open" || stage == "second-open" && calls == 2 {
					return nil, errors.New("synthetic open")
				}
				s, err := store.OpenReadOnly(ctx, path)
				if err != nil {
					return nil, err
				}
				switch stage {
				case "legacy-query":
					peopleArchiveSQL(t, e.db, "drop table people")
				case "native-query", "detail-query":
					peopleArchiveSQL(t, e.db, "update people_observations set version_raw='bad'")
				case "sources-query", "refresh-query":
					peopleArchiveSQL(t, e.db, "update knowledge_attempts set started_at='bad'")
				}
				return s, nil
			}
			args := []string{"--no-sync", "people"}
			if stage == "detail-query" {
				args = append(args, "show", "person-observation:1")
			}
			if stage == "sources-query" {
				args = append(args, "sources")
			}
			if stage == "refresh-query" {
				oldSync := runPeopleSync
				t.Cleanup(func() { runPeopleSync = oldSync })
				runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
					return syncer.PeopleReport{}, errors.New("refresh")
				}
			}
			if code, out, errout := e.run(args...); code == 0 {
				t.Fatalf("query fault hidden: %s %s", out, errout)
			}
		})
	}
}

func TestPeopleObservationsMixedTextIncludesConflicts(t *testing.T) {
	e := textEnv(t)
	peopleFixture(t, e)
	if code, out, _ := e.run("--format", "text", "--no-sync", "people", "show", "person-observation:1"); code != 0 || !strings.Contains(out, "conflicting claim") {
		t.Fatalf("native text conflicts: %d %s", code, out)
	}
}
