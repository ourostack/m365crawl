package syncer

import (
	"cmp"
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
)

type DocumentOptions struct {
	DBPath, Root       string
	SourceID           int64
	MaxAge             time.Duration
	Explicit, Discover bool
}

type DocumentReport struct {
	Sources                              []store.KnowledgeSource
	Captured, Unchanged, Skipped, Failed int
}

var (
	documentsNow      = time.Now
	documentsPlatform = runtime.GOOS
	documentsDiscover = officecapture.DiscoverJSON
	documentsObserve  = officecapture.ObserveJSON
	documentsCapture  = officecapture.WithJSONCapture
	documentsOpen     = store.Open
	documentsRead     = store.OpenReadOnly
	documentsAcquire  = store.AcquireLock
)

func readDocumentSources(ctx context.Context, options DocumentOptions) (DocumentReport, error) {
	st, err := documentsRead(ctx, options.DBPath)
	if errors.Is(err, store.ErrNoArchive) {
		if options.SourceID != 0 {
			return DocumentReport{}, store.ErrKnowledgeSourceUnknown
		}
		return DocumentReport{Sources: []store.KnowledgeSource{}}, nil
	}
	if err != nil {
		return DocumentReport{}, err
	}
	sources, readErr := selectedDocumentSources(ctx, st, options.SourceID, nil)
	return DocumentReport{Sources: sources}, errors.Join(readErr, st.Close())
}

func selectedDocumentSources(ctx context.Context, st *store.Store, sourceID int64, participating map[string]bool) ([]store.KnowledgeSource, error) {
	sources, err := st.DocumentSources(ctx)
	if err != nil {
		return nil, err
	}
	out := []store.KnowledgeSource{}
	for _, source := range sources {
		if sourceID > 0 && source.ID != sourceID {
			continue
		}
		if participating != nil && !participating[source.Locator] {
			continue
		}
		out = append(out, source)
	}
	if sourceID != 0 && len(out) == 0 {
		return nil, store.ErrKnowledgeSourceUnknown
	}
	return out, nil
}

func SyncDocuments(ctx context.Context, options DocumentOptions) (DocumentReport, error) {
	if options.SourceID < 0 || options.MaxAge < 0 || (options.Root != "" && options.SourceID != 0) ||
		(options.Discover && !options.Explicit) || (options.Root != "" && !options.Discover) {
		return DocumentReport{}, errors.New("documents: invalid source selection")
	}
	if !options.Explicit {
		report, err := readDocumentSources(ctx, options)
		if err != nil || options.MaxAge == 0 || len(report.Sources) == 0 {
			return report, err
		}
	}
	release, err := documentsAcquire(options.DBPath)
	if err != nil {
		return DocumentReport{}, err
	}
	defer release()
	st, err := documentsOpen(ctx, options.DBPath)
	if err != nil {
		return DocumentReport{}, err
	}
	report, runErr := syncDocuments(ctx, st, options)
	return report, errors.Join(runErr, st.Close())
}

func syncDocuments(ctx context.Context, st *store.Store, options DocumentOptions) (DocumentReport, error) {
	report := DocumentReport{Sources: []store.KnowledgeSource{}}
	locators, err := st.DocumentLocators(ctx, options.SourceID)
	if err != nil {
		return report, err
	}
	inputs := map[string]officecapture.DocumentInput{}
	if options.Discover {
		discovered, err := documentsDiscover(ctx, options.Root)
		if err != nil {
			return report, err
		}
		if options.Root != "" {
			locators = slices.DeleteFunc(locators, func(source store.KnowledgeSource) bool {
				return filepath.Dir(source.Locator) != options.Root
			})
		}
		for _, input := range discovered {
			inputs[input.Path] = input
			found := slices.ContainsFunc(locators, func(source store.KnowledgeSource) bool {
				return source.Locator == input.Path && source.Kind == store.DocumentKind(input.Surface)
			})
			if !found {
				locators = append(locators, store.KnowledgeSource{Kind: store.DocumentKind(input.Surface),
					Platform: documentsPlatform, Locator: input.Path})
			}
		}
	}
	participating := map[string]bool{}
	slices.SortFunc(locators, func(a, b store.KnowledgeSource) int {
		if order := cmp.Compare(cmp.Compare(b.LocatorID, 0), cmp.Compare(a.LocatorID, 0)); order != 0 {
			return order
		}
		if a.LocatorID > 0 && b.LocatorID > 0 {
			return cmp.Compare(a.LocatorID, b.LocatorID)
		}
		return cmp.Compare(a.Locator, b.Locator)
	})
	for _, source := range locators {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		participating[source.Locator] = true
		input, discovered := inputs[source.Locator]
		if !discovered {
			surface, application, err := st.DocumentLocatorInput(ctx, source.LocatorID)
			if err != nil {
				return report, err
			}
			input = officecapture.DocumentInput{Path: source.Locator, Surface: surface, Application: application}
		}
		signature, observeErr := documentsObserve(ctx, source.Locator)
		now := documentsNow().UTC()
		recent := source.AttemptedAt != nil && now.Sub(*source.AttemptedAt) < options.MaxAge
		same := signature == source.AcquisitionSignature
		if !options.Explicit && recent && same {
			report.Skipped++
			continue
		}
		attempt, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: source.Kind, Platform: source.Platform,
			Locator: source.Locator, AcquisitionSignature: signature, At: now}, input.Surface, input.Application)
		if err != nil {
			return report, err
		}
		if observeErr != nil {
			if err := recordDocumentFailure(ctx, st, attempt, signature, observeErr); err != nil {
				return report, err
			}
			report.Failed++
			continue
		}
		status, err := captureDocuments(ctx, st, attempt, source, input, signature)
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
	report.Sources, err = selectedDocumentSources(ctx, st, options.SourceID, participating)
	return report, err
}

