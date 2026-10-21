package syncer

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/indexeddb"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func defaultOutlookRoot() (string, error) {
	root, err := outlookOWADefaultRoot()
	if errors.Is(err, outlookdesktop.ErrNotSupported) {
		return outlookdesktop.DefaultRoot()
	}
	return root, err
}

// Explicit copied OWA roots work everywhere; explicit HxStore roots keep the old route.
func (r *runner) outlookOWARoute(root string) (bool, error) {
	if r.o.OutlookRoot == "" {
		return runtime.GOOS == "windows", nil
	}
	// Only positively recognized legacy stores select that route; skips/errors cannot hide OWA.
	hx, classic, _, _ := outlookDiscover(root)
	if len(hx)+len(classic) > 0 {
		return false, nil
	}
	profiles, skipped, err := outlookOWADiscover(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, owaSourceError(err)
	}
	return len(profiles)+len(skipped) > 0, nil
}

func (r *runner) outlookOWASources(ctx context.Context, rep *Report) []outlookOutcome {
	root := r.o.OutlookRoot
	if root == "" {
		var err error
		root, err = outlookDefaultRoot()
		if err != nil {
			return []outlookOutcome{{key: "outlook", err: owaSourceError(err)}}
		}
	}
	profiles, skipped, err := outlookOWADiscover(root)
	if err != nil {
		if r.o.OutlookImplicit && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []outlookOutcome{{key: "outlook", err: owaSourceError(err)}}
	}
	var out []outlookOutcome
	for _, sp := range skipped {
		cal := outlookOutcome{key: outlookKey(sp.Name), err: owaCalendarSourceError(sp.Err)}
		r.rememberOWACalendarFailure(ctx, sp.Name, cal.err)
		o := outlookOutcome{key: mailKey(outlookKey(sp.Name)), err: owaSourceError(sp.Err)}
		r.rememberOWAFailure(ctx, sp.Name, o.err)
		out = append(out, cal, o)
	}
	if len(profiles)+len(skipped) == 0 && !r.o.OutlookImplicit {
		return []outlookOutcome{{key: "outlook", err: &errs.Coded{Code: CodeNoOutlookProfiles, Exit: errs.ExitEnvironment,
			Message: "no new Outlook mail profile found", Fix: "Point --outlook-root at an EBWebView directory containing Outlook IndexedDB profiles."}}}
	}
	for _, p := range profiles {
		if ctx.Err() != nil {
			out = append(out, outlookOutcome{key: outlookKey(p.Name), err: ctx.Err()},
				outlookOutcome{key: mailKey(outlookKey(p.Name)), err: ctx.Err()})
			break
		}
		for _, o := range r.outlookOWAProfile(ctx, p) {
			if o.key == outlookKey(p.Name) && o.report.Counts != nil {
				rep.Calendar.Add(o.report.Counts.Calendar)
			}
			out = append(out, o)
		}
	}
	return out
}

func (r *runner) rememberOWAFailure(ctx context.Context, profile string, err error) {
	if ctx.Err() != nil {
		return
	}
	c := codedOf(err)
	if err := r.st.SetMailFailure(ctx, outlookAccount(profile), store.OutlookFailure{Code: c.Code, Message: c.Message, Fix: c.Fix, Exit: c.Exit}); err != nil {
		r.progress("%s: could not record the mail failure", mailKey(outlookKey(profile)))
	}
}

// ErrOWASnapshotRetained reports a private snapshot whose reader release was not proved.
var ErrOWASnapshotRetained = errors.New("reader release was not proved and its snapshot was retained")

func (r *runner) outlookOWAMail(ctx context.Context, p outlookowa.Profile) (SourceReport, bool, error) {
	mail, _ := r.readOWAProfile(ctx, p, false)
	return mail.report, mail.decoded, mail.err
}

