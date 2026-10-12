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
}

// LoopReport is what a refresh did. Sources are the participating registrations after it;
// Diagnostics hold bounded fixed-code lines (an identity-invalid record by its numeric ID, a
// superseded attempt by its registration), never archived strings.
type LoopReport struct {
	Sources                               []store.LoopSource
	Projected, Unchanged, Failed, Skipped int
	Diagnostics                           []string
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
	if !options.Explicit {
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
	report, runErr := syncLoop(ctx, st, options)
	if err := errors.Join(runErr, st.Close()); err != nil {
		return LoopReport{}, err
	}
	return report, nil
}

// loopDue says whether an ordinary read may refresh the source: its last attempt is older than maxAge.
func loopDue(source store.LoopSource, maxAge time.Duration) bool {
	return source.Health.AttemptedAt == nil || loopNow().Sub(*source.Health.AttemptedAt) >= maxAge
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
	sources, readErr := selectedLoopSources(ctx, st, options.SourceID, nil)
	if err := errors.Join(readErr, st.Close()); err != nil {
		return LoopReport{}, err
	}
	return LoopReport{Sources: sources}, nil
}

// loopStore is the part of the archive a Loop refresh uses; tests wrap it to inject failures.
type loopStore interface {
	LoopSources(context.Context) ([]store.LoopSource, error)
	LoopRawFrame(context.Context, int64) (store.LoopAcquisition, error)
	LoopDiscoveryFrames(context.Context) (store.LoopDiscovery, error)
	BeginLoopAttempt(context.Context, store.LoopRegistration, time.Time) (store.KnowledgeAttempt, error)
	CommitLoopObservations(context.Context, store.KnowledgeAttempt, store.LoopRawFrame, loopcache.Result, time.Time) error
	RecordLoopFailure(context.Context, store.KnowledgeAttempt, string, time.Time) error
}

func selectedLoopSources(ctx context.Context, st loopStore, sourceID int64, participating map[int64]bool) ([]store.LoopSource, error) {
	sources, err := st.LoopSources(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.LoopSource{}
	for _, source := range sources {
		id := source.Registration.ID
		if (sourceID == 0 || id == sourceID) && (participating == nil || participating[id]) {
			out = append(out, source)
		}
	}
	if sourceID != 0 && len(out) == 0 {
		return nil, store.ErrKnowledgeSourceUnknown
	}
	return out, nil
}

type loopRun struct {
	st            loopStore
	options       LoopOptions
	report        LoopReport
	participating map[int64]bool
}

func syncLoop(ctx context.Context, st loopStore, options LoopOptions) (LoopReport, error) {
	run := &loopRun{st: st, options: options, participating: map[int64]bool{}, report: LoopReport{Diagnostics: []string{}}}
	registered, err := selectedLoopSources(ctx, st, options.SourceID, nil)
	if err != nil {
		return LoopReport{}, err
	}
	for _, source := range registered {
		if err := ctx.Err(); err != nil {
			return LoopReport{}, err
		}
		run.participating[source.Registration.ID] = true
		if !options.Explicit && !loopDue(source, options.MaxAge) {
			run.report.Skipped++
			continue
		}
		acquisition, err := st.LoopRawFrame(ctx, source.Registration.ID)
		if err != nil {
			return LoopReport{}, err
		}
		if err := run.project(ctx, acquisition, source); err != nil {
			return LoopReport{}, err
		}
	}
	if options.Discover {
		discovery, err := st.LoopDiscoveryFrames(ctx)
		if err != nil {
			return LoopReport{}, err
		}
		for _, candidate := range discovery.Candidates {
			if err := ctx.Err(); err != nil {
				return LoopReport{}, err
			}
			if err := run.project(ctx, candidate, store.LoopSource{}); err != nil {
				return LoopReport{}, err
			}
		}
	}
	run.report.Sources, err = selectedLoopSources(ctx, st, options.SourceID, run.participating)
	if err != nil {
		return LoopReport{}, err
	}
	return run.report, nil
}

// project carries one acquisition through attempt, pure parsing and publication. Source-local
// refusals and reader failures become fixed failed attempts that keep the last good snapshot;
// cancellation and archive errors are returned.
func (run *loopRun) project(ctx context.Context, acquisition store.LoopAcquisition, previous store.LoopSource) error {
	if acquisition.Registration == nil {
		run.report.Diagnostics = append(run.report.Diagnostics, fmt.Sprintf("%s: record %d", acquisition.RefusalCode, acquisition.RecordID))
		return nil
	}
	attempt, err := run.st.BeginLoopAttempt(ctx, *acquisition.Registration, loopNow().UTC())
	if err != nil {
		return err
	}
	run.participating[attempt.LocatorID] = true
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