func captureDocuments(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, previous store.KnowledgeSource, input officecapture.DocumentInput, signature string) (string, error) {
	var result officedocuments.Result
	var metadata store.DocumentCaptureMetadata
	var digest, observedSignature string
	err := safeDocumentCapture(ctx, input, func(c *officecapture.JSONCapture) error {
		var err error
		result, err = c.ReadJSON(ctx)
		if err != nil {
			return err
		}
		digest, observedSignature = c.ContentDigest, c.AcquisitionSignature
		acquired, modified := c.AcquiredAt, c.SourceModified
		metadata = store.DocumentCaptureMetadata{Surface: input.Surface, Application: input.Application,
			AcquiredAt: &acquired, SourceObservedAt: &modified}
		return nil
	})
	if err != nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, err)
	}
	if digest == "" || metadata.AcquiredAt == nil || metadata.SourceObservedAt == nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, errors.New("documents: capture result unavailable"))
	}
	status, reason := "complete", ""
	if len(result.Losses) > 0 {
		status = "partial"
	} else if len(result.Documents) == 0 {
		reason = officedocuments.NoDocumentsListed
	}
	if previous.AdmittedID > 0 && previous.ContentDigest == digest && previous.Qualification == store.DocumentQualification {
		status = "unchanged"
		result = officedocuments.Result{}
	}
	outcome := store.KnowledgeOutcome{Status: status, Reason: reason, ContentDigest: digest, AcquisitionSignature: observedSignature,
		Qualification: store.DocumentQualification, SourceObservedAt: *metadata.SourceObservedAt, At: documentsNow().UTC()}
	if err := st.CommitDocumentObservations(ctx, attempt, outcome, result, metadata); err != nil {
		if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			return "", err
		}
		return "", errors.Join(err, recordDocumentFailure(ctx, st, attempt, observedSignature, err))
	}
	return status, nil
}

func safeDocumentCapture(ctx context.Context, input officecapture.DocumentInput, consume func(*officecapture.JSONCapture) error) (result error) {
	defer func() {
		if recover() != nil {
			result = &officecapture.ReadError{Code: "office_json_capture_failed"}
		}
	}()
	return documentsCapture(ctx, input, consume)
}

func recordDocumentFailure(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, signature string, cause error) error {
	status, reason := "failed", "office_json_capture_failed"
	var typed *officecapture.ReadError
	if errors.As(cause, &typed) && slices.Contains([]string{"office_json_input_unqualified", "office_json_capture_failed",
		"office_json_cleanup_failed", "office_json_policy_restore_failed", "office_surface_unknown", "office_json_unreadable",
		"office_json_too_large", "office_json_unrecognized"}, typed.Code) {
		reason = typed.Code
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status, reason = "cancelled", "office_json_cancelled"
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return st.RecordKnowledgeFailure(diagnosticCtx, attempt, store.KnowledgeOutcome{
		Status: status, Reason: reason, AcquisitionSignature: signature, At: documentsNow().UTC()})
}
