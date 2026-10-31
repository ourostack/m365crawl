package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestOWAProfileRetainsSnapshotUntilReaderCloseIsProved(t *testing.T) {
	for _, lane := range []string{"mail", "calendar", "canceled-calendar"} {
		t.Run(lane, func(t *testing.T) {
			r, profile, _ := owaCalendarRunner(t, newDB(t))
			copySource, mailClose, calClose := outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose
			t.Cleanup(func() {
				outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose = copySource, mailClose, calClose
			})
			var snap string
			var release func()
			cleaned, closed := 0, 0
			outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
				var err error
				snap, release, err = copySource(ctx, source)
				return snap, func() { cleaned++; release() }, err
			}
			t.Cleanup(func() {
				// Both real closes succeed in this fixture; the injected error only tests the unknown outcome.
				if release != nil && closed == 2 && cleaned == 0 {
					release()
				}
			})
			outlookOWAClose = func(o *indexeddb.Origin) error {
				if err := mailClose(o); err != nil {
					return err
				}
				closed++
				if lane == "mail" {
					return errors.New("private close failure")
				}
				return nil
			}
			outlookOWACalendarClose = func(o *indexeddb.Origin) error {
				if err := calClose(o); err != nil {
					return err
				}
				closed++
				if lane != "mail" {
					return errors.New("private close failure")
				}
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if lane == "canceled-calendar" {
				outlookOWACalendarCollect = func(context.Context, *indexeddb.Origin, string, bool) (outlookowacal.Result, error) {
					cancel()
					return outlookowacal.Result{}, context.Canceled
				}
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(ctx, profile))
			if cleaned != 0 || closed != 2 {
				t.Fatal("snapshot was removed while reader close was unproved")
			}
			if _, err := os.Stat(snap); err != nil {
				t.Fatal("unproved snapshot payload was not retained")
			}
			failed := cal
			if lane == "mail" {
				failed = mail
			}
			if failed.err == nil || strings.Contains(failed.err.Error(), "private") {
				t.Fatal("reader close failure was hidden or exposed its raw cause")
			}
			if lane == "canceled-calendar" && !errors.Is(failed.err, context.Canceled) {
				t.Fatal("close failure replaced cancellation")
			}
		})
	}
}

func TestOWAProfileSharedFaultsFailOnlyNeededReads(t *testing.T) {
	for _, where := range []string{"fingerprint", "copy", "after-copy", "cancel-copy", "panic"} {
		t.Run(where, func(t *testing.T) {
			r, profile, _ := owaCalendarRunner(t, newDB(t))
			fingerprint, copySource := outlookOWAFingerprint, outlookOWAPrivateSnapshot
			t.Cleanup(func() { outlookOWAFingerprint, outlookOWAPrivateSnapshot = fingerprint, copySource })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch where {
			case "fingerprint", "after-copy", "panic":
				calls := 0
				outlookOWAFingerprint = func(p outlookowa.Profile) (string, time.Time, error) {
					calls++
					if where == "panic" {
						panic("private shared panic")
					}
					if where == "fingerprint" || calls == 2 {
						return "", time.Time{}, errors.New("private fingerprint")
					}
					return fingerprint(p)
				}
			case "copy":
				outlookOWAPrivateSnapshot = func(context.Context, teamsdesktop.Source) (string, func(), error) {
					return "", func() {}, errors.New("private copy")
				}
			case "cancel-copy":
				outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
					snap, cleanup, err := copySource(ctx, source)
					cancel()
					return snap, cleanup, err
				}
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(ctx, profile))
			if cal.err == nil || mail.err == nil || cal.decoded || mail.decoded ||
				strings.Contains(cal.err.Error(), "private") || strings.Contains(mail.err.Error(), "private") {
				t.Fatal("shared failure became success or exposed source data")
			}
		})
	}
}

