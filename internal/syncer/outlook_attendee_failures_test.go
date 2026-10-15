package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
)

func TestAttendeeIndependentOptionalFailures(t *testing.T) {
	for _, failure := range []string{"people", "native", "cleanup", "cancel_after_cleanup"} {
		t.Run(failure, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			root, db := attendeeTestRoot(t, true), newDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			oldPeople, oldNative, oldSnapshot := peopleCollect, attendeeCollect, outlookSnapshot
			t.Cleanup(func() { peopleCollect, attendeeCollect, outlookSnapshot = oldPeople, oldNative, oldSnapshot })
			peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
				return outlookpeople.Result{People: []outlookpeople.Person{{Class: 0xd2, Key: 1, Email: "synthetic@example.invalid"}}}, "synthetic-people", nil
			}
			if _, err := SyncPeople(ctx, PeopleOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil {
				t.Fatal(err)
			}
			if r, err := SyncAttendees(ctx, AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err != nil || r.Captured != 1 {
				t.Fatal(r, err)
			}
			if failure == "people" {
				peopleCollect = func(context.Context, outlookdesktop.Info) (outlookpeople.Result, string, error) {
					return outlookpeople.Result{}, "", errors.New("synthetic people failure")
				}
			}
			if failure == "native" {
				attendeeCollect = func(context.Context, outlookdesktop.Info, *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
					return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", errors.New("synthetic native failure")
				}
			}
			outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
				info, cleanup, err := oldSnapshot(ctx, path)
				return info, func() {
					cleanup()
					if failure == "cleanup" {
						if err := os.MkdirAll(info.Path, 0o700); err != nil {
							t.Error(err)
						}
					}
					if failure == "cancel_after_cleanup" {
						cancel()
					}
				}, err
			}
			opts := outlookOpts(db, root)
			opts.FullRead = true
			rep, _, _ := Run(ctx, opts)
			members := count(t, db, `select count(*) from outlook_attendee_members`)
			switch failure {
			case "people":
				if members != 2 || sourceKeyed(t, rep, "outlook|Main|attendees").Status == "failed" {
					t.Fatal("people failure blocked native", rep)
				}
			case "native":
				if members != 1 || sourceKeyed(t, rep, "outlook|Main|people").Status == "failed" {
					t.Fatal("native failure blocked people", rep)
				}
			default:
				if members != 1 {
					t.Fatal("common cleanup/cancel admitted native")
				}
				if count(t, db, `select count(*) from people_capture_members`) != 1 {
					t.Fatal("common cleanup/cancel admitted people")
				}
			}
		})
	}
}

func TestAttendeeReaderCloseRefusal(t *testing.T) {
	for _, lane := range []string{"calendar", "native"} {
		t.Run(lane, func(t *testing.T) {
			isolateTmp(t)
			utcDays(t)
			root, db := attendeeTestRoot(t, false), newDB(t)
			oldCalendar, oldNative := outlookCalendarClose, attendeeClose
			t.Cleanup(func() { outlookCalendarClose, attendeeClose = oldCalendar, oldNative })
			if lane == "calendar" {
				outlookCalendarClose = func(f *os.File) error { return errors.Join(f.Close(), errors.New("synthetic close failure")) }
			}
			if lane == "native" {
				attendeeClose = func(s *hxstore.Store) error { return errors.Join(s.Close(), errors.New("synthetic close failure")) }
			}
			rep, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
			if err == nil || rep.Failed != 1 || count(t, db, `select count(*) from outlook_attendee_members`) != 0 {
				t.Fatal("close failure produced native association", rep, err)
			}
			if lane == "calendar" && count(t, db, `select count(*) from outlook_calendar_reads`) != 0 {
				t.Fatal("calendar close failure emitted successful decode receipt")
			}
		})
	}
}

func TestAttendeeLocalTimeoutCauseAndPrivacy(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	old := attendeeCollect
	t.Cleanup(func() { attendeeCollect = old })
	attendeeCollect = func(context.Context, outlookdesktop.Info, *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", errors.Join(context.DeadlineExceeded, errors.New("SYNTHETIC-PRIVATE-NATIVE-PATH"))
	}
	_, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lane-local timeout lost terminal cause", err)
	}
	if strings.Contains(err.Error(), "SYNTHETIC-PRIVATE-NATIVE-PATH") {
		t.Fatal("public failure exposed native text")
	}
}
