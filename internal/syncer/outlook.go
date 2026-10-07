package syncer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// OutlookMinReadInterval is the least time between two copies of one Outlook store, measured
// from the last attempted copy, so a failing store is not retried in a loop. The store is
// rewritten in place all day and a read copies about 100 MB. The value is assumed and is tuned by
// the real-store measurement.
const OutlookMinReadInterval = 5 * time.Minute

// StatusSkippedInterval is an Outlook source that was not read because the minimum interval has
// not passed. It is not an omission and not a loss.
const StatusSkippedInterval = "skipped_interval"

// StatusUnavailable is an Outlook source that was on by default and could not be read. It is a
// report and not a failure: the Teams sync it ran beside is as good as it was.
const StatusUnavailable = "unavailable"

// outlookKey is the source key of an Outlook profile in sync_runs and in the report.
func outlookKey(profile string) string { return "outlook|" + profile }

// outlookAccount is the calendar account id of a profile.
func outlookAccount(profile string) string { return "outlook/" + profile }

// outlookVersions are the version inputs of the Outlook fingerprint: the mapper version is one of
// them, so a mapper change reads an unchanged store again.
func outlookVersions() outlookdesktop.Versions {
	return outlookdesktop.Versions{Store: string(rune(hxstore.KnownStoreVersions[0])), Reader: hxstore.ReaderVersion, Mapper: outlookMapperVersion, Rules: teamsdesktop.RulesVersion, Mail: outlookMailMapperVersion}
}

// Test seams: the clock, the profile discovery, the copy and the mapper version (a test raises it
// to see a bump read an unchanged store again).
var (
	outlookMapperVersion     = outlookcal.MapperVersion
	outlookMailMapperVersion = outlookmail.MapperVersion
	outlookNow               = func() time.Time { return time.Now().UTC() }
	outlookDefaultRoot       = outlookdesktop.DefaultRoot
	outlookDiscover          = outlookdesktop.Discover
	outlookSnapshot          = outlookdesktop.Snapshot
)

// outlookOutcome is one profile's result, or the failure of finding profiles at all (key "outlook").
type outlookOutcome struct {
	key     string
	report  SourceReport
	decoded bool
	err     error
	// note marks a row that only says something (mail on a platform that does not read it): it is
	// neither a committed source nor a failure.
	note bool
}

// outlookSources runs after the Teams sources have finished (memory: the two never hold their
// copies at once). Every profile is its own source and fails alone. Outlook is on because the
// caller asked for it, so having nothing to read is a failure, not silence: no profile at all is
// one failed source "outlook", and each profile that could not be examined is one failed source.
// Profiles of the old Outlook are a note, not a loss.
func (r *runner) outlookSources(ctx context.Context, rep *Report) []outlookOutcome {
	root := r.o.OutlookRoot
	if root == "" {
		var err error
		if root, err = outlookDefaultRoot(); err != nil {
			return r.noOutlook(err)
		}
	}
	profiles, classic, skipped, err := outlookDiscover(root)
	if err != nil {
		return r.noOutlook(err)
	}
	rep.OutlookClassicOnly = classic
	var out []outlookOutcome
	for _, sp := range skipped {
		out = append(out, outlookOutcome{key: outlookKey(sp.Name), err: skippedProfileError(sp)})
	}
	if len(profiles) == 0 && len(skipped) == 0 && !r.o.OutlookImplicit {
		out = append(out, outlookOutcome{key: "outlook", err: NoOutlookProfilesError(root, classic)})
	}
	for _, p := range profiles {
		o := outlookOutcome{key: outlookKey(p.Name)}
		var mail []outlookOutcome
		o.report, o.decoded, mail, o.err = r.safeOutlook(ctx, p, rep)
		if o.err != nil && ctx.Err() == nil {
			// Remembered, so a skipped read reports it instead of reading as a success.
			c := codedOf(o.err)
			_ = r.st.SetOutlookFailure(ctx, outlookAccount(p.Name), &store.OutlookFailure{Code: c.Code, Message: bodyMessage(c), Fix: c.Fix, Exit: c.Exit})
		}
		out = append(out, o)
		out = append(out, mail...)
	}
	return out
}

// noOutlook is the outcome of an Outlook source whose profiles directory cannot be listed. Asked for
// by name, that is a failed source. On by default, a machine without the new Outlook (no directory,
// or an operating system with none) has nothing to say, and any other cause (no Full Disk Access)
// is reported as unavailable.
func (r *runner) noOutlook(err error) []outlookOutcome {
	if r.o.OutlookImplicit && (errors.Is(err, outlookdesktop.ErrRootNotFound) || errors.Is(err, outlookdesktop.ErrNotSupported)) {
		return nil
	}
	return []outlookOutcome{{key: "outlook", err: err}}
}

