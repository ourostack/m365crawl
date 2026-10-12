package syncer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ourostack/m365crawl/internal/loopcache"
	"github.com/ourostack/m365crawl/internal/store"
)

// LoopOptions selects a Loop refresh. Explicit runs (loop sync) may discover new archived tuples;
// ordinary reads refresh only registered sources whose last attempt is older than MaxAge, and
// MaxAge zero is archive-only.
type LoopOptions struct {
	DBPath             string
	SourceID           int64
	Explicit, Discover bool
	MaxAge             time.Duration
	// Probe is for a caller that reads the sources itself afterwards: an ordinary run with nothing
	// due reads no registered record and reports no Sources.
	Probe bool
}

// LoopReport is what a refresh did. Sources are the participating registrations after it;
// Diagnostics hold bounded fixed-code lines (an identity-invalid record by its numeric records
// rowid, a superseded attempt by its registration), never archived strings. A run that fails after
// writing still returns the counts of what it committed.
type LoopReport struct {
	Sources                               []store.LoopSource
	Projected, Unchanged, Failed, Skipped int
	Diagnostics                           []string
	// InvalidRecords counts the unregistered archived records whose identity this run refused.
	InvalidRecords int
	// DiscoveryRemaining says that discovery registered one full batch and left
	// DiscoveryRemainingRecords unregistered records uninspected; the next explicit run continues.
	DiscoveryRemaining        bool
	DiscoveryRemainingRecords int
}

var (
	loopNow      = time.Now
	loopRead     = loopcache.ReadSnapshot
	loopOpen     = store.Open
	loopReadOnly = store.OpenReadOnly
	loopAcquire  = store.AcquireLock
)

// SyncLoop projects cached Loop snapshots from records already in the archive. It never reads
// application storage, the network or a browser.
func SyncLoop(ctx context.Context, options LoopOptions) (LoopReport, error) {
	if options.SourceID < 0 || options.MaxAge < 0 || (options.Discover && (!options.Explicit || options.SourceID != 0)) {
		return LoopReport{}, errors.New("loop: invalid source selection")
	}
	if !options.Explicit && options.Probe {
		if due, err := loopAnyDue(ctx, options); err != nil || !due {
			return LoopReport{Sources: []store.LoopSource{}}, err
		}
	} else if !options.Explicit {
		report, err := readLoopSources(ctx, options)
		if err != nil || options.MaxAge == 0 || !slices.ContainsFunc(report.Sources, func(source store.LoopSource) bool {
			return loopDue(source, options.MaxAge)
		}) {
			return report, err
		}
	}
	release, err := loopAcquire(options.DBPath)
	if err != nil {
		return LoopReport{}, err
	}
	defer release()
	st, err := loopOpen(ctx, options.DBPath)
	if err != nil {
		return LoopReport{}, err
	}
	// A failure after a commit still returns what the run committed.
	report, runErr := syncLoop(ctx, st, options)
	return report, errors.Join(runErr, st.Close())
}

// loopDue says whether an ordinary read may refresh the source: its last attempt is older than maxAge.
func loopDue(source store.LoopSource, maxAge time.Duration) bool {
	return source.Health.AttemptedAt == nil || loopNow().Sub(*source.Health.AttemptedAt) >= maxAge
}

