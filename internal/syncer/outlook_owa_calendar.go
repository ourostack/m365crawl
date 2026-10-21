package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/outlookowacal"
	"github.com/ourostack/m365crawl/internal/store"
)

const outlookOWACalendarMapperVersion = 1

var (
	outlookOWACalendarOpen    = outlookowacal.Open
	outlookOWACalendarClose   = (*indexeddb.Origin).Close
	outlookOWACalendarCollect = outlookowacal.Collect
)

func (r *runner) outlookOWAProfile(ctx context.Context, p outlookowa.Profile) []outlookOutcome {
	mail := outlookOutcome{key: mailKey(outlookKey(p.Name))}
	cal := outlookOutcome{key: outlookKey(p.Name)}
	if err := contained(func() error {
		mail, cal = r.readOWAProfile(ctx, p, true)
		return nil
	}); err != nil {
		mail.err, cal.err = err, err
	}
	if mail.err != nil {
		mail.err = owaSourceError(mail.err)
		r.rememberOWAFailure(ctx, p.Name, mail.err)
	}
	if cal.err != nil {
		cal.err = owaCalendarSourceError(cal.err)
		if ctx.Err() == nil {
			c := codedOf(cal.err)
			if err := r.st.SetOutlookFailure(ctx, outlookAccount(p.Name), &store.OutlookFailure{Code: c.Code, Message: c.Message, Fix: c.Fix, Exit: c.Exit}); err != nil {
				r.progress("%s: could not record the calendar failure", cal.key)
			}
		}
	}
	return []outlookOutcome{cal, mail}
}

func collectOWACalendar(ctx context.Context, snap, account string, expect bool, closed *bool) (res outlookowacal.Result, err error) {
	if err := ctx.Err(); err != nil {
		return res, err
	}
	*closed = false
	o, err := outlookOWACalendarOpen(ctx, filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob"))
	if err != nil {
		*closed = !errors.Is(err, outlookowacal.ErrReaderClose)
		return res, err
	}
	defer func() {
		if closeErr := contained(func() error { return outlookOWACalendarClose(o) }); closeErr != nil {
			var coded *errs.Coded
			if err == nil && errors.As(closeErr, &coded) && coded.Code == errs.CodeInternal {
				err = errs.Internal(errors.New("outlook calendar reader release failed"))
			}
			err = errors.Join(err, outlookowacal.ErrReaderClose)
		} else {
			*closed = true
		}
	}()
	return outlookOWACalendarCollect(ctx, o, account, expect)
}

func (r *runner) commitOWACalendarSnapshot(ctx context.Context, snap, account, key, fp, origin string, begun, fresh time.Time, stable, expect bool, closed *bool) (SourceReport, bool, error) {
	res, err := collectOWACalendar(ctx, snap, account, expect, closed)
	if err != nil {
		return SourceReport{}, false, err
	}
	omissions := map[string]int{}
	for _, loss := range res.Losses {
		omissions[loss.Code] += loss.Count
	}
	if !stable {
		omissions["outlook_owa_source_changed"]++
		fp = ""
	}
	read := store.OutlookRead{UnmappedValues: map[string]int{}}
	for key, count := range map[string]int{
		"owa_event": res.Notes.UnmappedEvents, "owa_value": res.Notes.UnmappedFields,
		"owa_zone": res.Notes.UnknownZone, "owa_all_day": res.Notes.AllDayUnaligned,
		"owa_partial_attendees": res.Notes.PartialAttendees, "owa_decode": res.Notes.DecodeFailures,
	} {
		if count > 0 {
			read.UnmappedValues[key] = count
		}
	}
	status := StatusOK
	zone := calendarZone()
	cal, err := r.st.CommitOutlook(ctx, store.OutlookBatch{
		Account: account, Origin: origin, Events: res.Events, FreshAt: fresh, At: begun, Zone: zone,
		Stamp: store.OutlookStamp(outlookOWACalendarMapperVersion, zone), Read: read,
		PreserveIdentity: true, InferGone: false,
	}, func(cal store.CalendarResult) store.Run {
		if cal.Counts.Refused > 0 {
			omissions[store.OmitCalendarRefused] += cal.Counts.Refused
		}
		if lost(omissions) > 0 {
			status = StatusOmissions
		}
		return store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Counts: runCounts{Calendar: cal.Counts}, Omissions: omissions}
	})
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if len(omissions) == 0 {
		omissions = nil
	}
	r.progress("%s: %s (%d events)", key, status, len(res.Events))
	return SourceReport{Source: key, Status: status, Omissions: omissions, Accounts: []string{account}, Counts: &SourceCounts{Calendar: cal.Counts}}, true, nil
}

func owaCalendarSourceError(err error) (result error) {
	defer func() {
		if errors.Is(err, ErrOWASnapshotRetained) {
			result = owaRetainedError(result)
		}
	}()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var guard *hxstore.GuardError
	if errors.As(err, &guard) {
		return &errs.Coded{Code: guard.Code, Exit: errs.ExitEnvironment, Message: "the Outlook calendar layout is not supported: " + guard.Detail,
			Fix: "Update m365crawl; only one supported OWA calendar database per profile is read. Nothing of this calendar read was applied."}
	}
	var coded *errs.Coded
	if errors.As(err, &coded) {
		switch coded.Code {
		case outlookcal.CodeLayoutUnsupported, CodeOutlookProfileUnreadable:
			return coded
		case errs.CodeDBError:
			return errs.DBError(errors.New("outlook calendar archive operation failed"))
		case errs.CodeInternal:
			return errs.Internal(errors.New("outlook calendar source operation failed"))
		case errs.CodeSnapshotInconsistent, errs.CodeUnsupportedBlockCompression:
			return &errs.Coded{Code: coded.Code, Exit: coded.Exit, Message: "the Outlook calendar snapshot could not be read consistently",
				Fix: "Wait for Outlook to finish syncing, then run again; update m365crawl if it persists."}
		}
	}
	return &errs.Coded{Code: CodeOutlookProfileUnreadable, Exit: errs.ExitEnvironment, Message: "the Outlook calendar profile could not be read",
		Fix: "Check that the Outlook profile is readable, then run again."}
}
