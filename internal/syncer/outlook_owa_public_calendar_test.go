package syncer

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSyncOWAPublicCalendarAndMailShareRealFixtureCopy(t *testing.T) {
	tmp := isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "calendar")
	opts := outlookOpts(db, root)
	opts.Root = t.TempDir()
	copySource, mailClose, calClose := outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose
	t.Cleanup(func() {
		outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose = copySource, mailClose, calClose
	})
	copies, closed := 0, 0
	outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
		copies++
		return copySource(ctx, source)
	}
	outlookOWAClose = func(o *indexeddb.Origin) error { closed++; return mailClose(o) }
	outlookOWACalendarClose = func(o *indexeddb.Origin) error { closed++; return calClose(o) }
	rep, _, err := Run(context.Background(), opts)
	if err != nil || rep.Status != StatusOK || len(rep.Sources) != 2 || rep.Calendar.Events.Seen != 2 || copies != 1 || closed != 2 {
		t.Fatalf("public shared owner/counts: status=%s sources=%d events=%d copies=%d closes=%d err=%v", rep.Status, len(rep.Sources), rep.Calendar.Events.Seen, copies, closed, err)
	}
	cal, mail := sourceKeyed(t, rep, "outlook|Default"), sourceKeyed(t, rep, "outlook|Default|mail")
	if cal.Counts == nil || cal.Counts.Calendar.Events.Seen != 2 || mail.Counts == nil || mail.Counts.Mail.Added != 5 ||
		count(t, db, `select count(*) from calendar_source_events where source='outlook'`) != 2 ||
		count(t, db, `select count(*) from mail_messages`) != 5 ||
		count(t, db, `select count(*) from meta where key like 'outlook_identity:%'`) != 0 {
		t.Fatal("public calendar/mail import invented identity, lost rows or double-counted")
	}
	rep, _, err = Run(context.Background(), opts)
	if err != nil || rep.Status != StatusUnchanged || rep.Calendar.Events.Seen != 0 || copies != 1 || closed != 2 ||
		sourceKeyed(t, rep, "outlook|Default").Status != StatusUnchanged ||
		sourceKeyed(t, rep, "outlook|Default|mail").Status != StatusUnchanged {
		t.Fatal("one public receipt suppressed the other or forced another copy")
	}
	opts.FullRead = true
	rep, _, err = Run(context.Background(), opts)
	if err != nil || rep.Calendar.Events.Seen != 2 || copies != 2 || closed != 4 || len(snapshotDirs(t, tmp)) != 0 {
		t.Fatal("full public read skipped a lane, double-counted or leaked its copy")
	}
}

func TestSyncOWAPublicCalendarFailureKeepsMailAndIndependentRetry(t *testing.T) {
	isolateTmp(t)
	db, root := newDB(t), owaRoot(t, "mail")
	opts := outlookOpts(db, root)
	opts.Root = t.TempDir()
	mailClose, calClose := outlookOWAClose, outlookOWACalendarClose
	t.Cleanup(func() { outlookOWAClose, outlookOWACalendarClose = mailClose, calClose })
	mailReads, calReads := 0, 0
	outlookOWAClose = func(o *indexeddb.Origin) error { mailReads++; return mailClose(o) }
	outlookOWACalendarClose = func(o *indexeddb.Origin) error { calReads++; return calClose(o) }
	for attempt := 0; attempt < 2; attempt++ {
		rep, _, err := Run(context.Background(), opts)
		if err == nil || rep.Status != StatusPartial || len(rep.Sources) != 2 || rep.Calendar.Events.Seen != 0 {
			t.Fatal("a failed public calendar became success or suppressed committed mail")
		}
		cal, mail := sourceKeyed(t, rep, "outlook|Default"), sourceKeyed(t, rep, "outlook|Default|mail")
		wantMail := StatusOK
		if attempt != 0 {
			wantMail = StatusUnchanged
		}
		if cal.Status != StatusFailed || cal.Error == nil || cal.Error.Code != outlookcal.CodeLayoutUnsupported ||
			mail.Status != wantMail || mailReads != 1 || calReads != attempt+1 ||
			count(t, db, `select count(*) from mail_messages`) != 5 ||
			count(t, db, `select count(*) from calendar_source_events`) != 0 {
			t.Fatal("independent public failures or retries changed mail/calendar evidence")
		}
	}
}

func TestSyncOWAPublicCalendarEmptyAndDamagedKeepObservationBoundaries(t *testing.T) {
	for _, variant := range []string{"calendar-empty", "calendar-damaged"} {
		t.Run(variant, func(t *testing.T) {
			isolateTmp(t)
			db, root := newDB(t), owaRoot(t, variant)
			opts := outlookOpts(db, root)
			opts.Root = t.TempDir()
			rep, _, err := Run(context.Background(), opts)
			if err != nil || len(rep.Sources) != 2 || count(t, db, `select count(*) from mail_messages`) != 5 {
				t.Fatal("calendar observation suppressed independent public mail")
			}
			cal := sourceKeyed(t, rep, "outlook|Default")
			if variant == "calendar-empty" {
				if rep.Status != StatusOK || cal.Status != StatusOK || cal.Counts == nil || rep.Calendar.Events.Seen != 0 {
					t.Fatal("a new empty calendar was not a qualified empty observation")
				}
			} else if rep.Status != StatusOmissions || cal.Status != StatusOmissions || rep.Calendar.Events.Seen != 2 ||
				cal.Omissions[outlookcal.CodeEventUnmapped] != 1 || count(t, db, `select count(*) from calendar_source_events`) != 2 {
				t.Fatal("one damaged browser value erased good events or hid its omission")
			}
		})
	}
}