// readOWAProfile owns the single attempt check and copy; each requested lane keeps its own receipt.
func (r *runner) readOWAProfile(ctx context.Context, p outlookowa.Profile, withCalendar bool) (mail, cal outlookOutcome) {
	begun := outlookNow()
	key, account := mailKey(outlookKey(p.Name)), outlookAccount(p.Name)
	mail.key, cal.key = key, outlookKey(p.Name)
	fail := func(err error) (outlookOutcome, outlookOutcome) {
		mail.err = err
		if withCalendar {
			cal.err = err
		}
		return mail, cal
	}
	prior, err := r.st.OutlookState(ctx, p.Name, key, account)
	if err != nil {
		return fail(errs.DBError(err))
	}
	mailState, err := r.st.MailState(ctx, account)
	if err != nil {
		return fail(errs.DBError(err))
	}
	var calPrior store.OutlookState
	var receipt store.OutlookReadReceipt
	origin := ""
	if withCalendar {
		calPrior, err = r.st.OutlookState(ctx, p.Name, cal.key, account)
		if err != nil {
			return fail(errs.DBError(err))
		}
		receipt, err = r.st.OutlookReadReceipt(ctx, account)
		if err != nil {
			return fail(errs.DBError(err))
		}
		origin = outlookSourceOrigin(p.Source.LevelDBDir)
	}
	if gap := r.o.OutlookMinReadInterval; gap >= 0 {
		if gap == 0 {
			gap = OutlookMinReadInterval
		}
		if next := prior.LastAttempt.Add(gap); begun.Before(next) && !prior.LastAttempt.After(begun) {
			if f := mailState.Failure; f != nil {
				mail.err = &errs.Coded{Code: f.Code, Message: f.Message, Fix: f.Fix, Exit: f.Exit}
			} else {
				mail.report = SourceReport{Source: key, Status: StatusSkippedInterval, Omissions: prior.Omissions, NextReadAfter: &next}
			}
			if withCalendar {
				if f := calPrior.Failure; f != nil {
					cal.err = &errs.Coded{Code: f.Code, Message: f.Message, Fix: f.Fix, Exit: f.Exit}
				} else {
					cal.report = SourceReport{Source: cal.key, Status: StatusSkippedInterval, Omissions: calPrior.Omissions, NextReadAfter: &next}
				}
			}
			if mail.err == nil || withCalendar && cal.err == nil {
				if err := r.st.SetOutlookSkipped(ctx, p.Name); err != nil {
					return fail(errs.DBError(err))
				}
			}
			return mail, cal
		}
	}
	if err := r.st.SetOutlookChecked(ctx, p.Name, begun); err != nil {
		return fail(errs.DBError(err))
	}
	fp, fresh, err := outlookOWAFingerprint(p)
	if err != nil {
		return fail(err)
	}
	// IdentityRead is a calendar marker. OWA mail uses its own read/failure marker only.
	needMail := fp != prior.Fingerprint || r.o.FullRead || !mailState.Read || mailState.Failure != nil
	needCalendar := false
	if withCalendar {
		switch {
		case origin == "":
			cal.err = &hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: "owa_origin_unverified"}
		case calPrior.IdentityRead:
			// Until current sign-in identity is established, do not put new data under an old automatic binding.
			cal.err = &hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: "owa_identity_unverified"}
		case calPrior.HoldsEvents && (receipt.ID == 0 || receipt.Origin != origin):
			cal.err = &hxstore.GuardError{Code: outlookcal.CodeLayoutUnsupported, Detail: "owa_history_unverified"}
		default:
			stamp := store.OutlookStamp(outlookOWACalendarMapperVersion, calendarZone())
			needCalendar = r.o.FullRead || fp != calPrior.Fingerprint || calPrior.Failure != nil || receipt.ID == 0 ||
				origin == "" || receipt.Origin != origin || receipt.MapperStamp != stamp
		}
	}
	if !needMail {
		mail.report, mail.err = r.owaUnchanged(ctx, key, prior, begun, fp)
	}
	if withCalendar && !needCalendar && cal.err == nil {
		cal.report, cal.err = r.owaUnchanged(ctx, cal.key, calPrior, begun, fp)
	}
	if !needMail && !needCalendar {
		return mail, cal
	}
	if err := r.st.SetOutlookLastAttempt(ctx, p.Name, begun); err != nil {
		return fail(errs.DBError(err))
	}
	if needMail {
		if err := r.st.ClearMailRead(ctx, account); err != nil {
			return fail(errs.DBError(err))
		}
	}
	failNeeded := func(err error) (outlookOutcome, outlookOutcome) {
		if needMail {
			mail.err = err
		}
		if needCalendar {
			cal.err = err
		}
		return mail, cal
	}
	mailClosed, calendarClosed := true, true
	snap, cleanup, err := outlookOWAPrivateSnapshot(ctx, p.Source)
	defer func() {
		if mailClosed && calendarClosed {
			cleanup()
			return
		}
		if !mailClosed {
			mail.err = errors.Join(mail.err, ErrOWASnapshotRetained)
		}
		if !calendarClosed {
			cal.err = errors.Join(cal.err, ErrOWASnapshotRetained)
		}
		r.progress("%s: snapshot retained because reader release was not proved", outlookKey(p.Name))
	}()
	if err != nil {
		return failNeeded(err)
	}
	if err := ctx.Err(); err != nil {
		return failNeeded(err)
	}
	afterFP, afterFresh, err := outlookOWAFingerprint(p)
	if err != nil {
		return failNeeded(err)
	}
	stable := fp == afterFP && fresh.Equal(afterFresh)
	if needMail {
		mail.err = contained(func() (err error) {
			mail.report, mail.decoded, err = r.commitOWAMailSnapshot(ctx, snap, account, key, fp, begun, fresh, stable, &mailClosed)
			return err
		})
	}
	if needCalendar {
		cal.err = contained(func() (err error) {
			cal.report, cal.decoded, err = r.commitOWACalendarSnapshot(ctx, snap, account, cal.key, fp, origin, begun, fresh, stable, calPrior.HoldsEvents, &calendarClosed)
			return err
		})
	}
	return mail, cal
}

