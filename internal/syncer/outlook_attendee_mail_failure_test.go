package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
)

func TestAttendeeStandaloneFailurePreservesMailCatchup(t *testing.T) {
	for _, failure := range []string{"native", "cancel_before_admit"} {
		t.Run(failure, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			root, db := attendeeTestRoot(t, true), newDB(t)
			now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
			run(t, outlookOpts(db, root))
			appendBlock(t, root, hxbuild.NewMailHeader(hxbuild.MailHeaderSpec{Key: 13, Stamp: 1, DetailKey: 23, FolderKey: 101, Received: mailDay, Subject: "Synthetic third", SenderAddr: "third@example.invalid"}),
				hxbuild.NewMailDetail(hxbuild.MailDetailSpec{Key: 23, Stamp: 1, MessageID: "<third@example.invalid>", Class: "IPM.Note", Sent: mailDay}))
			touch(t, root, time.Minute)
			*now = now.Add(6 * time.Minute)
			oldCollect, oldSnapshot := attendeeCollect, outlookSnapshot
			t.Cleanup(func() { attendeeCollect, outlookSnapshot = oldCollect, oldSnapshot })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "native" {
				attendeeCollect = func(context.Context, outlookdesktop.Info, *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
					return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", errors.New("synthetic native mapping failure")
				}
			} else {
				outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
					info, cleanup, err := oldSnapshot(ctx, path)
					return info, func() { cleanup(); cancel() }, err
				}
			}
			if _, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err == nil {
				t.Fatal("failed native stage reported success")
			}
			if count(t, db, `select count(*) from outlook_attendee_members`) != 0 || count(t, db, `select count(*) from mail_messages`) != 2 || count(t, db, `select count(*) from meta where key='outlook_mail_read:outlook/Main'`) != 0 {
				t.Fatal("optional failure changed mail or retained stale marker")
			}
			attendeeCollect, outlookSnapshot = oldCollect, oldSnapshot
			*now = now.Add(6 * time.Minute)
			rep, _ := run(t, outlookOpts(db, root))
			if row := sourceKeyed(t, rep, "outlook|Main|mail"); row.Status == StatusUnchanged || row.Counts.Mail.Added != 1 {
				t.Fatal("ordinary B failed mail catch-up after native failure", row)
			}
		})
	}
}
