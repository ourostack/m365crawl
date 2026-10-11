package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookpeople"
	"github.com/ourostack/m365crawl/internal/store"
)

type PeopleOptions struct {
	DBPath, Root       string
	SourceID           int64
	MaxAge             time.Duration
	Explicit, Discover bool
}

type PeopleReport struct {
	Sources                              []store.KnowledgeSource
	Captured, Unchanged, Skipped, Failed int
}

var (
	peopleCollect  = collectPeopleCapture
	peopleSnapshot = outlookdesktop.Snapshot
	peopleNow      = time.Now
	peopleStat     = os.Lstat
	peopleAbs      = filepath.Abs
	peopleResolve  = filepath.EvalSymlinks
	peopleDefault  = outlookdesktop.DefaultRoot
	peopleDiscover = outlookdesktop.Discover
	peopleOpen     = store.Open
	peopleOpenRead = store.OpenReadOnly
	peopleAcquire  = store.AcquireLock
	peopleIndex    = hxstore.OpenFile
	peopleHashOpen = func(path string) (io.ReadCloser, error) {
		return os.Open(path) //nolint:gosec // G304: the qualified private capture path, never an application file
	}
)

var errPeopleCleanup = errors.New("outlook_people_snapshot_cleanup_unverified")

type pendingPeople struct {
	attempt   store.KnowledgeAttempt
	previous  store.KnowledgeSource
	signature string
	acquired  time.Time
}

func beginSharedPeople(ctx context.Context, s *store.Store, path, signature string, at time.Time, full bool) (*pendingPeople, error) {
	locators, err := s.KnowledgeKindLocators(ctx, "outlook_people", 0)
	if err != nil {
		return nil, err
	}
	canonical, err := peopleResolve(path)
	if err != nil {
		return nil, err
	}
	canonical, err = peopleAbs(canonical)
	if err != nil {
		return nil, err
	}
	for _, source := range locators {
		if source.Locator != canonical {
			continue
		}
		if !full && source.AttemptedAt != nil && at.Sub(*source.AttemptedAt) >= 0 && at.Sub(*source.AttemptedAt) < OutlookMinReadInterval {
			return nil, nil
		}
		attempt, err := s.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "outlook_people", Platform: source.Platform,
			Locator: source.Locator, AcquisitionSignature: signature, At: at})
		if err != nil {
			return nil, err
		}
		return &pendingPeople{attempt: attempt, previous: source, signature: signature}, nil
	}
	return nil, nil
}

func optionalPeopleOutcome(profile string, cause error) outlookOutcome {
	key := "outlook|" + profile + "|people"
	report := SourceReport{Source: key, Status: "complete"}
	if cause != nil {
		coded := codedOf(cause)
		report.Status = "failed"
		report.Error = &SourceError{Code: coded.Code, Message: bodyMessage(coded), Fix: coded.Fix}
	}
	return outlookOutcome{key: key, report: report, optional: true}
}

func finishSharedPeople(ctx context.Context, s *store.Store, profile string, info outlookdesktop.Info, cleanup func(), captureErr error, pending pendingPeople) outlookOutcome {
	if captureErr != nil {
		cleanup()
		err := recordPeopleFailure(ctx, s, pending.attempt, pending.signature, captureErr, pending.previous.Locator)
		return optionalPeopleOutcome(profile, errors.Join(captureErr, err))
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, digest, readErr := peopleCollect(readCtx, info)
	cause := errors.Join(readErr, peopleCleanupVerified(info, cleanup), readCtx.Err())
	if cause != nil {
		err := recordPeopleFailure(ctx, s, pending.attempt, pending.signature, cause, pending.previous.Locator)
		return optionalPeopleOutcome(profile, errors.Join(cause, err))
	}
	status := "complete"
	if len(result.Losses) > 0 {
		status = "partial"
	}
	if digest == pending.previous.ContentDigest && pending.previous.Qualification == store.PeopleQualification {
		status = "unchanged"
	}
	link, err := s.PeopleProfileLink(ctx, "outlook/"+profile)
	if err != nil {
		diagnosticErr := recordPeopleFailure(ctx, s, pending.attempt, pending.signature, err, pending.previous.Locator)
		return optionalPeopleOutcome(profile, errors.Join(err, diagnosticErr))
	}
	err = s.CommitPeopleObservations(ctx, pending.attempt, store.KnowledgeOutcome{Status: status, ContentDigest: digest,
		AcquisitionSignature: pending.signature, Qualification: store.PeopleQualification, SourceObservedAt: info.ModTime, At: peopleNow().UTC()},
		result, store.PeopleCaptureMetadata{AcquiredAt: &pending.acquired, SourceObservedAt: &info.ModTime, Link: link})
	if err != nil {
		if !errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			err = errors.Join(err, recordPeopleFailure(ctx, s, pending.attempt, pending.signature, err, pending.previous.Locator))
		}
		return optionalPeopleOutcome(profile, err)
	}
	outcome := optionalPeopleOutcome(profile, nil)
	outcome.report.Status = status
	return outcome
}