func (r *runner) owaUnchanged(ctx context.Context, key string, prior store.OutlookState, begun time.Time, fp string) (SourceReport, error) {
	status := StatusUnchanged
	if lost(prior.Omissions) > 0 {
		status = StatusOmissions
	}
	if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Omissions: prior.Omissions}); err != nil {
		return SourceReport{}, errs.DBError(err)
	}
	return SourceReport{Source: key, Status: status, Omissions: prior.Omissions}, nil
}

func (r *runner) commitOWAMailSnapshot(ctx context.Context, snap, account, key, fp string, begun, fresh time.Time, stable bool, closed *bool) (SourceReport, bool, error) {
	expect, err := r.st.MailHolds(ctx, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	res, err := collectOWAMail(ctx, snap, account, expect, closed)
	if err != nil {
		return SourceReport{}, false, err
	}
	omissions := map[string]int{}
	for _, loss := range res.Losses {
		omissions[loss.Code] += loss.Count
	}
	if !stable {
		omissions["outlook_owa_source_changed"]++
		// A successful but uncertain read cannot vouch for either observed fingerprint.
		fp = ""
	}
	status := StatusOK
	if lost(omissions) > 0 {
		status = StatusOmissions
	} else {
		omissions = nil
	}
	mail, err := r.st.CommitOWAMail(ctx, store.OWAMailBatch{Account: account, ReadAt: begun, FreshAt: fresh, Result: res, Trusted: stable})
	if err != nil {
		var coded *errs.Coded
		if errors.As(err, &coded) && coded.Code == "outlook_mail_key_exhausted" {
			return SourceReport{}, false, owaSourceError(coded)
		}
		return SourceReport{}, false, errs.DBError(err)
	}
	if err := outlookOWARecordRun(ctx, r.st, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Counts: mailRunCounts{Mail: mail}, Omissions: omissions}); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	// Vouch for this read last: cancellation while recording its receipt must leave it retryable.
	if err := r.st.SetMailRead(ctx, account, begun); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	r.progress("%s: %s (%d messages)", key, status, len(res.Messages))
	return SourceReport{Source: key, Status: status, Omissions: omissions, Accounts: []string{account}, Counts: &SourceCounts{Mail: &mail}}, true, nil
}