// loopAnyDue says whether an ordinary run has a selected registered source to refresh. It reads
// attempt times only, never a registered record, and never locks, migrates or writes.
func loopAnyDue(ctx context.Context, options LoopOptions) (bool, error) {
	st, err := loopReadOnly(ctx, options.DBPath)
	if errors.Is(err, store.ErrNoArchive) {
		if options.SourceID != 0 {
			return false, store.ErrKnowledgeSourceUnknown
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	health, readErr := st.KnowledgeSources(ctx, store.LoopKind)
	if err := errors.Join(readErr, st.Close()); err != nil {
		return false, err
	}
	due, known := false, options.SourceID == 0
	for _, source := range health {
		if source.Status == "retained" || (options.SourceID != 0 && source.LocatorID != options.SourceID) {
			continue
		}
		known = true
		due = due || loopDue(store.LoopSource{Health: source}, options.MaxAge)
	}
	if !known {
		return false, store.ErrKnowledgeSourceUnknown
	}
	return due && options.MaxAge > 0, nil
}

// readLoopSources is the archive-only path: no lock, migration, discovery or projection.
func readLoopSources(ctx context.Context, options LoopOptions) (LoopReport, error) {
	st, err := loopReadOnly(ctx, options.DBPath)
	if errors.Is(err, store.ErrNoArchive) {
		if options.SourceID != 0 {
			return LoopReport{}, store.ErrKnowledgeSourceUnknown
		}
		return LoopReport{Sources: []store.LoopSource{}}, nil
	}
	if err != nil {
		return LoopReport{}, err
	}
	sources, readErr := st.LoopSources(ctx, options.SourceID)
	if err := errors.Join(readErr, st.Close()); err != nil {
		return LoopReport{}, err
	}
	return LoopReport{Sources: sources}, nil
}

// loopStore is the part of the archive a Loop refresh uses; tests wrap it to inject failures.
type loopStore interface {
	LoopSources(context.Context, int64) ([]store.LoopSource, error)
	LoopRawFrame(context.Context, int64) (store.LoopAcquisition, error)
	LoopDiscoveryCandidates(context.Context) (store.LoopDiscovery, error)
	BeginLoopAttempt(context.Context, store.LoopRegistration, time.Time) (store.KnowledgeAttempt, error)
	CommitLoopObservations(context.Context, store.KnowledgeAttempt, store.LoopRawFrame, loopcache.Result, time.Time) error
	RecordLoopFailure(context.Context, store.KnowledgeAttempt, string, time.Time) error
}

type loopRun struct {
	st            loopStore
	options       LoopOptions
	report        LoopReport
	participating map[int64]bool
}

// syncLoop refreshes the selected registered sources, then (for discovery) registers and projects
// at most one batch of new tuples in record order. Each value is loaded, parsed, committed and
// released before the next one is loaded.
func syncLoop(ctx context.Context, st loopStore, options LoopOptions) (LoopReport, error) {
	run := &loopRun{st: st, options: options, participating: map[int64]bool{}, report: LoopReport{Sources: []store.LoopSource{}, Diagnostics: []string{}}}
	err := run.sync(ctx)
	// The participating sources are read even after a failure, so the report shows what was committed.
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	sources, readErr := st.LoopSources(readCtx, options.SourceID)
	if err == nil {
		err = readErr
	}
	for _, source := range sources {
		if run.participating[source.Registration.ID] {
			run.report.Sources = append(run.report.Sources, source)
		}
	}
	return run.report, err
}

func (run *loopRun) sync(ctx context.Context) error {
	registered, err := run.st.LoopSources(ctx, run.options.SourceID)
	if err != nil {
		return err
	}
	for _, source := range registered {
		if err := ctx.Err(); err != nil {
			return err
		}
		run.participating[source.Registration.ID] = true
		if !run.options.Explicit && !loopDue(source, run.options.MaxAge) {
			run.report.Skipped++
			continue
		}
		if err := run.project(ctx, source.Registration, source); err != nil {
			return err
		}
	}
	if !run.options.Discover {
		return nil
	}
	discovery, err := run.st.LoopDiscoveryCandidates(ctx)
	if err != nil {
		return err
	}
	run.report.InvalidRecords = discovery.Invalid
	for _, record := range discovery.InvalidRecords {
		run.report.Diagnostics = append(run.report.Diagnostics, fmt.Sprintf("loop_cache_identity_unqualified: records rowid %d", record))
	}
	if more := discovery.Invalid - len(discovery.InvalidRecords); more > 0 {
		run.report.Diagnostics = append(run.report.Diagnostics, fmt.Sprintf("loop_cache_identity_unqualified: %d more records", more))
	}
	run.report.DiscoveryRemaining, run.report.DiscoveryRemainingRecords = discovery.Remaining > 0, discovery.Remaining
	for _, candidate := range discovery.Candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := run.project(ctx, *candidate.Registration, store.LoopSource{}); err != nil {
			return err
		}
	}
	return nil
}

// project carries one registration through attempt, value acquisition, pure parsing and
// publication. The tuple is registered and its attempt begun before the value is loaded.
// Source-local refusals and reader failures become fixed failed attempts that keep the last good
// snapshot; cancellation and archive errors are returned.
func (run *loopRun) project(ctx context.Context, registration store.LoopRegistration, previous store.LoopSource) error {
	attempt, err := run.st.BeginLoopAttempt(ctx, registration, loopNow().UTC())
	if err != nil {
		return err
	}
	run.participating[attempt.LocatorID] = true
	acquisition, err := run.st.LoopRawFrame(ctx, attempt.LocatorID)
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), run.fail(ctx, attempt, "loop_cache_cancelled"))
	}
	if err != nil {
		return errors.Join(err, run.fail(ctx, attempt, "loop_cache_reader_failed"))
	}
	if acquisition.Registration.TenantID != registration.TenantID || acquisition.Registration.UserID != registration.UserID {
		return run.fail(ctx, attempt, "loop_cache_input_changed") // the owner evidence the attempt recorded is stale
	}
	if acquisition.Frame == nil {
		return run.fail(ctx, attempt, acquisition.RefusalCode)
	}
	result, err := safeLoopRead(ctx, acquisition.Frame.Value)
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), run.fail(ctx, attempt, "loop_cache_cancelled"))
	}
	if err != nil {
		reason := "loop_cache_reader_failed"
		var typed *loopcache.ReadError
		if errors.As(err, &typed) && slices.Contains(store.LoopFailureReasons, typed.Code) {
			reason = typed.Code
		}
		return run.fail(ctx, attempt, reason)
	}
	unchanged := previous.Health.AdmittedID > 0 && previous.Health.ContentDigest == acquisition.Frame.ValueDigest &&
		previous.SnapshotQualification == store.LoopQualification
	err = run.st.CommitLoopObservations(ctx, attempt, *acquisition.Frame, result, loopNow().UTC())
	switch {
	case errors.Is(err, store.ErrLoopInputChanged):
		return run.fail(ctx, attempt, "loop_cache_input_changed")
	case errors.Is(err, store.ErrStaleKnowledgeAttempt):
		run.superseded(attempt)
	case err != nil:
		return err
	case unchanged:
		run.report.Unchanged++
	default:
		run.report.Projected++
	}
	return nil
}

func (run *loopRun) superseded(attempt store.KnowledgeAttempt) {
	run.report.Diagnostics = append(run.report.Diagnostics, fmt.Sprintf("stale_attempt: source %d superseded by a newer attempt", attempt.LocatorID))
}

// fail records a fixed reason on the still-current attempt. A newer attempt wins; the archive's
// own errors are returned. The record survives the command's cancellation.
func (run *loopRun) fail(ctx context.Context, attempt store.KnowledgeAttempt, reason string) error {
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := run.st.RecordLoopFailure(diagnosticCtx, attempt, reason, loopNow().UTC())
	if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
		run.superseded(attempt)
		return nil
	}
	if err != nil {
		return err
	}
	run.report.Failed++
	return nil
}

func safeLoopRead(ctx context.Context, value []byte) (result loopcache.Result, err error) {
	defer func() {
		if recover() != nil {
			result, err = loopcache.Result{}, errors.New("loop: reader panicked")
		}
	}()
	return loopRead(ctx, value)
}