func readPeopleSources(ctx context.Context, options PeopleOptions) (PeopleReport, error) {
	s, err := peopleOpenRead(ctx, options.DBPath)
	if errors.Is(err, store.ErrNoArchive) {
		return PeopleReport{Sources: []store.KnowledgeSource{}}, nil
	}
	if err != nil {
		return PeopleReport{}, err
	}
	sources, err := selectedPeopleSources(ctx, s, options.SourceID, nil)
	return PeopleReport{Sources: sources}, errors.Join(err, s.Close())
}

func selectedPeopleSources(ctx context.Context, s *store.Store, sourceID int64, locators map[string]bool) ([]store.KnowledgeSource, error) {
	sources, err := s.KnowledgeSources(ctx, "outlook_people")
	if err != nil {
		return nil, err
	}
	out := []store.KnowledgeSource{}
	for _, source := range sources {
		if sourceID > 0 && source.ID != sourceID || locators != nil && !locators[source.Locator] {
			continue
		}
		out = append(out, source)
	}
	if sourceID > 0 && len(out) == 0 {
		return nil, store.ErrKnowledgeSourceUnknown
	}
	return out, nil
}

func SyncPeople(ctx context.Context, options PeopleOptions) (PeopleReport, error) {
	if !options.Explicit {
		prior, err := readPeopleSources(ctx, options)
		if err != nil || options.MaxAge == 0 || len(prior.Sources) == 0 {
			return prior, err
		}
	}
	release, err := peopleAcquire(options.DBPath)
	if err != nil {
		return PeopleReport{}, err
	}
	defer release()
	s, err := peopleOpen(ctx, options.DBPath)
	if err != nil {
		return PeopleReport{}, err
	}
	report, err := syncPeople(ctx, s, options)
	return report, errors.Join(err, s.Close())
}

func syncPeople(ctx context.Context, s *store.Store, options PeopleOptions) (PeopleReport, error) {
	report := PeopleReport{Sources: []store.KnowledgeSource{}}
	locators, err := s.KnowledgeKindLocators(ctx, "outlook_people", options.SourceID)
	if err != nil {
		return report, err
	}
	if options.Discover {
		if !options.Explicit {
			return report, errors.New("people: discovery requires explicit sync")
		}
		root := options.Root
		if root == "" {
			root, err = peopleDefault()
			if err != nil {
				return report, err
			}
		}
		root, err = peopleResolve(root)
		if err != nil {
			return report, err
		}
		root, err = peopleAbs(root)
		if err != nil {
			return report, err
		}
		profiles, _, skipped, err := peopleDiscover(root)
		if err != nil {
			return report, err
		}
		if options.SourceID == 0 {
			retained := []store.KnowledgeSource{}
			for _, locator := range locators {
				if filepath.Dir(filepath.Dir(locator.Locator)) == root {
					retained = append(retained, locator)
				}
			}
			locators = retained
		}
		paths := []string{}
		for _, profile := range profiles {
			paths = append(paths, profile.StorePath)
		}
		for _, profile := range skipped {
			paths = append(paths, filepath.Join(profile.Dir, outlookdesktop.StoreFileName))
		}
		for _, path := range paths {
			found := false
			for _, locator := range locators {
				found = found || locator.Locator == path
			}
			if !found && options.SourceID == 0 {
				locators = append(locators, store.KnowledgeSource{Kind: "outlook_people", Platform: runtime.GOOS, Locator: path})
			}
		}
	}
	sort.Slice(locators, func(i, j int) bool { return locators[i].Locator < locators[j].Locator })
	participating := map[string]bool{}
	for _, source := range locators {
		participating[source.Locator] = true
		if err := ctx.Err(); err != nil {
			return report, err
		}
		now := peopleNow().UTC()
		age := time.Duration(0)
		if source.AttemptedAt != nil {
			age = now.Sub(*source.AttemptedAt)
		}
		signature, observeErr := outlookdesktop.FingerprintOf(source.Locator, outlookVersions())
		if !options.Explicit && source.AttemptedAt != nil && age >= 0 &&
			(age < OutlookMinReadInterval || age < options.MaxAge && signature == source.AcquisitionSignature) {
			report.Skipped++
			continue
		}
		attempt, err := s.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "outlook_people", Platform: source.Platform,
			Locator: source.Locator, AcquisitionSignature: signature, At: now})
		if err != nil {
			return report, err
		}
		if observeErr != nil {
			if err := recordPeopleFailure(ctx, s, attempt, signature, observeErr, source.Locator); err != nil {
				return report, err
			}
			report.Failed++
			continue
		}
		status, err := capturePeople(ctx, s, attempt, source, signature)
		if err != nil {
			return report, err
		}
		switch status {
		case "complete", "partial":
			report.Captured++
		case "unchanged":
			report.Unchanged++
		default:
			report.Failed++
		}
	}
	report.Sources, err = selectedPeopleSources(ctx, s, options.SourceID, participating)
	return report, err
}

