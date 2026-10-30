package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func TestPeopleObservationsAccountScopeAndClosedQueries(t *testing.T) {
	e := newEnv(t)
	peopleFixture(t, e)
	var out bytes.Buffer
	s, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt := &runtime{ctx: context.Background(), g: &Globals{Account: "tenant/user"}, stdout: &out}
	res, err := (&peopleCmd{Limit: 1}).connectedPeopleResult(rt, s, peopleCoverage(nil))
	if err != nil || len(res.Items) != 0 || len(res.Notices) != 1 {
		t.Fatalf("subject account exclusion: %+v %v", res, err)
	}
	for _, run := range []func() error{
		func() error { return (&peopleSyncCmd{}).Run(rt) },
		func() error {
			return rt.peopleRead("people sources", func(*store.Store) (*listResult, error) {
				t.Fatal("account should reject before query")
				return nil, nil
			})
		},
	} {
		if run() == nil {
			t.Fatal("subject tenancy inferred")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (&peopleCmd{Limit: 1}).connectedPeopleResult(rt, s, peopleCoverage(nil)); err == nil {
		t.Fatal("closed query hidden")
	}
}

func TestPeopleObservationsRefreshFailsWithoutReadableArchive(t *testing.T) {
	e := newEnv(t)
	old := runPeopleSync
	t.Cleanup(func() { runPeopleSync = old })
	runPeopleSync = func(context.Context, syncer.PeopleOptions) (syncer.PeopleReport, error) {
		return syncer.PeopleReport{}, errors.New("refresh")
	}
	rt := &runtime{ctx: context.Background(), dbPath: e.db}
	if _, err := rt.refreshPeople(); err == nil {
		t.Fatal("missing retained archive hidden")
	}
	peopleFixture(t, e)
	if err := os.WriteFile(e.db, []byte("invalid database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.refreshPeople(); err == nil {
		t.Fatal("invalid retained source query hidden")
	}
}

func TestPeopleObservationsEmptyAndReadFailurePaths(t *testing.T) {
	e := newEnv(t)
	if code, _, _ := e.run("--no-sync", "people", "sources"); code != 0 {
		t.Fatal("empty sources refused")
	}
	if code, _, _ := e.run("--no-sync", "people", "show", "person-observation:1"); code != 2 {
		t.Fatal("missing detail did not report usage")
	}
	peopleFixture(t, e)
	s, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeopleObservation(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The native-only callback error remains explicit through the independent read path.
	rt := &runtime{ctx: context.Background(), g: &Globals{}, dbPath: e.db}
	if err := rt.peopleRead("people show", func(*store.Store) (*listResult, error) { return nil, errors.New("detail") }); err == nil {
		t.Fatal("detail callback error hidden")
	}
	if err := (&peopleCmd{Limit: 1}).runConnectedPeople(&runtime{ctx: context.Background(), g: &Globals{}, dbPath: "missing"}, peopleCoverage(nil)); err == nil {
		t.Fatal("connected missing archive hidden")
	}
}
