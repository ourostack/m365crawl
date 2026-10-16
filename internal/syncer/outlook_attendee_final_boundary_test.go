package syncer

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeCurrentBodyAndSharedBeginRefusal(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, false), newDB(t)
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(6 * time.Minute)
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	targets, err := attendeeTargets(ctx, s, AttendeeOptions{Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	peopleSQL(t, db, `update outlook_attendee_snapshots set result_json='{}'`)
	if _, err := captureAttendee(ctx, s, targets[0]); !errors.Is(err, store.ErrAttendeeArchiveCorrupt) {
		t.Fatal("cheap unchanged certified corrupt native body", err)
	}
	peopleSQL(t, db, `create trigger fail_native_begin before insert on knowledge_attempts begin select raise(abort,'synthetic'); end`)
	if _, err := beginSharedAttendee(ctx, s, filepath.Join(root, "Main", "HxStore.hxd"), "synthetic", *now, true); err == nil {
		t.Fatal("shared Begin failure swallowed")
	}
	_ = s.Close()
	if _, err := selectedAttendeeSources(ctx, s, 0, nil); err == nil {
		t.Fatal("source read error swallowed")
	}
}

func TestAttendeeChangedOriginAfterGuard(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	oldSnapshot, oldResolve := outlookSnapshot, peopleResolve
	t.Cleanup(func() { outlookSnapshot, peopleResolve = oldSnapshot, oldResolve })
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := oldSnapshot(ctx, path)
		peopleResolve = func(string) (string, error) { return "/synthetic/foreign/HxStore.hxd", nil }
		return info, cleanup, err
	}
	if _, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err == nil {
		t.Fatal("changed canonical origin admitted")
	}
	if count(t, db, `select count(*) from outlook_calendar_reads`) != 0 {
		t.Fatal("changed origin overwrote calendar")
	}
}

type attendeeAfterCommitContext struct {
	context.Context
	db     *sql.DB
	cancel context.CancelFunc
}

func (c attendeeAfterCommitContext) Err() error {
	var count int
	if err := c.db.QueryRowContext(context.Background(), `select count(*) from outlook_attendee_members`).Scan(&count); err == nil && count > 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestAttendeeCancellationAfterCommittedBoundary(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw := openRaw(t, db)
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	targets, err := attendeeTargets(context.Background(), s, AttendeeOptions{Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = captureAttendee(attendeeAfterCommitContext{ctx, raw, cancel}, s, targets[0])
	if !errors.Is(err, context.Canceled) {
		t.Fatal("post-commit reporting cancellation disappeared", err)
	}
	if count(t, db, `select count(*) from outlook_attendee_members`) != 1 {
		t.Fatal("already committed healthy admission was relabeled as uncommitted")
	}
}