func collectPeopleCapture(ctx context.Context, info outlookdesktop.Info) (outlookpeople.Result, string, error) {
	s, err := peopleIndex(info.Path)
	if err != nil {
		return outlookpeople.Result{}, "", err
	}
	result, readErr := outlookpeople.Collect(ctx, s)
	closeErr := s.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return outlookpeople.Result{}, "", err
	}
	f, err := peopleHashOpen(info.Path)
	if err != nil {
		return outlookpeople.Result{}, "", err
	}
	h := sha256.New()
	_, copyErr := io.Copy(h, f)
	closeErr = f.Close()
	if err := errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return outlookpeople.Result{}, "", err
	}
	return result, hex.EncodeToString(h.Sum(nil)), nil
}

func peopleCleanupVerified(info outlookdesktop.Info, cleanup func()) error {
	cleanup()
	for _, path := range []string{info.Path, filepath.Dir(info.Path)} {
		if _, err := peopleStat(path); !errors.Is(err, os.ErrNotExist) {
			return errPeopleCleanup
		}
	}
	return nil
}

func capturePeople(ctx context.Context, s *store.Store, attempt store.KnowledgeAttempt, previous store.KnowledgeSource, signature string) (string, error) {
	captureCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	info, cleanup, err := peopleSnapshot(captureCtx, previous.Locator)
	if err != nil {
		cleanup()
		return "failed", recordPeopleFailure(ctx, s, attempt, signature, err, previous.Locator)
	}
	acquired := peopleNow().UTC()
	result, digest, readErr := peopleCollect(captureCtx, info)
	cleanupErr := peopleCleanupVerified(info, cleanup)
	if err := errors.Join(readErr, cleanupErr, captureCtx.Err()); err != nil {
		return "failed", recordPeopleFailure(ctx, s, attempt, signature, err, previous.Locator)
	}
	status := "complete"
	if len(result.Losses) > 0 {
		status = "partial"
	}
	if previous.AdmittedID > 0 && previous.ContentDigest == digest && previous.Qualification == store.PeopleQualification {
		status = "unchanged"
	}
	link, err := s.PeopleProfileLink(ctx, "outlook/"+filepath.Base(filepath.Dir(previous.Locator)))
	if err != nil {
		return "", errors.Join(err, recordPeopleFailure(ctx, s, attempt, signature, err, previous.Locator))
	}
	err = s.CommitPeopleObservations(ctx, attempt, store.KnowledgeOutcome{Status: status, ContentDigest: digest,
		AcquisitionSignature: signature, Qualification: store.PeopleQualification, SourceObservedAt: info.ModTime, At: peopleNow().UTC()},
		result, store.PeopleCaptureMetadata{AcquiredAt: &acquired, SourceObservedAt: &info.ModTime, Link: link})
	if err != nil {
		if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			return "", err
		}
		return "", errors.Join(err, recordPeopleFailure(ctx, s, attempt, signature, err, previous.Locator))
	}
	return status, nil
}

func recordPeopleFailure(ctx context.Context, s *store.Store, attempt store.KnowledgeAttempt, signature string, cause error, path string) error {
	status := "failed"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = "cancelled"
	} else if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		status = "unavailable"
	}
	reason := "outlook_people_capture_unavailable"
	if errors.Is(cause, errPeopleCleanup) {
		reason = "outlook_people_snapshot_cleanup_unverified"
	}
	diagnostic, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.RecordKnowledgeFailure(diagnostic, attempt, store.KnowledgeOutcome{Status: status, Reason: reason,
		AcquisitionSignature: signature, At: peopleNow().UTC()}); err != nil {
		return errors.Join(cause, err)
	}
	return ctx.Err()
}
