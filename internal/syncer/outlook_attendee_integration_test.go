package syncer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestAttendeeOneCopyLifecycle(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, true), newDB(t)
	oldPeople := peopleCollect
	peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
		return outlookpeople.Result{}, "synthetic-people", nil
	}
	t.Cleanup(func() { peopleCollect = oldPeople })
	if _, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
		t.Fatal(err)
	}
	if got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || got.Captured != 1 {
		t.Fatalf("register native: %#v %v", got, err)
	}
	oldSnapshot, oldAttendee := outlookSnapshot, attendeeCollect
	t.Cleanup(func() { outlookSnapshot, attendeeCollect = oldSnapshot, oldAttendee })
	copies, cleanups := 0, 0
	peopleRead, nativeRead := false, false
	private := ""
	peopleCollect = func(_ context.Context, info outlookdesktop.Info) (outlookpeople.Result, string, error) {
		if cleanups != 0 || info.Path != private {
			t.Fatal("people read another/deleted private copy")
		}
		peopleRead = true
		return outlookpeople.Result{}, "synthetic-people", nil
	}
	attendeeCollect = func(ctx context.Context, info outlookdesktop.Info, selections *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
		if cleanups != 0 || info.Path != private || selections == nil {
			t.Fatal("native read lacked same-copy actual selection receipts")
		}
		nativeRead = true
		return oldAttendee(ctx, info, selections)
	}
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		copies++
		info, cleanup, err := oldSnapshot(ctx, path)
		private = info.Path
		return info, func() {
			if !peopleRead || !nativeRead {
				t.Error("cleanup ran before every eligible lane")
			}
			cleanups++
			cleanup()
		}, err
	}
	opts := outlookOpts(db, root)
	opts.FullRead = true
	rep, _ := run(t, opts)
	if copies != 1 || cleanups != 1 || !peopleRead || !nativeRead {
		t.Fatalf("lifecycle: copies=%d cleanup=%d people=%v native=%v", copies, cleanups, peopleRead, nativeRead)
	}
	for _, path := range []string{private, filepath.Dir(private)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("shared optional-lane cleanup left its owned payload or snapshot root")
		}
	}
	if got := sourceKeyed(t, rep, "outlook|Main|attendees"); got.Status == "failed" {
		t.Fatalf("native capture failed: %#v", got)
	}
}

func TestAttendeeStandaloneMailFreshness(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, db := attendeeTestRoot(t, true), newDB(t)
	oldNow := outlookNow
	now := time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC)
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = oldNow })
	run(t, outlookOpts(db, root))
	appendBlock(t, root, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 13, Stamp: 1, DetailKey: 23, FolderKey: 101, Received: mailDay, Subject: "Synthetic third", SenderAddr: "third@example.invalid"}),
		hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 23, Stamp: 1, MessageID: "<third@example.invalid>", Class: "IPM.Note", Sent: mailDay}))
	touch(t, root, time.Minute)
	now = now.Add(6 * time.Minute)
	got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if err != nil || got.Captured != 1 {
		t.Fatalf("standalone: %#v %v", got, err)
	}
	if count(t, db, `select count(*) from mail_messages`) != 2 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 0 {
		t.Fatal("standalone read/erased mail or retained stale read marker")
	}
	now = now.Add(6 * time.Minute)
	rep, _ := run(t, outlookOpts(db, root))
	if got := sourceKeyed(t, rep, "outlook|Main|mail"); got.Status == StatusUnchanged || got.Counts.Mail.Added != 1 {
		t.Fatal("ordinary unchanged B skipped pending mail", got)
	}
	copies := countSnapshots(t)
	run(t, outlookOpts(db, root))
	if *copies != 0 {
		t.Fatal("healthy optional lane forced another global recapture")
	}
}

func TestAttendeeStandaloneOriginAndScope(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	ctx := context.Background()
	root, other, db := attendeeTestRoot(t, false), attendeeTestRoot(t, false), newDB(t)
	oldNow := outlookNow
	now := oldNow()
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = oldNow })
	if got, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || got.Captured != 1 {
		t.Fatalf("initial native: %#v %v", got, err)
	}
	oldOrigin := filepath.Join(root, "Main", "HxStore.hxd")
	now = now.Add(6 * time.Minute)
	if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: other, Explicit: true, Discover: true}); err == nil {
		t.Fatal("same-name foreign origin overwrote ordinary calendar")
	}
	var origin string
	if err := openRaw(t, db).QueryRow(`select r.origin from outlook_calendar_read_state s join outlook_calendar_reads r on r.rowid=s.read_id where s.account_id='outlook/Main'`).Scan(&origin); err != nil || origin != oldOrigin {
		t.Fatal("origin collision advanced calendar", origin, err)
	}
	if _, err := os.Stat(oldOrigin); err != nil {
		t.Fatal("synthetic source changed", err)
	}
}
