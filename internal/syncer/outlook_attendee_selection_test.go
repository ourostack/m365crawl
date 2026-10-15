package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeExplicitIntervalAndSourceRetry(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	first, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || first.Captured != 1 {
		t.Fatal(first, err)
	}
	oldDiscover := peopleDiscover
	t.Cleanup(func() { peopleDiscover = oldDiscover })
	peopleDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		t.Fatal("source retry discovered unrelated roots")
		return nil, nil, nil, nil
	}
	options := AttendeeOptions{DBPath: db, SourceID: first.Sources[0].ID, Explicit: true}
	if got, err := SyncAttendees(context.Background(), options); err != nil || got.Skipped != 1 {
		t.Fatal("minimum copy interval bypassed", got, err)
	}
	*now = now.Add(6 * time.Minute)
	if got, err := SyncAttendees(context.Background(), options); err != nil || got.Skipped != 1 {
		t.Fatal("healthy unchanged copied/decoded again", got, err)
	}
	if count(t, db, `select count(*) from outlook_calendar_reads`) != 1 || count(t, db, `select count(*) from outlook_attendee_members`) != 1 {
		t.Fatal("unchanged allocated identities")
	}
	touch(t, root, time.Minute)
	*now = now.Add(6 * time.Minute)
	if got, err := SyncAttendees(context.Background(), options); err != nil || got.Reused != 1 {
		t.Fatal("same representation did not preserve physical rows", got, err)
	}
	if count(t, db, `select count(*) from outlook_attendee_snapshots`) != 1 || count(t, db, `select count(*) from outlook_attendee_members`) != 2 {
		t.Fatal("changed input failed new membership/reuse")
	}
}

func TestAttendeeUnknownOriginBootstrap(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	old := peopleResolve
	peopleResolve = func(string) (string, error) { return "", errors.New("synthetic unknown origin") }
	run(t, outlookOpts(db, root))
	peopleResolve = old
	t.Cleanup(func() { peopleResolve = old })
	*now = now.Add(6 * time.Minute)
	if r, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || r.Captured != 1 {
		t.Fatal("ordinary unknown origin did not bootstrap through actual decode", r, err)
	}
	if count(t, db, `select count(*) from outlook_calendar_reads`) != 2 {
		t.Fatal("origin bootstrap lacked unique ordinary read")
	}
}

func TestAttendeeOriginContinuityBeforeCalendar(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	oldSnapshot, oldResolve := outlookSnapshot, peopleResolve
	t.Cleanup(func() { outlookSnapshot, peopleResolve = oldSnapshot, oldResolve })
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := oldSnapshot(ctx, path)
		peopleResolve = func(string) (string, error) { return "", errors.New("synthetic source continuity lost") }
		return info, cleanup, err
	}
	got, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err == nil || got.Failed != 1 || count(t, db, `select count(*) from outlook_calendar_reads`) != 0 || count(t, db, `select count(*) from outlook_attendee_members`) != 0 {
		t.Fatal("unqualified continuity overwrote calendar", got, err)
	}
}

func TestAttendeeReadOnlyAndInvalidSelection(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	if got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db}); err != nil || len(got.Sources) != 0 {
		t.Fatal(got, err)
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only native request created archive")
	}
	for _, o := range []AttendeeOptions{
		{DBPath: db, SourceID: -1, Explicit: true},
		{DBPath: db, SourceID: 1, Root: "synthetic", Explicit: true},
		{DBPath: db, Discover: true},
		{DBPath: db, Root: "synthetic"},
	} {
		if _, err := SyncAttendees(ctx, o); err == nil {
			t.Fatal("invalid selector accepted")
		}
	}
	root := attendeeTestRoot(t, false)
	if got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || got.Captured != 1 {
		t.Fatal(got, err)
	}
	if got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db}); err != nil || len(got.Sources) != 1 {
		t.Fatal("read-only registered source", got, err)
	}
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, SourceID: 999, Explicit: true}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatal("unknown archive source accepted", err)
	}
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: filepath.Join(t.TempDir(), "missing-parent", "db"), Root: filepath.Join(t.TempDir(), "absent"), Explicit: true}); err == nil {
		t.Fatal("missing copied root accepted")
	}
}
