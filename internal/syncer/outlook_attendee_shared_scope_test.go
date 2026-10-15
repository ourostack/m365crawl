package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeSharedRegistrationAndIntervalScope(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	ctx := context.Background()
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	path := filepath.Join(root, "Main", "HxStore.hxd")
	if pending, err := beginSharedAttendee(ctx, s, path, "synthetic", *now, false); err != nil || pending != nil {
		t.Fatal("optional lane bypassed interval", err)
	}
	*now = now.Add(6 * time.Minute)
	other := attendeeTestRoot(t, false)
	if pending, err := beginSharedAttendee(ctx, s, filepath.Join(other, "Main", "HxStore.hxd"), "synthetic", *now, true); err != nil || pending != nil {
		t.Fatal("unregistered native root acquired", err)
	}
	old := peopleResolve
	peopleResolve = func(string) (string, error) { return "", errors.New("synthetic") }
	if _, err := beginSharedAttendee(ctx, s, path, "synthetic", *now, true); err == nil {
		t.Fatal("unqualified shared origin admitted")
	}
	peopleResolve = old
	t.Cleanup(func() { peopleResolve = old })
	peopleSQL(t, db, `update outlook_attendee_requests set request_json='{}'`)
	if _, err := beginSharedAttendee(ctx, s, path, "synthetic", *now, true); err == nil {
		t.Fatal("shared request corruption ignored")
	}
}

func TestAttendeeSharedSnapshotFailureClosesOnce(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, false), newDB(t)
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	old := outlookSnapshot
	t.Cleanup(func() { outlookSnapshot = old })
	cleanups := 0
	outlookSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
		return outlookdesktop.Info{}, func() { cleanups++ }, errors.New("synthetic snapshot failure")
	}
	opts := outlookOpts(db, root)
	opts.FullRead = true
	rep, _, _ := Run(ctx, opts)
	if cleanups != 1 || count(t, db, `select count(*) from outlook_attendee_members`) != 1 || sourceKeyed(t, rep, "outlook|Main|attendees").Status != "failed" {
		t.Fatal("snapshot failure leaked/admitted optional capture", rep)
	}
}

func TestAttendeeSharedMissingCalendarAssociation(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, false), newDB(t)
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	pending, err := beginSharedAttendee(ctx, s, filepath.Join(root, "Main", "HxStore.hxd"), "synthetic", outlookNow(), true)
	if err != nil {
		t.Fatal(err)
	}
	info, cleanup, err := outlookSnapshot(ctx, filepath.Join(root, "Main", "HxStore.hxd"))
	if err != nil {
		t.Fatal(err)
	}
	got := finishSharedOutlook(ctx, s, "Main", info, cleanup, nil, nil, pending, nil, 0, nil, outlookNow())
	if len(got) != 1 || got[0].report.Status != "failed" || count(t, db, `select count(*) from outlook_attendee_members`) != 1 {
		t.Fatal("missing ordinary decode became native association", got)
	}
}