func TestOWAProfileCalendarOmissionsAndUnstableCopyKeepMailIndependent(t *testing.T) {
	db := newDB(t)
	r, profile, _ := owaCalendarRunner(t, db)
	collect, fingerprint := outlookOWACalendarCollect, outlookOWAFingerprint
	t.Cleanup(func() { outlookOWAFingerprint = fingerprint })
	outlookOWACalendarCollect = func(ctx context.Context, o *indexeddb.Origin, account string, expect bool) (outlookowacal.Result, error) {
		res, err := collect(ctx, o, account, expect)
		res.Notes.UnmappedEvents, res.Notes.UnmappedFields, res.Notes.UnknownZone = 1, 1, 1
		res.Notes.AllDayUnaligned, res.Notes.PartialAttendees, res.Notes.DecodeFailures = 1, 1, 1
		res.Losses = []outlookcal.Loss{{Code: outlookcal.CodeEventUnmapped, Count: 6}}
		res.Doubtful = true
		return res, err
	}
	calls := 0
	outlookOWAFingerprint = func(p outlookowa.Profile) (string, time.Time, error) {
		fp, fresh, err := fingerprint(p)
		calls++
		if calls == 2 {
			fp += "-synthetic-change"
		}
		return fp, fresh, err
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || !mail.decoded || cal.report.Status != StatusOmissions ||
		cal.report.Omissions[outlookcal.CodeEventUnmapped] != 6 || cal.report.Omissions["outlook_owa_source_changed"] != 1 {
		t.Fatal("loss or instability was lost from the calendar outcome")
	}
	var raw string
	if err := openRaw(t, db).QueryRow(`select value from meta where key='outlook_read:outlook/Default'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var read store.OutlookRead
	if err := json.Unmarshal([]byte(raw), &read); err != nil || len(read.UnmappedValues) != 6 {
		t.Fatal("counts-only calendar census was not retained")
	}
}

func TestOWAProfileDatabaseFailuresAreNotSourceSuccess(t *testing.T) {
	for _, where := range []string{"initial", "mail-state", "calendar-state", "calendar-receipt", "checked", "attempt", "clear-mail", "skip", "calendar-commit", "calendar-failure"} {
		t.Run(where, func(t *testing.T) {
			db := newDB(t)
			r, profile, clock := owaCalendarRunner(t, db)
			query := ""
			switch where {
			case "initial":
				query = `drop table calendar_source_events`
			case "mail-state":
				query = `insert into meta(key,value) values('outlook_mail_failure:outlook/Default','{')`
			case "calendar-state":
				if err := r.st.RecordRun(context.Background(), store.Run{Source: outlookKey("Default"), Status: StatusOK, StartedAt: *clock, FinishedAt: *clock}); err != nil {
					t.Fatal(err)
				}
				query = `update sync_runs set omissions_json='{' where source='outlook|Default'`
			case "calendar-receipt":
				query = `drop table outlook_calendar_reads`
			case "checked":
				query = `create trigger owa_cal_fault before insert on meta when new.key='outlook_checked:Default' begin select raise(abort,'synthetic refusal'); end`
			case "attempt":
				query = `create trigger owa_cal_fault before insert on meta when new.key='outlook_last_attempt:Default' begin select raise(abort,'synthetic refusal'); end`
			case "clear-mail":
				query = `create trigger owa_cal_fault before delete on meta when old.key='outlook_mail_read:outlook/Default' begin select raise(abort,'synthetic refusal'); end`
				if err := r.st.SetMailRead(context.Background(), outlookAccount("Default"), *clock); err != nil {
					t.Fatal(err)
				}
			case "skip":
				owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
				r.o.OutlookMinReadInterval = time.Hour
				*clock = clock.Add(time.Second)
				query = `create trigger owa_cal_fault before insert on meta when new.key='outlook_skipped:Default' begin select raise(abort,'synthetic refusal'); end`
			case "calendar-commit":
				query = `create trigger owa_cal_fault before insert on calendar_source_events begin select raise(abort,'synthetic refusal'); end`
			case "calendar-failure":
				query = `create trigger owa_cal_fault before insert on meta when new.key='outlook_failure:outlook/Default' begin select raise(abort,'synthetic refusal'); end`
				outlookOWACalendarCollect = func(context.Context, *indexeddb.Origin, string, bool) (outlookowacal.Result, error) {
					return outlookowacal.Result{}, errors.New("private calendar failure")
				}
			}
			if _, err := openRaw(t, db).Exec(query); err != nil {
				t.Fatal(err)
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
			if cal.err == nil || cal.decoded {
				t.Fatal("calendar database failure became success")
			}
			if where != "calendar-commit" && where != "calendar-failure" && (mail.err == nil || mail.decoded) {
				t.Fatal("shared database failure became mail success")
			}
		})
	}
}

func TestOWACalendarSourceErrorsAreFixedAndWindowsSpecific(t *testing.T) {
	for _, input := range []error{
		context.Canceled, context.DeadlineExceeded,
		&hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: "owa_events_missing"},
		&errs.Coded{Code: outlookcal.CodeLayoutUnsupported, Message: "already public"},
		&errs.Coded{Code: CodeOutlookProfileUnreadable, Message: "already public"},
		errs.DBError(errors.New("private database")), errs.Internal(errors.New("private panic")),
		&errs.Coded{Code: errs.CodeSnapshotInconsistent, Exit: errs.ExitEnvironment, Message: "private snapshot"},
		&errs.Coded{Code: errs.CodeUnsupportedBlockCompression, Exit: errs.ExitEnvironment, Message: "private compression"},
		&errs.Coded{Code: "unknown", Message: "private unknown"}, errors.New("private source"),
	} {
		got := owaCalendarSourceError(input)
		if got == nil || strings.Contains(got.Error(), "private") || strings.Contains(got.Error(), "Full Disk Access") {
			t.Fatal("calendar error exposed source data or macOS permission guidance")
		}
	}
}

func TestOWACollectorsCanceledBeforeOpenOwnNoReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mailClosed, calClosed := true, true
	_, mailErr := collectOWAMailboxes(ctx, "unused", &mailClosed)
	_, calErr := collectOWACalendar(ctx, "unused", "outlook/Default", false, &calClosed)
	if !errors.Is(mailErr, context.Canceled) || !errors.Is(calErr, context.Canceled) || !mailClosed || !calClosed {
		t.Fatal("an unstarted read acquired ownership or lost cancellation")
	}
}

func TestOWAProfileCalendarOpeningFailureTracksReleaseProof(t *testing.T) {
	for _, mode := range []string{"error", "close-unproved", "panic"} {
		t.Run(mode, func(t *testing.T) {
			r, profile, _ := owaCalendarRunner(t, newDB(t))
			open, copySource := outlookOWACalendarOpen, outlookOWAPrivateSnapshot
			t.Cleanup(func() { outlookOWACalendarOpen, outlookOWAPrivateSnapshot = open, copySource })
			var release func()
			cleaned := 0
			outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
				snap, cleanup, err := copySource(ctx, source)
				release = cleanup
				return snap, func() { cleaned++; cleanup() }, err
			}
			t.Cleanup(func() {
				// This fixture opens no calendar reader; its injected unknown result can now be independently released.
				if release != nil && cleaned == 0 {
					release()
				}
			})
			outlookOWACalendarOpen = func(context.Context, string, string) (*indexeddb.Origin, error) {
				switch mode {
				case "close-unproved":
					return nil, errors.Join(context.Canceled, outlookowacal.ErrReaderClose)
				case "panic":
					panic("private calendar opening panic")
				}
				return nil, errors.New("private opening failure")
			}
			cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
			if cal.err == nil || mail.err != nil || cal.decoded || !mail.decoded || strings.Contains(cal.err.Error(), "private") {
				t.Fatal("calendar opening failure suppressed mail or exposed its cause")
			}
			wantCleaned := 0
			if mode == "error" {
				wantCleaned = 1
			}
			if cleaned != wantCleaned {
				t.Fatal("snapshot disposal did not follow actual reader-release proof")
			}
		})
	}
}

func TestOWAProfileCoreRefusalIsAnExplicitOmission(t *testing.T) {
	r, profile, _ := owaCalendarRunner(t, newDB(t))
	collect := outlookOWACalendarCollect
	outlookOWACalendarCollect = func(ctx context.Context, o *indexeddb.Origin, account string, expect bool) (outlookowacal.Result, error) {
		res, err := collect(ctx, o, account, expect)
		res.Events[0].UnknownDeclared = false
		return res, err
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || cal.report.Status != StatusOmissions || cal.report.Omissions[store.OmitCalendarRefused] != 1 {
		t.Fatalf("core refusal: cal=%+v mail=%+v", cal, mail)
	}
}

func TestOWADefaultRootRetainsLegacyFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	defaultRoot := outlookOWADefaultRoot
	t.Cleanup(func() { outlookOWADefaultRoot = defaultRoot })
	outlookOWADefaultRoot = func() (string, error) { return "", outlookdesktop.ErrNotSupported }
	want, wantErr := outlookdesktop.DefaultRoot()
	got, err := defaultOutlookRoot()
	if got != want || (err == nil) != (wantErr == nil) || err != nil && err.Error() != wantErr.Error() {
		t.Fatalf("legacy default-root fallback: got=%q err=%v want=%q err=%v", got, err, want, wantErr)
	}
}