// outlookPresent says whether the default profiles directory holds a profile or one that cannot be
// examined: whether Outlook has anything for a machine that has no Teams.
func (r *runner) outlookPresent() bool {
	root := r.o.OutlookRoot
	if root == "" {
		var err error
		if root, err = outlookDefaultRoot(); err != nil {
			return false
		}
	}
	profiles, _, skipped, _ := outlookDiscover(root)
	return len(profiles)+len(skipped) > 0
}

// unavailableSource is the report of an Outlook source that could not be read and did not have to be.
func unavailableSource(key string, c *errs.Coded) SourceReport {
	return SourceReport{Source: key, Status: StatusUnavailable, Error: &SourceError{Code: c.Code, Message: bodyMessage(c), Fix: c.Fix}}
}

// autoLinkOutlook links each profile to the Teams account that has one of its addresses (store
// AutoLinkOutlook). It never fails a sync: the link only changes how events are merged, and a
// profile that is not linked is told so by the unlinked notice.
func (r *runner) autoLinkOutlook(ctx context.Context) {
	res, err := r.st.AutoLinkOutlook(ctx, time.Now().UTC())
	switch {
	case err != nil:
		r.progress("outlook: automatic link failed: %v", err)
	case res.Linked+res.Unlinked > 0:
		r.progress("outlook: %d linked and %d unlinked by address", res.Linked, res.Unlinked)
	}
}

// CodeNoOutlookProfiles and CodeOutlookProfileUnreadable are the failures of an Outlook source
// that has nothing to read or whose profile could not be examined.
const (
	CodeNoOutlookProfiles        = "no_outlook_profiles"
	CodeOutlookProfileUnreadable = "outlook_profile_unreadable"
)

// NoOutlookProfilesError is the failure of an Outlook source that finds no profile under root;
// `doctor` gives the same message and fix.
func NoOutlookProfilesError(root string, classic []string) *errs.Coded {
	fix := "--outlook-root is the directory of Outlook profiles: one directory per profile, each holding HxStore.hxd (<root>/<profile>/HxStore.hxd)."
	if info, err := os.Stat(filepath.Join(root, outlookdesktop.StoreFileName)); err == nil && info.Mode().IsRegular() {
		fix = "HxStore.hxd is directly in " + root + ", which is a profile directory: point --outlook-root at its parent (<root>/<profile>/HxStore.hxd)."
	}
	msg := "no Outlook profile found under " + root
	if len(classic) > 0 {
		msg += fmt.Sprintf(" (%d profile(s) of the classic Outlook are not read)", len(classic))
	}
	return &errs.Coded{Code: CodeNoOutlookProfiles, Exit: errs.ExitEnvironment, Message: msg, Fix: fix}
}

func skippedProfileError(sp outlookdesktop.SkippedProfile) *errs.Coded {
	if sp.Reason == "no_full_disk_access" {
		return errs.NoFullDiskAccess(sp.Dir, sp.Err)
	}
	return &errs.Coded{Code: CodeOutlookProfileUnreadable, Exit: errs.ExitEnvironment, Message: "the Outlook profile " + sp.Name + " could not be examined",
		Fix: "Check that the profile directory is readable, then run again."}
}

func (r *runner) safeOutlook(ctx context.Context, p outlookdesktop.Profile, rep *Report) (sr SourceReport, decoded bool, mail []outlookOutcome, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			sr, decoded, err = SourceReport{}, false, errs.Internal(fmt.Errorf("panic while syncing: %v", rec))
		}
	}()
	return r.outlook(ctx, p, rep)
}