// Copy cancellation follows the caller until handoff; the original reader context still cancels reads/deadlines, but this owner removes the snapshot only after Close.
func ownedOWASnapshot(ctx context.Context, source teamsdesktop.Source) (string, func(), error) {
	copyCtx, cancelCopy := context.WithCancel(context.WithoutCancel(ctx))
	forwarded := make(chan struct{})
	stopForwarding := context.AfterFunc(ctx, func() {
		cancelCopy()
		close(forwarded)
	})
	var stopped sync.Once
	stop := func() {
		stopped.Do(func() {
			if !stopForwarding() {
				<-forwarded
			}
		})
	}
	handedOff := false
	defer func() {
		stop()
		if !handedOff {
			cancelCopy()
		}
	}()
	snap, cleanup, err := outlookOWASnapshot(copyCtx, source)
	stop()
	if err := ctx.Err(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err != nil {
		return snap, cleanup, err
	}
	handedOff = true
	return snap, func() {
		cleanup()
		cancelCopy()
	}, nil
}

// released is true only when no reader was acquired or its Close succeeded.
func collectOWAMail(ctx context.Context, snap, account string, expect bool, released *bool) (res outlookowa.Result, err error) {
	if err := ctx.Err(); err != nil {
		return res, err
	}
	*released = false
	o, err := outlookOWAOpen(ctx, filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob"))
	if err != nil {
		*released = !errors.Is(err, outlookowa.ErrReaderClose)
		return res, err
	}
	defer func() {
		if closeErr := contained(func() error { return outlookOWAClose(o) }); closeErr != nil {
			var coded *errs.Coded
			if err == nil && errors.As(closeErr, &coded) && coded.Code == errs.CodeInternal {
				err = errs.Internal(errors.New("outlook mail reader release failed"))
			}
			err = errors.Join(err, outlookowa.ErrReaderClose)
		} else {
			*released = true
		}
	}()
	return outlookowa.Collect(ctx, o, account, expect)
}

// Only fixed diagnostics cross the source boundary; Windows access failures are not macOS FDA.
func owaSourceError(err error) (result error) {
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
		return &errs.Coded{Code: guard.Code, Exit: errs.ExitEnvironment, Message: "the Outlook mail layout is not supported: " + guard.Detail,
			Fix: "Update m365crawl; only one supported OWA mail database per profile is read. Nothing of this mail read was applied."}
	}
	var c *errs.Coded
	if errors.As(err, &c) {
		switch c.Code {
		case "outlook_mail_key_exhausted":
			return &errs.Coded{Code: "outlook_mail_key_exhausted", Exit: errs.ExitEnvironment,
				Message: "the archive has no numeric mail identity keys left for this account and kind",
				Fix:     "Keep the existing archive for reads; use a separate private archive for further syncs."}
		case outlookmail.CodeMailLayoutUnsupported, CodeOutlookProfileUnreadable:
			return c
		case errs.CodeInternal:
			return errs.Internal(errors.New("outlook mail source operation failed"))
		case errs.CodeDBError:
			return errs.DBError(errors.New("outlook mail archive operation failed"))
		case errs.CodeSnapshotInconsistent, errs.CodeUnsupportedBlockCompression:
			return &errs.Coded{Code: c.Code, Exit: c.Exit, Message: "the Outlook mail snapshot could not be read consistently", Fix: "Wait for Outlook to finish syncing, then run again; update m365crawl if it persists."}
		}
	}
	return &errs.Coded{Code: CodeOutlookProfileUnreadable, Exit: errs.ExitEnvironment, Message: "the Outlook mail profile could not be read",
		Fix: "Check that the Outlook profile is readable, then run again."}
}

func owaRetainedError(err error) error {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		public := *coded
		public.Message += "; " + ErrOWASnapshotRetained.Error()
		return errors.Join(&public, ErrOWASnapshotRetained)
	}
	return err
}

var (
	outlookOWAFingerprint     = outlookowa.Fingerprint
	outlookOWADefaultRoot     = outlookowa.DefaultRoot
	outlookOWADiscover        = outlookowa.Discover
	outlookOWAOpen            = outlookowa.Open
	outlookOWAClose           = (*indexeddb.Origin).Close
	outlookOWASnapshot        = teamsdesktop.Snapshot
	outlookOWAPrivateSnapshot = ownedOWASnapshot
	outlookOWARecordRun       = func(ctx context.Context, st *store.Store, run store.Run) error { return st.RecordRun(ctx, run) }
)
