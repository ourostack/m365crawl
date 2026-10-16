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

func TestAttendeeSourceIDRecoveryLookupFailureIsTerminal(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, true), newDB(t)
	old := peopleResolve
	t.Cleanup(func() { peopleResolve = old })
	peopleResolve = func(string) (string, error) { return "", errors.New("synthetic unknown origin") }
	run(t, outlookOpts(db, root))
	peopleResolve = old
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	path := filepath.Join(root, "Main", "HxStore.hxd")
	before := attendeeArchiveState(t, db)
	status, err := captureAttendee(ctx, s, attendeeTarget{
		profile: outlookdesktop.Profile{Name: "Main", StorePath: path}, locator: outlookSourceOrigin(path),
		request: store.AttendeeRequest{SourceID: 999},
	})
	if status != "failed" || !errors.Is(err, store.ErrKnowledgeSourceUnknown) || attendeeArchiveState(t, db) != before {
		t.Fatal("failed request validation continued into capture or mutated archive", status, err)
	}
}

func TestAttendeeActualSourceIDCarriesSavedBootstrapRoot(t *testing.T) {
	for _, sourceOnlyLatest := range []bool{false, true} {
		name := "original-request"
		if sourceOnlyLatest {
			name = "successful-source-id-latest"
		}
		t.Run(name, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			ctx := context.Background()
			root, db := attendeeTestRoot(t, true), newDB(t)
			now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
			first, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
			if err != nil || first.Captured != 1 {
				t.Fatal(first, err)
			}
			id := first.Sources[0].ID
			if sourceOnlyLatest {
				touch(t, root, time.Minute)
				*now = now.Add(6 * time.Minute)
				retry, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, SourceID: id, Explicit: true})
				if err != nil || retry.Reused != 1 {
					t.Fatal("actual source-ID request did not admit", retry, err)
				}
			}
			s, err := store.Open(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			view, err := s.AttendeeSources(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			request := view.Coverage.Sources[0].Request
			if sourceOnlyLatest && (request.Root != "" || request.SourceID != id) {
				t.Fatal("source-ID retry selectors were rewritten", request)
			}
			read, err := s.OutlookReadReceipt(ctx, "outlook/Main")
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.CommitOutlook(ctx, store.OutlookBatch{Account: "outlook/Main", At: read.At.Add(time.Minute), Zone: time.UTC,
				Stamp: read.MapperStamp}, func(store.CalendarResult) store.Run {
				return store.Run{StartedAt: read.At, FinishedAt: read.At, Source: "outlook|Main", Status: "ok", Fingerprint: "synthetic-unqualified"}
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			before := attendeeArchiveState(t, db)
			report, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, SourceID: id, Explicit: true})
			var bootstrap *AttendeeBootstrapRequired
			if !errors.As(err, &bootstrap) || report.Failed != 1 {
				t.Fatal("actual source-ID unknown refusal lost typed recovery", report, err)
			}
			if attendeeArchiveState(t, db) != before {
				t.Fatal("recovering saved request mutated calendar/read/native/mail state")
			}
			if bootstrap.Root != root {
				t.Fatalf("real source-ID handoff dropped validated saved root: got %q, want %q", bootstrap.Root, root)
			}
		})
	}
}
