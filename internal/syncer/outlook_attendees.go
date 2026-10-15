package syncer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookattendees"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmeetingattendees"
	"github.com/ourostack/m365crawl/internal/store"
)

type AttendeeOptions struct {
	DBPath, Root       string
	SourceID           int64
	Explicit, Discover bool
}

type AttendeeReport struct {
	Sources                           []store.KnowledgeSource
	Captured, Reused, Skipped, Failed int
}

var (
	attendeeCollect = collectAttendeeCapture
	attendeeIndex   = hxstore.OpenFile
	attendeeClose   = (*hxstore.Store).Close
)

type pendingAttendee struct {
	attempt  store.KnowledgeAttempt
	previous store.KnowledgeSource
	acquired time.Time
}

type stagedAttendee struct {
	pending pendingAttendee
	capture store.AttendeeCapture
	err     error
}

type attendeeTarget struct {
	profile           outlookdesktop.Profile
	locator, platform string
	request           store.AttendeeRequest
}

type attendeeFailure struct {
	coded *errs.Coded
	cause error
}

func (e *attendeeFailure) Error() string   { return e.coded.Error() }
func (e *attendeeFailure) Unwrap() []error { return []error{e.coded, e.cause} }

func attendeeError(code string) *errs.Coded {
	return &errs.Coded{Code: code, Exit: errs.ExitEnvironment, Message: "native Outlook attendee evidence could not be associated safely",
		Fix: "Retry the same explicit calendar attendees sync request against the selected archive. Existing calendar and mail are retained."}
}

func attendeeCode(cause error) string {
	switch {
	case errors.Is(cause, store.ErrAttendeeArchiveCorrupt):
		return "outlook_attendee_archive_corrupt"
	case errors.Is(cause, errPeopleCleanup):
		return "outlook_attendee_cleanup_failed"
	case errors.Is(cause, outlookmeetingattendees.ErrTooLarge):
		return "outlook_attendee_evidence_too_large"
	case errors.Is(cause, outlookmeetingattendees.ErrInvalid):
		return "outlook_attendee_evidence_invalid"
	case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
		return "outlook_attendee_cancelled"
	}
	var native *outlookattendees.Error
	if errors.As(cause, &native) {
		return native.Code
	}
	var coded *errs.Coded
	if errors.As(cause, &coded) {
		return coded.Code
	}
	if cause != nil && cause.Error() == "outlook_attendee_selection_too_large" {
		return cause.Error()
	}
	return "outlook_attendees_read_failed"
}

func collectAttendeeCapture(ctx context.Context, info outlookdesktop.Info, selections *outlookcal.AttendeeSelections) (outlookmeetingattendees.Evidence, outlookmeetingattendees.Census, string, error) {
	if selections == nil {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", attendeeError("outlook_attendee_calendar_unassociated")
	}
	if selections.Err != nil {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", selections.Err
	}
	index, err := attendeeIndex(info.Path)
	if err != nil {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", err
	}
	native, readErr := func() (result outlookattendees.Result, returnErr error) {
		defer func() { returnErr = errors.Join(returnErr, attendeeClose(index)) }()
		return outlookattendees.Collect(ctx, index)
	}()
	if err := readErr; err != nil {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", err
	}
	evidence, census, err := outlookmeetingattendees.Build(ctx, native, *selections)
	if err != nil {
		return outlookmeetingattendees.Evidence{}, outlookmeetingattendees.Census{}, "", err
	}
	digest, err := hashOutlookCapture(ctx, info.Path)
	return evidence, census, digest, err
}