// outlook reads one profile: the calendar and, after it, the mail, from one copy of the store.
// The order is the cheap checks first: the minimum interval, then the fingerprint, then the copy,
// the guard and one transaction per kind of data. The mail outcomes are rows of their own; a
// refusal or failure of one kind never stops the other.
func (r *runner) outlook(ctx context.Context, p outlookdesktop.Profile, rep *Report) (SourceReport, bool, []outlookOutcome, error) {
	begun := outlookNow()
	key, account := outlookKey(p.Name), outlookAccount(p.Name)
	prior, err := r.st.OutlookState(ctx, p.Name, key, account)
	if err != nil {
		return SourceReport{}, false, nil, errs.DBError(err)
	}
	mailOn := outlookMailSupported()
	if gap := r.o.OutlookMinReadInterval; gap >= 0 {
		if gap == 0 {
			gap = OutlookMinReadInterval
		}
		// A last attempt later than this run's start is a clock that moved back: it is stale.
		if next := prior.LastAttempt.Add(gap); begun.Before(next) && !prior.LastAttempt.After(begun) {
			if f := prior.Failure; f != nil {
				// A skip after a failed read is still that failure, not a success.
				return SourceReport{}, false, nil, &errs.Coded{Code: f.Code, Message: f.Message, Fix: f.Fix, Exit: f.Exit}
			}
			_ = r.st.SetOutlookSkipped(ctx, p.Name)
			r.progress("%s: %s", key, StatusSkippedInterval)
			skipped := SourceReport{Source: key, Status: StatusSkippedInterval, Omissions: prior.Omissions, NextReadAfter: &next}
			return skipped, false, r.mailRowOf(mailOn, SourceReport{Status: StatusSkippedInterval, NextReadAfter: &next}, key), nil
		}
	}
	_ = r.st.SetOutlookChecked(ctx, p.Name, begun) // the store is looked at from here on; a skip never gets here
	fp, err := outlookdesktop.FingerprintOf(p.StorePath, outlookVersions())
	if err != nil {
		return SourceReport{}, false, nil, err
	}
	mailState, err := r.st.MailState(ctx, account)
	if err != nil {
		return SourceReport{}, false, nil, errs.DBError(err)
	}
	// An archive that never read the account's mail (an upgrade, or a failed read) reads it even
	// when the store did not change.
	if prior.Fingerprint == fp && !r.o.FullRead && prior.IdentityRead && (!mailOn || mailState.Read) {
		status := StatusUnchanged
		if lost(prior.Omissions) > 0 {
			status = StatusOmissions
		}
		if err := r.st.RecordRun(ctx, store.Run{StartedAt: begun, FinishedAt: outlookNow(), Source: key, Fingerprint: fp, Status: status, Omissions: prior.Omissions}); err != nil {
			return SourceReport{}, false, nil, errs.DBError(err)
		}
		_ = r.st.SetOutlookFailure(ctx, account, nil) // the store is what the last good read saw
		r.progress("%s: %s", key, status)
		return SourceReport{Source: key, Status: status, Omissions: prior.Omissions}, false, r.mailRowOf(mailOn, SourceReport{Status: StatusUnchanged}, key), nil
	}
	if err := r.st.SetOutlookLastAttempt(ctx, p.Name, begun); err != nil {
		return SourceReport{}, false, nil, errs.DBError(err)
	}
	info, cleanup, err := outlookSnapshot(ctx, p.StorePath)
	defer cleanup()
	if err != nil {
		return SourceReport{}, false, nil, err
	}
	if mailOn {
		// The calendar commit records the new fingerprint. If the sync stops before the mail is
		// committed, the old marker must not stand, or the mail would be skipped until the store
		// changes again.
		if err := r.st.ClearMailRead(ctx, account); err != nil {
			return SourceReport{}, false, nil, errs.DBError(err)
		}
	}
	sr, decoded, calErr := r.outlookCalendar(ctx, info, rep, prior, key, account, fp, begun)
	afterCalendar()
	if ctx.Err() != nil {
		return sr, decoded, nil, calErr
	}
	if !mailOn {
		return sr, decoded, r.mailRowOf(false, SourceReport{}, key), calErr
	}
	// The calendar's result is out of scope here: the mail read starts with the heap the calendar
	// read gave back (A19).
	return sr, decoded, []outlookOutcome{r.outlookMail(ctx, p, info, fp, begun)}, calErr
}

// afterCalendar is a seam for a test that stops the sync between the calendar commit and the mail.
var afterCalendar = func() {}

