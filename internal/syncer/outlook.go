package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/hxstore"
	"github.com/ourostack/teamscrawl/internal/outlookcal"
	"github.com/ourostack/teamscrawl/internal/outlookdesktop"
	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// OutlookMinReadInterval is the least time between two copies of one Outlook store, measured
// from the last attempted copy, so a failing store is not retried in a loop. The store is
// rewritten in place all day and a read copies about 100 MB. The value is assumed and is tuned by
// the real-store measurement.
const OutlookMinReadInterval = 5 * time.Minute

// StatusSkippedInterval is an Outlook source that was not read because the minimum interval has
// not passed. It is not an omission and not a loss.
const StatusSkippedInterval = "skipped_interval"

// outlookKey is the source key of an Outlook profile in sync_runs and in the report.
func outlookKey(profile string) string { return "outlook|" + profile }

// outlookAccount is the calendar account id of a profile.
func outlookAccount(profile string) string { return "outlook/" + profile }

// outlookVersions are the version inputs of the Outlook fingerprint: the mapper version is one of
// them, so a mapper change reads an unchanged store again.
func outlookVersions() outlookdesktop.Versions {
	return outlookdesktop.Versions{Store: string(rune(hxstore.KnownStoreVersions[0])), Reader: hxstore.ReaderVersion, Mapper: outlookMapperVersion, Rules: teamsdesktop.RulesVersion}
}

// Test seams: the clock, the profile discovery, the copy and the mapper version (a test raises it
// to see a bump read an unchanged store again).
var (
	outlookMapperVersion = outlookcal.MapperVersion
	outlookNow           = func() time.Time { return time.Now().UTC() }
	outlookDefaultRoot   = outlookdesktop.DefaultRoot
	outlookDiscover      = outlookdesktop.Discover
	outlookSnapshot      = outlookdesktop.Snapshot
)

// outlookOutcome is one profile's result, or the failure of finding profiles at all (key "outlook").
type outlookOutcome struct {
	key     string
	report  SourceReport
	decoded bool
	err     error
}

// outlookSources runs after the Teams sources have finished (memory: the two never hold their
// copies at once). Every profile is its own source and fails alone.
func (r *runner) outlookSources(ctx context.Context, rep *Report) []outlookOutcome {
	root := r.o.OutlookRoot
	if root == "" {
		var err error
		if root, err = outlookDefaultRoot(); err != nil {
			return []outlookOutcome{{key: "outlook", err: err}}
		}
	}
	profiles, _, _, err := outlookDiscover(root)
	if err != nil {
		return []outlookOutcome{{key: "outlook", err: err}}
	}
	var out []outlookOutcome
	for _, p := range profiles {
		o := outlookOutcome{key: outlookKey(p.Name)}
		o.report, o.decoded, o.err = r.safeOutlook(ctx, p, rep)
		out = append(out, o)
	}
	return out
}

func (r *runner) safeOutlook(ctx context.Context, p outlookdesktop.Profile, rep *Report) (sr SourceReport, decoded bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			sr, decoded, err = SourceReport{}, false, errs.Internal(fmt.Errorf("panic while syncing: %v", rec))
		}
	}()
	return r.outlook(ctx, p, rep)
}

