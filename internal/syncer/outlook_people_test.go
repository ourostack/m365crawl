package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestPeopleRegisteredCaptureReadAndRetention(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	profile := filepath.Join(root, "Fixture")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(profile, outlookdesktop.StoreFileName)
	if err := os.WriteFile(path, []byte("synthetic-one"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := peopleCollect
	t.Cleanup(func() { peopleCollect = old })
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture@example.invalid"}}}, "digest-one", nil
	}
	db := newDB(t)
	first, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || first.Captured != 1 || len(first.Sources) != 1 {
		t.Fatalf("registered capture: %+v %v", first, err)
	}
	read, err := SyncPeople(ctx, PeopleOptions{DBPath: db, MaxAge: time.Hour})
	if err != nil || read.Skipped != 1 {
		t.Fatalf("registered read: %+v %v", read, err)
	}
	unchanged, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Explicit: true})
	if err != nil || unchanged.Unchanged != 1 {
		t.Fatalf("unchanged: %+v %v", unchanged, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	failed, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Explicit: true})
	if err != nil || failed.Failed != 1 || failed.Sources[0].Status != "unavailable" {
		t.Fatalf("failed source: %+v %v", failed, err)
	}
	s, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rows, _, err := s.PeopleObservations(ctx, "fixture", 0, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("last good lost: %d %v", len(rows), err)
	}
}

func TestPeopleUnregisteredOrdinaryReadDoesNotCreateArchive(t *testing.T) {
	db := newDB(t)
	for _, age := range []time.Duration{0, time.Hour} {
		report, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, MaxAge: age})
		if err != nil || len(report.Sources) != 0 {
			t.Fatalf("unregistered read: %+v %v", report, err)
		}
		if _, err := os.Stat(db); !os.IsNotExist(err) {
			t.Fatal("unregistered read created archive")
		}
	}
}

func TestPeopleSharedCopyFailureDoesNotChangeLegacySettlement(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := mailRoot(t)
	db := newDB(t)
	old := peopleCollect
	t.Cleanup(func() { peopleCollect = old })
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "fixture@example.invalid"}}}, "first", nil
	}
	if _, err := SyncPeople(context.Background(), PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{}, "", errors.New("synthetic people failure")
	}
	copies := countSnapshots(t)
	opts := outlookOpts(db, root)
	opts.FullRead = true
	report, _ := run(t, opts)
	if *copies != 1 {
		t.Fatalf("shared source copied %d times", *copies)
	}
	if report.Status == "partial" {
		t.Fatal("optional people failure poisoned legacy status")
	}
	found := false
	for _, source := range report.Sources {
		if source.Source == "outlook|Main|people" {
			found = true
			if source.Status != "failed" || source.Error == nil {
				t.Fatal("optional failure not explicit")
			}
		}
	}
	if !found {
		t.Fatal("registered people source did not reuse normal Outlook capture")
	}
}
