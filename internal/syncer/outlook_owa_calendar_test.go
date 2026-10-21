package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/v8"
)

func owaCalendarRunner(t *testing.T, db string) (*runner, outlookowa.Profile, *time.Time) {
	t.Helper()
	clock, collect := outlookNow, outlookOWACalendarCollect
	t.Cleanup(func() { outlookNow, outlookOWACalendarCollect = clock, collect })
	now := time.Date(2031, 3, 6, 12, 0, 0, 0, time.UTC)
	outlookNow = func() time.Time { return now }
	outlookOWACalendarCollect = func(_ context.Context, _ *indexeddb.Origin, account string, _ bool) (outlookowacal.Result, error) {
		o := &v8.Object{
			Keys:   []string{"UID", "Start", "End", "LastModifiedTime", "Subject", "IsAllDayEvent", "IsCancelled"},
			Values: []any{"AA11", "2031-03-05T09:30:00Z", "2031-03-05T10:00:00Z", "2031-03-04T12:00:00Z", "Synthetic OWA calendar", false, false},
		}
		e, notes, err := outlookowacal.MapEvent(account, o)
		if err != nil || notes != (outlookowacal.Notes{}) {
			t.Fatal("invalid synthetic calendar event")
		}
		return outlookowacal.Result{Events: []calendar.Event{e}, SeenIDs: []string{e.SourceID}, Notes: outlookowacal.CollectNotes{Records: 1, Events: 1}}, nil
	}
	st, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	return &runner{st: st, o: Options{OutlookMinReadInterval: -1}}, owaProfile(owaRoot(t, "mail")), &now
}

func owaKinds(t *testing.T, outcomes []outlookOutcome) (cal, mail outlookOutcome) {
	t.Helper()
	if len(outcomes) != 2 {
		t.Fatalf("want independent calendar/mail outcomes, got %d", len(outcomes))
	}
	for _, outcome := range outcomes {
		switch outcome.key {
		case outlookKey("Default"):
			cal = outcome
		case mailKey(outlookKey("Default")):
			mail = outcome
		default:
			t.Fatal("unexpected OWA source key")
		}
	}
	return cal, mail
}

