package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestAttendeeDefaultExplicitRootAndSnapshotFailure(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	oldDefault, oldSnapshot := peopleDefault, outlookSnapshot
	t.Cleanup(func() { peopleDefault, outlookSnapshot = oldDefault, oldSnapshot })
	peopleDefault = func() (string, error) { return root, nil }
	cleanups := 0
	outlookSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
		return outlookdesktop.Info{}, func() { cleanups++ }, errors.New("synthetic snapshot failure")
	}
	got, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Explicit: true, Discover: true})
	if err == nil || got.Failed != 1 || cleanups != 1 {
		t.Fatal("failed explicit default snapshot escaped lifecycle", got, err)
	}
}

func TestAttendeeCancelBeforeBegin(t *testing.T) {
	isolateTmp(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := peopleDiscover
	t.Cleanup(func() { peopleDiscover = old })
	peopleDiscover = func(path string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		profiles, classic, skipped, err := old(path)
		cancel()
		return profiles, classic, skipped, err
	}
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-Begin cancellation lost", err)
	}
	if count(t, db, `select count(*) from knowledge_attempts`) != 0 {
		t.Fatal("cancelled discovery registered attempt")
	}
}

func TestAttendeeSourceSelectorAuditsSavedBody(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	ctx := context.Background()
	first, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, SourceID: 999}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	peopleSQL(t, db, `update schema_migrations set version=18`)
	if _, err := attendeeTargets(ctx, s, AttendeeOptions{SourceID: first.Sources[0].ID}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatal("partial old source acquired", err)
	}
	peopleSQL(t, db, `update schema_migrations set version=19`)
	peopleSQL(t, db, `update outlook_attendee_snapshots set result_json='{}'`)
	if _, err := attendeeTargets(ctx, s, AttendeeOptions{SourceID: first.Sources[0].ID}); !errors.Is(err, store.ErrAttendeeArchiveCorrupt) {
		t.Fatal("source selector concealed body corruption", err)
	}
}
