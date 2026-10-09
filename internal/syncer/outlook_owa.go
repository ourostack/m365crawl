package syncer

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"runtime"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func defaultOutlookRoot() (string, error) {
	if runtime.GOOS == "windows" {
		return outlookowa.DefaultRoot()
	}
	return outlookdesktop.DefaultRoot()
}

// Explicit copied OWA roots work everywhere; explicit HxStore roots keep the old route.
func (r *runner) outlookOWARoute(root string) (bool, error) {
	if r.o.OutlookRoot == "" {
		return runtime.GOOS == "windows", nil
	}
	// HxStore discovery owns its existing validation and error contract, including Mac aliases.
	hx, classic, hxSkipped, hxErr := outlookDiscover(root)
	if len(hx)+len(classic)+len(hxSkipped) > 0 || hxErr != nil {
		return false, nil
	}
	profiles, skipped, err := outlookowa.Discover(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, owaSourceError(err)
	}
	return len(profiles)+len(skipped) > 0, nil
}

func (r *runner) outlookOWASources(ctx context.Context, _ *Report) []outlookOutcome {
	root := r.o.OutlookRoot
	if root == "" {
		var err error
		root, err = outlookDefaultRoot()
		if err != nil {
			return []outlookOutcome{{key: "outlook", err: owaSourceError(err)}}
		}
	}
	profiles, skipped, err := outlookowa.Discover(root)
	if err != nil {
		if r.o.OutlookImplicit && errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []outlookOutcome{{key: "outlook", err: owaSourceError(err)}}
	}
	var out []outlookOutcome
	for _, sp := range skipped {
		o := outlookOutcome{key: mailKey(outlookKey(sp.Name)), err: owaSourceError(sp.Err)}
		r.rememberOWAFailure(ctx, sp.Name, o.err)
		out = append(out, o)
	}
	if len(profiles)+len(skipped) == 0 && !r.o.OutlookImplicit {
		return []outlookOutcome{{key: "outlook", err: &errs.Coded{Code: CodeNoOutlookProfiles, Exit: errs.ExitEnvironment,
			Message: "no new Outlook mail profile found", Fix: "Point --outlook-root at an EBWebView directory containing Outlook IndexedDB profiles."}}}
	}
	for _, p := range profiles {
		if ctx.Err() != nil {
			out = append(out, outlookOutcome{key: mailKey(outlookKey(p.Name)), err: ctx.Err()})
			break
		}
		o := outlookOutcome{key: mailKey(outlookKey(p.Name))}
		err := contained(func() (err error) {
			o.report, o.decoded, err = r.outlookOWAMail(ctx, p)
			return err
		})
		if err != nil {
			o.err = owaSourceError(err)
			r.rememberOWAFailure(ctx, p.Name, o.err)
		}
		out = append(out, o)
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

func (r *runner) outlookOWAMail(ctx context.Context, p outlookowa.Profile) (SourceReport, bool, error) {
	begun := outlookNow()
	key, account := mailKey(outlookKey(p.Name)), outlookAccount(p.Name)
	prior, err := r.st.OutlookState(ctx, p.Name, key, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	mailState, err := r.st.MailState(ctx, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if gap := r.o.OutlookMinReadInterval; gap >= 0 {
		if gap == 0 {
			gap = OutlookMinReadInterval
		}
		if next := prior.LastAttempt.Add(gap); begun.Before(next) && !prior.LastAttempt.After(begun) {
			if f := mailState.Failure; f != nil {
				return SourceReport{}, false, &errs.Coded{Code: f.Code, Message: f.Message, Fix: f.Fix, Exit: f.Exit}
			}
			if err := r.st.SetOutlookSkipped(ctx, p.Name); err != nil {
				return SourceReport{}, false, errs.DBError(err)
			}
			return SourceReport{Source: key, Status: StatusSkippedInterval, Omissions: prior.Omissions, NextReadAfter: &next}, false, nil
		}
	}
	if err := r.st.SetOutlookChecked(ctx, p.Name, begun); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	fp, fresh, err := outlookOWAFingerprint(p)
	if err != nil {
		return SourceReport{}, false, err
	}
	// IdentityRead is a calendar marker. OWA mail uses its own read/failure marker only.
	if fp == prior.Fingerprint && !r.o.FullRead && mailState.Read && mailState.Failure == nil {
		status := StatusUnchanged
		if lost(prior.Omissions) > 0 {
			status = StatusOmissions
		}
		if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Omissions: prior.Omissions}); err != nil {
			return SourceReport{}, false, errs.DBError(err)
		}
		return SourceReport{Source: key, Status: status, Omissions: prior.Omissions}, false, nil
	}
	if err := r.st.SetOutlookLastAttempt(ctx, p.Name, begun); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if err := r.st.ClearMailRead(ctx, account); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	snap, cleanup, err := outlookOWASnapshot(ctx, p.Source)
	defer cleanup()
	if err != nil {
		return SourceReport{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return SourceReport{}, false, err
	}
	afterFP, afterFresh, err := outlookOWAFingerprint(p)
	if err != nil {
		return SourceReport{}, false, err
	}
	stable := fp == afterFP && fresh.Equal(afterFresh)
	expect, err := r.st.MailHolds(ctx, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	res, err := collectOWAMail(ctx, snap, account, expect)
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

// The Origin closes before this returns, so the caller can then remove the private snapshot.
func collectOWAMail(ctx context.Context, snap, account string, expect bool) (res outlookowa.Result, err error) {
	o, err := outlookowa.Open(ctx, filepath.Join(snap, "leveldb"), filepath.Join(snap, "blob"))
	if err != nil {
		return res, err
	}
	defer func() {
		if closeErr := o.Close(); closeErr != nil && err == nil {
			err = errors.New("outlookowa: close_failed")
		}
	}()
	return outlookowa.Collect(ctx, o, account, expect)
}

// Only fixed diagnostics cross the source boundary; Windows access failures are not macOS FDA.
func owaSourceError(err error) error {
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
		case outlookmail.CodeMailLayoutUnsupported, CodeOutlookProfileUnreadable:
			return c
		case errs.CodeInternal:
			return errs.Internal(errors.New("Outlook mail source operation failed"))
		case errs.CodeDBError:
			return errs.DBError(errors.New("Outlook mail archive operation failed"))
		case errs.CodeSnapshotInconsistent, errs.CodeUnsupportedBlockCompression:
			return &errs.Coded{Code: c.Code, Exit: c.Exit, Message: "the Outlook mail snapshot could not be read consistently", Fix: "Wait for Outlook to finish syncing, then run again; update m365crawl if it persists."}
		}
	}
	return &errs.Coded{Code: CodeOutlookProfileUnreadable, Exit: errs.ExitEnvironment, Message: "the Outlook mail profile could not be read",
		Fix: "Check that the Outlook profile is readable, then run again."}
}

var (
	outlookOWAFingerprint = outlookowa.Fingerprint
	outlookOWASnapshot    = teamsdesktop.Snapshot
	outlookOWARecordRun   = func(ctx context.Context, st *store.Store, run store.Run) error { return st.RecordRun(ctx, run) }
)