func TestOWAProfileOneCopyIndependentReceiptsAndUnchanged(t *testing.T) {
	r, profile, clock := owaCalendarRunner(t, newDB(t))
	copySource, mailClose, calClose := outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose
	t.Cleanup(func() {
		outlookOWAPrivateSnapshot, outlookOWAClose, outlookOWACalendarClose = copySource, mailClose, calClose
	})
	copies, closed := 0, 0
	outlookOWAClose = func(o *indexeddb.Origin) error { closed++; return mailClose(o) }
	outlookOWACalendarClose = func(o *indexeddb.Origin) error { closed++; return calClose(o) }
	outlookOWAPrivateSnapshot = func(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
		copies++
		snap, cleanup, err := copySource(ctx, source)
		return snap, func() {
			if closed != 2 {
				t.Error("snapshot cleanup ran before both origins closed")
			}
			cleanup()
		}, err
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || !mail.decoded || copies != 1 || closed != 2 ||
		cal.report.Counts == nil || cal.report.Counts.Calendar.Events.Seen != 1 || mail.report.Counts == nil || mail.report.Counts.Mail.Added != 5 {
		t.Fatalf("shared copy or outcomes: cal=%+v mail=%+v copies=%d closed=%d", cal, mail, copies, closed)
	}
	state, err := r.st.OutlookState(context.Background(), "Default", outlookKey("Default"), outlookAccount("Default"))
	if err != nil || state.IdentityRead || !state.HoldsEvents {
		t.Fatal("calendar receipt invented identity or lost its event")
	}
	read, err := r.st.OutlookReadReceipt(context.Background(), outlookAccount("Default"))
	if err != nil || read.ID == 0 {
		t.Fatal("calendar commit lacked its independent receipt")
	}
	*clock = clock.Add(time.Minute)
	cal, mail = owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || cal.decoded || mail.decoded || cal.report.Status != StatusUnchanged || mail.report.Status != StatusUnchanged || copies != 1 {
		t.Fatal("one lane's marker suppressed the other or forced another copy")
	}
}

func TestOWAProfileCalendarFailureDoesNotSuppressMailAndRetries(t *testing.T) {
	r, profile, clock := owaCalendarRunner(t, newDB(t))
	collect := outlookOWACalendarCollect
	attempts := 0
	outlookOWACalendarCollect = func(ctx context.Context, o *indexeddb.Origin, account string, expect bool) (outlookowacal.Result, error) {
		attempts++
		if attempts == 1 {
			return outlookowacal.Result{}, errors.New("private calendar parser failure")
		}
		return collect(ctx, o, account, expect)
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err == nil || mail.err != nil || !mail.decoded || cal.decoded || attempts != 1 {
		t.Fatal("calendar failure suppressed the valid mail read")
	}
	failed, err := r.st.OutlookState(context.Background(), "Default", outlookKey("Default"), outlookAccount("Default"))
	if err != nil || failed.Failure == nil {
		t.Fatal("calendar failure was not retained independently")
	}
	*clock = clock.Add(time.Minute)
	cal, mail = owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || !cal.decoded || mail.decoded || mail.report.Status != StatusUnchanged || attempts != 2 {
		t.Fatal("failed calendar was not retried independently")
	}
	ms, err := r.st.MailState(context.Background(), outlookAccount("Default"))
	if err != nil || !ms.Read || ms.Failure != nil {
		t.Fatal("calendar retry invalidated mail's good-read marker")
	}
}

func TestOWAProfileMailFailureDoesNotSuppressCalendarAndRetries(t *testing.T) {
	r, profile, clock := owaCalendarRunner(t, newDB(t))
	open := outlookOWAOpen
	t.Cleanup(func() { outlookOWAOpen = open })
	attempts := 0
	outlookOWAOpen = func(ctx context.Context, dir, blob string) (*indexeddb.Origin, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("private mail open failure")
		}
		return open(ctx, dir, blob)
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err == nil || !cal.decoded || mail.decoded {
		t.Fatal("mail failure suppressed the valid calendar read")
	}
	*clock = clock.Add(time.Minute)
	cal, mail = owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err != nil || mail.err != nil || cal.decoded || !mail.decoded || cal.report.Status != StatusUnchanged {
		t.Fatal("failed mail was not retried without rereading calendar")
	}
}

func TestOWAProfileIntervalReplaysOnlyTheFailedLane(t *testing.T) {
	r, profile, clock := owaCalendarRunner(t, newDB(t))
	owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if err := r.st.SetOutlookFailure(context.Background(), outlookAccount("Default"), &store.OutlookFailure{Code: "outlook_layout_unsupported", Message: "synthetic calendar failure", Fix: "synthetic fix", Exit: errs.ExitEnvironment}); err != nil {
		t.Fatal(err)
	}
	r.o.OutlookMinReadInterval = time.Hour
	*clock = clock.Add(time.Second)
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err == nil || codedOf(cal.err).Code != "outlook_layout_unsupported" || mail.err != nil || mail.report.Status != StatusSkippedInterval ||
		mail.report.NextReadAfter == nil || cal.decoded || mail.decoded {
		t.Fatal("interval skip erased an independent failure")
	}
}

func TestOWAProfileUnknownIdentityDoesNotReplaceOldIdentityOrBindNewData(t *testing.T) {
	r, profile, _ := owaCalendarRunner(t, newDB(t))
	account := outlookAccount("Default")
	_, err := r.st.CommitOutlook(context.Background(), store.OutlookBatch{
		Account: account, At: outlookNow(), FreshAt: outlookNow(), Zone: time.UTC,
		Stamp: store.OutlookStamp(1, time.UTC), Addresses: []string{"known@example.invalid"},
	}, func(store.CalendarResult) store.Run {
		return store.Run{Source: outlookKey("Default"), Status: StatusOK, StartedAt: outlookNow(), FinishedAt: outlookNow()}
	})
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	outlookOWACalendarCollect = func(context.Context, *indexeddb.Origin, string, bool) (outlookowacal.Result, error) {
		calls++
		return outlookowacal.Result{}, errors.New("must not read calendar without current identity")
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err == nil || mail.err != nil || !mail.decoded || cal.decoded || calls != 0 {
		t.Fatal("unknown identity admitted data under an old binding or suppressed mail")
	}
	state, err := r.st.OutlookState(context.Background(), "Default", outlookKey("Default"), account)
	if err != nil || !state.IdentityRead || state.HoldsEvents {
		t.Fatal("refusal changed the old identity or imported unqualified calendar data")
	}
}

func TestOWAProfileDoesNotOverwriteCalendarFromAnotherOrigin(t *testing.T) {
	r, profile, _ := owaCalendarRunner(t, newDB(t))
	account := outlookAccount("Default")
	res, err := outlookOWACalendarCollect(context.Background(), nil, account, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.st.CommitOutlook(context.Background(), store.OutlookBatch{
		Account: account, Origin: "different-synthetic-source", Events: res.Events, At: outlookNow(), FreshAt: outlookNow(),
		Zone: time.UTC, Stamp: store.OutlookStamp(1, time.UTC), PreserveIdentity: true,
	}, func(store.CalendarResult) store.Run {
		return store.Run{Source: outlookKey("Default"), Status: StatusOK, StartedAt: outlookNow(), FinishedAt: outlookNow()}
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	outlookOWACalendarCollect = func(context.Context, *indexeddb.Origin, string, bool) (outlookowacal.Result, error) {
		calls++
		return res, nil
	}
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err == nil || mail.err != nil || !mail.decoded || cal.decoded || calls != 0 {
		t.Fatal("unverified calendar history was overwritten or suppressed mail")
	}
}

func TestOWAProfileCalendarRequiresKnownOriginWithoutSuppressingMail(t *testing.T) {
	r, profile, _ := owaCalendarRunner(t, newDB(t))
	resolve := peopleResolve
	t.Cleanup(func() { peopleResolve = resolve })
	peopleResolve = func(string) (string, error) { return "", errors.New("private origin failure") }
	cal, mail := owaKinds(t, r.outlookOWAProfile(context.Background(), profile))
	if cal.err == nil || mail.err != nil || cal.decoded || !mail.decoded {
		t.Fatal("unknown calendar origin imported data or suppressed independent mail")
	}
}