// outlookCalendar reads the calendar from the private copy and commits it in one transaction.
func (r *runner) outlookCalendar(ctx context.Context, info outlookdesktop.Info, rep *Report, prior store.OutlookState, key, account, fp string, begun time.Time) (SourceReport, bool, error) {
	res, err := collectOutlook(ctx, info, account, prior.HoldsEvents)
	if err != nil {
		return SourceReport{}, false, err
	}
	zone := calendarZone()
	// An event Outlook deleted leaves the store, with no tombstone, once Outlook compacts (docs/outlook-store.md).
	// Absence is read as deletion only from a read that lost nothing: a damaged block or an event that
	// would not map could hide a live event, and then every unseen event stays.
	var omissions map[string]int
	status := StatusOK
	cal, err := r.st.CommitOutlook(ctx, store.OutlookBatch{Account: account, Events: res.Events, FreshAt: info.ModTime, At: begun, Zone: zone, Stamp: store.OutlookStamp(outlookMapperVersion, zone), Read: outlookRead(res), InferGone: len(res.Losses) == 0, Addresses: res.AccountAddresses},
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

// outlookGCPercent is the GC target while the Outlook store is read.
const outlookGCPercent = 25

// collectOutlook opens the private copy and reads it. A guard refusal comes back as a coded
// error that carries the guard's code.
func collectOutlook(ctx context.Context, info outlookdesktop.Info, account string, had bool) (outlookcal.Result, error) {
	f, err := os.Open(info.Path)
	if err != nil {
		return outlookcal.Result{}, errs.Internal(err)
	}
	defer func() { _ = f.Close() }()
	var res outlookcal.Result
	s, err := outlookcal.OpenStore(f, info.Size)
	if err == nil {
		// The read inflates about 400 MB of payloads and builds as much garbage as it keeps. Hand
		// back what the earlier sources freed, collect at a quarter of the live heap (not the usual
		// 100%) while the read runs, and hand back its garbage after. A soft memory limit is not
		// used: it is an absolute number and the live heap of the earlier sources is not known here.
		debug.FreeOSMemory()
		defer debug.FreeOSMemory()
		defer debug.SetGCPercent(debug.SetGCPercent(outlookGCPercent))
		if res, err = outlookcal.Collect(ctx, s, account, outlookcal.Options{ExpectEvents: had}); err == nil {
			return res, nil
		}
	}
	var guard *outlookcal.GuardError
	if errors.As(err, &guard) {
		return outlookcal.Result{}, &errs.Coded{Code: guard.Code, Exit: errs.ExitEnvironment, Message: "the Outlook store cannot be read by this version of m365crawl: " + guard.Error(),
			Fix: "Update m365crawl: this version reads Outlook store version i, event layout 0x6b/0x455. Nothing of Outlook was applied."}
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

// outlookRead is the census of one read that `calendar sources` shows: the layouts the reader does
// not know, how many blocks were damaged and how many values fell outside the mapped sets.
func outlookRead(res outlookcal.Result) store.OutlookRead {
	r := store.OutlookRead{BlocksFound: res.Stats.BlocksFound, BlocksInvalid: res.Stats.BlocksRejected()}
	for _, l := range res.UnknownLayouts {
		r.UnknownLayouts = append(r.UnknownLayouts, store.OutlookLayout{Class: l.Class, Tag: l.Tag, Count: l.Count})
	}
	for name, n := range map[string]int{"event_type": res.Notes.EventTypeUnknown, "show_as": res.Notes.ShowAsUnmapped, "response": res.Notes.ResponseUnmapped} {
		if n > 0 {
			if r.UnmappedValues == nil {
				r.UnmappedValues = map[string]int{}
			}
			r.UnmappedValues[name] = n
		}
	}
	return r
}

// OutlookLinkNone, as Options.OutlookLink, ends the link of an Outlook profile.
const OutlookLinkNone = "none"

// linkOutlook applies Options.OutlookLink. A mistake is a usage-class error, and nothing changes.
func (r *runner) linkOutlook(ctx context.Context) error {
	profiles, err := r.linkProfiles(ctx)
	if err != nil {
		return err
	}
	principal := r.o.OutlookLink
	if principal == OutlookLinkNone {
		principal = ""
	}
	at := outlookNow()
	for _, name := range profiles {
		if err := r.st.SetOutlookLink(ctx, outlookAccount(name), principal, at); err != nil {
			return asCoded(err)
		}
	}
	return nil
}

// linkProfiles picks the profiles a link applies to: the one named, the only one there is, or (to
// end links only) all of them. A link of two profiles to one Teams account is refused by the core,
// so a choice among several is the operator's. Ending a link also knows the profiles the archive
// holds an active link for, so a link whose profile directory is gone can still be ended.
func (r *runner) linkProfiles(ctx context.Context) ([]string, error) {
	none := r.o.OutlookLink == OutlookLinkNone
	root := r.o.OutlookRoot
	var names []string
	var err error
	if root == "" {
		root, err = outlookDefaultRoot()
	}
	if err == nil {
		var found []outlookdesktop.Profile
		if found, _, _, err = outlookDiscover(root); err == nil {
			for _, p := range found {
				names = append(names, p.Name)
			}
		}
	}
	if err != nil && !none {
		return nil, err
	}
	if none { // a missing directory is no reason to keep a link
		linked, lerr := r.st.OutlookLinkedAccounts(ctx)
		if lerr != nil {
			return nil, asCoded(lerr)
		}
		for _, a := range linked {
			if n := strings.TrimPrefix(a, "outlook/"); !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		slices.Sort(names)
	}
	switch want := r.o.OutlookLinkProfile; {
	case want != "":
		if !slices.Contains(names, want) {
			return nil, linkProfileUsage("no Outlook profile named "+want+" under "+root, names)
		}
		return []string{want}, nil
	case len(names) == 0 && !none:
		return nil, linkProfileUsage("no Outlook profile found under "+root+" to link", names)
	case len(names) > 1 && !none:
		return nil, linkProfileUsage("more than one Outlook profile is under "+root+": say which one to link", names)
	}
	return names, nil
}

func linkProfileUsage(msg string, names []string) error {
	c := errs.Usage(msg)
	c.Fix = "Add --outlook-profile NAME."
	if len(names) > 0 {
		c.Fix = "Add --outlook-profile NAME, one of: " + strings.Join(names, ", ") + "."
	}
	return c
}
