package syncer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func resetPeopleSeams(t *testing.T) {
	t.Helper()
	abs, resolve, def, discover := peopleAbs, peopleResolve, peopleDefault, peopleDiscover
	open, read, acquire, hash := peopleOpen, peopleOpenRead, peopleAcquire, peopleHashOpen
	t.Cleanup(func() {
		peopleAbs, peopleResolve, peopleDefault, peopleDiscover = abs, resolve, def, discover
		peopleOpen, peopleOpenRead, peopleAcquire, peopleHashOpen = open, read, acquire, hash
	})
}

func TestPeopleRunnerSourceSelectionAndDiscoveryErrors(t *testing.T) {
	root := mailRoot(t)
	ctx := context.Background()
	// Actual no-people HxStore fixture exercises the private reader without mocking collection.
	db := newDB(t)
	first, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || len(first.Sources) != 1 {
		t.Fatalf("fixture registration: %+v %v", first, err)
	}
	id := first.Sources[0].ID
	for _, options := range []PeopleOptions{
		{DBPath: db, SourceID: id, MaxAge: 0},
		{DBPath: db, SourceID: id, Root: root, Explicit: true, Discover: true},
		{DBPath: db, Root: t.TempDir(), Explicit: true, Discover: true},
	} {
		if _, err := SyncPeople(ctx, options); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := SyncPeople(ctx, PeopleOptions{DBPath: db, SourceID: 999, MaxAge: 0}); err == nil {
		t.Fatal("unknown selected source accepted")
	}
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := syncPeople(ctx, s, PeopleOptions{DBPath: db, Discover: true}); err == nil {
		t.Fatal("implicit discovery accepted")
	}
	for _, mode := range []string{"default", "resolve", "abs", "discover", "read-open", "write-open", "lock"} {
		t.Run(mode, func(t *testing.T) {
			resetPeopleSeams(t)
			fail := errors.New("synthetic seam")
			switch mode {
			case "default":
				peopleDefault = func() (string, error) { return "", fail }
			case "resolve":
				peopleResolve = func(string) (string, error) { return "", fail }
			case "abs":
				peopleAbs = func(string) (string, error) { return "", fail }
			case "discover":
				peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
					return nil, nil, nil, fail
				}
			case "read-open":
				peopleOpenRead = func(context.Context, string) (*store.Store, error) { return nil, fail }
			case "write-open":
				peopleOpen = func(context.Context, string) (*store.Store, error) { return nil, fail }
			case "lock":
				peopleAcquire = func(string) (func(), error) { return nil, fail }
			}
			options := PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}
			if mode == "default" {
				options.Root = ""
			}
			if mode == "read-open" {
				options.Explicit = false
			}
			if _, err := SyncPeople(ctx, options); !errors.Is(err, fail) {
				t.Fatalf("source error hidden: %v", err)
			}
		})
	}
	resetPeopleSeams(t)
	peopleDefault = func() (string, error) { return root, nil }
	if _, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		return nil, nil, []outlookdesktop.SkippedProfile{{Name: "Missing", Dir: filepath.Join(root, "Missing")}}, nil
	}
	if report, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || report.Failed != 1 {
		t.Fatalf("skipped source not explicit: %+v %v", report, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := syncPeople(cancelled, s, PeopleOptions{}); err == nil {
		t.Fatal("cancelled scheduling succeeded")
	}
	// Closing the actual store propagates query failures through source and locator selection.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := syncPeople(ctx, s, PeopleOptions{}); err == nil {
		t.Fatal("closed source query accepted")
	}
	if _, err := selectedPeopleSources(ctx, s, 0, nil); err == nil {
		t.Fatal("closed coverage query accepted")
	}
	if _, err := beginSharedPeople(ctx, s, root, "", time.Now(), true); err == nil {
		t.Fatal("closed shared source query accepted")
	}
}

type failedPeopleHash struct{ close bool }

func (f failedPeopleHash) Read([]byte) (int, error) {
	if f.close {
		return 0, io.EOF
	}
	return 0, errors.New("hash read")
}

func (f failedPeopleHash) Close() error {
	if f.close {
		return errors.New("hash close")
	}
	return nil
}

func TestPeoplePrivateDigestErrorsAreExplicit(t *testing.T) {
	root := mailRoot(t)
	info, cleanup, err := outlookdesktop.Snapshot(context.Background(), filepath.Join(root, "Main", outlookdesktop.StoreFileName))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, mode := range []string{"open", "read", "close"} {
		t.Run(mode, func(t *testing.T) {
			resetPeopleSeams(t)
			peopleHashOpen = func(string) (io.ReadCloser, error) {
				if mode == "open" {
					return nil, os.ErrPermission
				}
				return failedPeopleHash{close: mode == "close"}, nil
			}
			if _, _, err := collectPeopleCapture(context.Background(), info); err == nil {
				t.Fatal("private digest failure hidden")
			}
		})
	}
}