// outlook reads one profile. The order is the cheap checks first: the minimum interval, then the
// fingerprint, then the copy, the guard and one transaction.
func (r *runner) outlook(ctx context.Context, p outlookdesktop.Profile, rep *Report) (SourceReport, bool, error) {
	begun := outlookNow()
	key, account := outlookKey(p.Name), outlookAccount(p.Name)
	prior, err := r.st.OutlookState(ctx, p.Name, key, account)
	if err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	if gap := r.o.OutlookMinReadInterval; gap >= 0 {
		if gap == 0 {
			gap = OutlookMinReadInterval
		}
		if next := prior.LastAttempt.Add(gap); begun.Before(next) {
			r.progress("%s: %s", key, StatusSkippedInterval)
			return SourceReport{Source: key, Status: StatusSkippedInterval, NextReadAfter: &next}, false, nil
		}
	}
	fp, err := outlookdesktop.FingerprintOf(p.StorePath, outlookVersions())
	if err != nil {
		return SourceReport{}, false, err
	}
	if prior.Fingerprint == fp && !r.o.FullRead {
		status := StatusUnchanged
		if lost(prior.Omissions) > 0 {
			status = StatusOmissions
		}
		if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Omissions: prior.Omissions}); err != nil {
			return SourceReport{}, false, errs.DBError(err)
		}
		r.progress("%s: %s", key, status)
		return SourceReport{Source: key, Status: status, Omissions: prior.Omissions}, false, nil
	}
	if err := r.st.SetOutlookLastAttempt(ctx, p.Name, begun); err != nil {
		return SourceReport{}, false, errs.DBError(err)
	}
	info, cleanup, err := outlookSnapshot(ctx, p.StorePath)
	defer cleanup()
	if err != nil {
		return SourceReport{}, false, err
	}
	res, err := collectOutlook(ctx, info, account, prior.HoldsEvents)
	if err != nil {
		return SourceReport{}, false, err
	}
	zone := calendarZone()
	var omissions map[string]int
	status := StatusOK
	cal, err := r.st.CommitOutlook(ctx, store.OutlookBatch{Account: account, Events: res.Events, FreshAt: info.ModTime, At: begun, Zone: zone, Stamp: store.OutlookStamp(outlookMapperVersion, zone)},
		func(cal store.CalendarResult) store.Run {
			if omissions = outlookOmissions(res, cal); lost(omissions) > 0 {
				status = StatusOmissions
			}
			return store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Counts: runCounts{Calendar: cal.Counts}, Omissions: omissions}
		})
	if err != nil {
		return SourceReport{}, false, asCoded(err)
	}
	rep.Calendar.Add(cal.Counts)
	r.progress("%s: %s (%d events)", key, status, len(res.Events))
	return SourceReport{Source: key, Status: status, Omissions: omissions, Accounts: []string{account}, Counts: &SourceCounts{Calendar: cal.Counts}}, true, nil
}

// collectOutlook opens the private copy and reads it. A guard refusal comes back as a coded
// error that carries the guard's code.
func collectOutlook(ctx context.Context, info outlookdesktop.Info, account string, had bool) (outlookcal.Result, error) {
	f, err := os.Open(info.Path)
	if err != nil {
		return outlookcal.Result{}, errs.Internal(err)
	}
	defer func() { _ = f.Close() }()
	s, err := outlookcal.OpenStore(f, info.Size)
	if err == nil {
		var res outlookcal.Result
		if res, err = outlookcal.Collect(ctx, s, account, outlookcal.Options{ExpectEvents: had}); err == nil {
			return res, nil
		}
	}
	var guard *outlookcal.GuardError
	if errors.As(err, &guard) {
		return outlookcal.Result{}, &errs.Coded{Code: guard.Code, Exit: errs.ExitEnvironment, Message: "the Outlook store cannot be read by this version of teamscrawl: " + guard.Error(),
			Fix: "Update teamscrawl: this version reads Outlook store version i, event layout 0x6b/0x455. Nothing of Outlook was applied."}
	}
	return outlookcal.Result{}, err
}

// outlookOmissions are the losses of one read, by code. An event that does not map is the
// calendar loss calendar_unmapped and one the core refuses calendar_refused (the codes the Teams
// derivation uses); damaged blocks keep the reader's own code.
func outlookOmissions(res outlookcal.Result, cal store.CalendarResult) map[string]int {
	out := map[string]int{}
	for _, l := range res.Losses {
		code := l.Code
		if code == outlookcal.CodeEventUnmapped {
			code = store.OmitCalendarUnmapped
		}
		out[code] += l.Count
	}
	for k, v := range cal.Omissions {
		out[k] += v
	}
	if n := res.Notes.AllDayUnaligned; n > 0 {
		out[store.OmitCalendarAllDayUnaligned] = n
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
