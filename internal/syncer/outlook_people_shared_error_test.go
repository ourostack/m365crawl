package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleSharedPreparationFailureStaysOptional(t *testing.T) {
	isolateTmp(t)
	root := mailRoot(t)
	db := newDB(t)
	if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	old := peopleResolve
	t.Cleanup(func() { peopleResolve = old })
	peopleResolve = func(string) (string, error) { return "", errors.New("people locator") }
	report, _ := run(t, outlookOpts(db, root))
	found := false
	for _, source := range report.Sources {
		if source.Source == "outlook|Main|people" {
			found = true
			if source.Error == nil {
				t.Fatal("shared preparation error hidden")
			}
		}
	}
	if !found || report.Status == "partial" {
		t.Fatal("optional preparation changed legacy settlement")
	}
	if out := optionalPeopleOutcome("Main", errs.DBError(context.Canceled)); out.report.Error == nil {
		t.Fatal("coded failure hidden")
	}
}

func TestPeopleUnregisteredSharedPathDoesNotProbeLocator(t *testing.T) {
	s, err := store.Open(context.Background(), newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	old := peopleResolve
	t.Cleanup(func() { peopleResolve = old })
	peopleResolve = func(string) (string, error) {
		t.Fatal("unregistered shared source probed")
		return "", nil
	}
	if pending, err := beginSharedPeople(context.Background(), s, "/unregistered", "", time.Now(), true); err != nil || pending != nil {
		t.Fatalf("unregistered source: %v", err)
	}
}