func hashOutlookCapture(ctx context.Context, path string) (digest string, returnErr error) {
	file, err := peopleHashOpen(path)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close(), ctx.Err())
		if returnErr != nil {
			digest = ""
		}
	}()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func stageAttendee(ctx context.Context, info outlookdesktop.Info, selections *outlookcal.AttendeeSelections, readID int64, pending pendingAttendee, cause error) (stage stagedAttendee) {
	stage.pending = pending
	stage.err = cause
	if cause != nil {
		return stage
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		if recover() != nil {
			stage.capture = store.AttendeeCapture{}
			stage.err = attendeeError("outlook_attendee_evidence_invalid")
		}
	}()
	stage.capture.Evidence, stage.capture.Census, stage.capture.InputDigest, stage.err = attendeeCollect(readCtx, info, selections)
	stage.err = errors.Join(stage.err, readCtx.Err())
	stage.capture.CalendarReadID, stage.capture.AcquiredAt, stage.capture.SourceObservedAt = readID, pending.acquired, &info.ModTime
	return stage
}

func recordAttendeeFailure(ctx context.Context, s *store.Store, a store.KnowledgeAttempt, cause error) error {
	status := "failed"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = "cancelled"
	}
	diagnostic, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.RecordAttendeeFailure(diagnostic, a, store.KnowledgeOutcome{Status: status, Reason: attendeeCode(cause), At: outlookNow()})
}

func finishAttendee(ctx context.Context, s *store.Store, stage stagedAttendee, common error) error {
	cause := errors.Join(stage.err, common, ctx.Err())
	if cause == nil {
		stage.capture.AdmittedAt = outlookNow()
		cause = s.CommitAttendeeCapture(ctx, stage.pending.attempt, stage.capture)
	}
	if cause == nil || errors.Is(cause, store.ErrStaleKnowledgeAttempt) {
		return cause
	}
	return &attendeeFailure{coded: attendeeError(attendeeCode(cause)), cause: errors.Join(cause, recordAttendeeFailure(ctx, s, stage.pending.attempt, cause))}
}

func selectedAttendeeSources(ctx context.Context, s *store.Store, id int64, locators map[string]bool) ([]store.KnowledgeSource, error) {
	sources, err := s.KnowledgeSources(ctx, store.AttendeeKind)
	if err != nil {
		return nil, err
	}
	out := []store.KnowledgeSource{}
	for _, source := range sources {
		if id > 0 && source.ID != id || locators != nil && !locators[source.Locator] {
			continue
		}
		out = append(out, source)
	}
	if id > 0 && len(out) == 0 {
		return nil, store.ErrKnowledgeSourceUnknown
	}
	return out, nil
}

func attendeeTargets(ctx context.Context, s *store.Store, options AttendeeOptions) ([]attendeeTarget, error) {
	if options.SourceID > 0 {
		sources, err := s.KnowledgeKindLocators(ctx, store.AttendeeKind, options.SourceID)
		if err != nil {
			return nil, err
		}
		view, err := s.AttendeeSources(ctx, options.SourceID)
		if err != nil {
			return nil, err
		}
		if len(sources) != 1 || len(view.Coverage.Sources) != 1 || !strings.HasPrefix(view.Coverage.Sources[0].Account, "outlook/") {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		source := sources[0]
		name := strings.TrimPrefix(view.Coverage.Sources[0].Account, "outlook/")
		return []attendeeTarget{{profile: outlookdesktop.Profile{Name: name, StorePath: source.Locator}, locator: source.Locator, platform: source.Platform,
			request: store.AttendeeRequest{SourceID: options.SourceID}}}, nil
	}
	root := options.Root
	var err error
	if root == "" {
		root, err = peopleDefault()
		if err != nil {
			return nil, attendeeError("outlook_attendee_origin_unknown")
		}
	}
	root, err = peopleResolve(root)
	if err != nil {
		return nil, attendeeError("outlook_attendee_origin_unknown")
	}
	root, err = peopleAbs(root)
	if err != nil {
		return nil, attendeeError("outlook_attendee_origin_unknown")
	}
	profiles, _, skipped, err := peopleDiscover(root)
	if err != nil || len(skipped) > 0 || len(profiles) == 0 {
		return nil, attendeeError("outlook_attendee_origin_unknown")
	}
	out := []attendeeTarget{}
	for _, profile := range profiles {
		origin := outlookSourceOrigin(profile.StorePath)
		if origin == "" {
			return nil, attendeeError("outlook_attendee_origin_unknown")
		}
		out = append(out, attendeeTarget{profile: profile, locator: origin, platform: runtime.GOOS, request: store.AttendeeRequest{Root: root, Discover: true}})
	}
	return out, nil
}

func SyncAttendees(ctx context.Context, options AttendeeOptions) (AttendeeReport, error) {
	report := AttendeeReport{Sources: []store.KnowledgeSource{}}
	if options.SourceID < 0 || (options.Root != "" && options.SourceID > 0) || (!options.Explicit && (options.Discover || options.Root != "")) {
		return report, errs.Usage("attendee sync requires an explicit root or positive source selector, never both")
	}
	if !options.Explicit {
		s, err := peopleOpenRead(ctx, options.DBPath)
		if errors.Is(err, store.ErrNoArchive) {
			return report, nil
		}
		if err != nil {
			return report, err
		}
		report.Sources, err = selectedAttendeeSources(ctx, s, options.SourceID, nil)
		return report, errors.Join(err, s.Close())
	}
	release, err := peopleAcquire(options.DBPath)
	if err != nil {
		return report, err
	}
	defer release()
	s, err := peopleOpen(ctx, options.DBPath)
	if err != nil {
		return report, err
	}
	report, err = syncAttendees(ctx, s, options)
	return report, errors.Join(err, s.Close())
}

func syncAttendees(ctx context.Context, s *store.Store, options AttendeeOptions) (AttendeeReport, error) {
	report := AttendeeReport{Sources: []store.KnowledgeSource{}}
	targets, err := attendeeTargets(ctx, s, options)
	if err != nil {
		return report, err
	}
	participating := map[string]bool{}
	var failures error
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(failures, err)
		}
		participating[target.locator] = true
		status, err := captureAttendee(ctx, s, target)
		switch status {
		case "captured":
			report.Captured++
		case "reused":
			report.Reused++
		case "skipped":
			report.Skipped++
		default:
			report.Failed++
		}
		failures = errors.Join(failures, err)
	}
	report.Sources, err = selectedAttendeeSources(ctx, s, options.SourceID, participating)
	return report, errors.Join(failures, err)
}

func captureAttendee(ctx context.Context, s *store.Store, target attendeeTarget) (string, error) {
	begun := outlookNow()
	account := outlookAccount(target.profile.Name)
	origin := outlookSourceOrigin(target.profile.StorePath)
	if origin == "" || origin != target.locator {
		return "failed", attendeeError("outlook_attendee_origin_unknown")
	}
	state, err := s.CheckAttendeeOrigin(ctx, account, origin)
	if err != nil {
		return "failed", err
	}
	if state == "conflict" {
		return "failed", attendeeError("outlook_attendee_origin_conflict")
	}
	prior, err := s.OutlookState(ctx, target.profile.Name, outlookKey(target.profile.Name), account)
	if err != nil {
		return "failed", err
	}
	if !prior.LastAttempt.After(begun) && begun.Before(prior.LastAttempt.Add(OutlookMinReadInterval)) {
		return "skipped", nil
	}
	signature, err := outlookdesktop.FingerprintOf(target.profile.StorePath, outlookVersions())
	if err != nil {
		return "failed", attendeeError("outlook_attendee_origin_unknown")
	}
	previous, err := selectedAttendeeSources(ctx, s, 0, map[string]bool{origin: true})
	if err != nil {
		return "failed", err
	}
	if len(previous) == 1 && previous[0].Status == "partial" && previous[0].AttemptID == previous[0].AdmittedID && previous[0].AcquisitionSignature == signature {
		view, err := s.AttendeeSources(ctx, previous[0].ID)
		if err != nil {
			return "failed", err
		}
		if len(view.Coverage.Sources) == 1 {
			scope := view.Coverage.Sources[0]
			if scope.OriginState == "matched" && scope.CalendarState == "matched" && (scope.LinkState == "linked" || scope.LinkState == "source_local_unlinked") {
				return "skipped", nil
			}
		}
	}
	if err := s.SetOutlookChecked(ctx, target.profile.Name, begun); err != nil {
		return "failed", err
	}
	if err := s.SetOutlookLastAttempt(ctx, target.profile.Name, begun); err != nil {
		return "failed", err
	}
	attempt, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Platform: target.platform, Locator: origin, AcquisitionSignature: signature, At: begun}, target.request)
	if err != nil {
		return "failed", err
	}
	captureCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	info, cleanup, captureErr := outlookSnapshot(captureCtx, target.profile.StorePath)
	pending := pendingAttendee{attempt: attempt, acquired: outlookNow()}
	var selections *outlookcal.AttendeeSelections
	var readID int64
	if captureErr == nil {
		if after := outlookSourceOrigin(target.profile.StorePath); after == "" {
			captureErr = attendeeError("outlook_attendee_origin_unknown")
		} else if after != origin {
			captureErr = attendeeError("outlook_attendee_origin_conflict")
		}
	}
	if captureErr == nil {
		captureErr = s.ClearMailRead(captureCtx, account)
	}
	if captureErr == nil {
		r := runner{st: s}
		_, _, selections, readID, captureErr = r.outlookCalendarEvidence(captureCtx, info, &Report{}, prior, outlookKey(target.profile.Name), account, signature, origin, begun, true)
	}
	if captureErr == nil {
		_, captureErr = s.AutoLinkOutlook(captureCtx, begun)
	}
	stage := stageAttendee(captureCtx, info, selections, readID, pending, captureErr)
	var common error
	if info.Path == "" {
		cleanup()
	} else {
		common = peopleCleanupVerified(info, cleanup)
	}
	err = finishAttendee(captureCtx, s, stage, common)
	if err != nil {
		return "failed", err
	}
	reused, err := s.AttendeeCaptureReused(ctx, attempt)
	if err != nil {
		return "failed", err
	}
	if reused {
		return "reused", nil
	}
	return "captured", nil
}

func beginSharedAttendee(ctx context.Context, s *store.Store, path, signature string, at time.Time, full bool) (*pendingAttendee, error) {
	sources, err := s.KnowledgeKindLocators(ctx, store.AttendeeKind, 0)
	if err != nil || len(sources) == 0 {
		return nil, err
	}
	origin := outlookSourceOrigin(path)
	if origin == "" {
		return nil, attendeeError("outlook_attendee_origin_unknown")
	}
	for _, source := range sources {
		if source.Locator != origin {
			continue
		}
		if !full && source.AttemptedAt != nil && at.Sub(*source.AttemptedAt) >= 0 && at.Sub(*source.AttemptedAt) < OutlookMinReadInterval {
			return nil, nil
		}
		request, err := s.AttendeeRequestForLocator(ctx, source.LocatorID)
		if err != nil {
			return nil, err
		}
		attempt, err := s.BeginAttendeeAttempt(ctx, store.KnowledgeInput{Kind: store.AttendeeKind, Platform: source.Platform, Locator: origin, AcquisitionSignature: signature, At: at}, request)
		if err != nil {
			return nil, err
		}
		return &pendingAttendee{attempt: attempt, previous: source}, nil
	}
	return nil, nil
}

func optionalAttendeeOutcome(profile string, cause error) outlookOutcome {
	key := "outlook|" + profile + "|attendees"
	report := SourceReport{Source: key, Status: "partial"}
	if cause != nil {
		coded := attendeeError(attendeeCode(cause))
		report.Status = "failed"
		report.Error = &SourceError{Code: coded.Code, Message: coded.Message, Fix: coded.Fix}
	}
	return outlookOutcome{key: key, report: report, optional: true}
}
