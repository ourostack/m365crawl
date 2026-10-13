package syncer

import (
	"cmp"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/onedriveusage"
	"github.com/ourostack/m365crawl/internal/store"
)

// DocumentOptions selects what a document sync touches. Root (an Office JSON directory),
// RegistryFile (one Office registry SQLite file), UsageFile (one OneDrive usage SQLite file) and
// SourceID (one registered source) are mutually exclusive; with none of them, an explicit
// discovering sync tries every default family on its own.
type DocumentOptions struct {
	DBPath, Root, RegistryFile, UsageFile string
	SourceID                              int64
	MaxAge                                time.Duration
	Explicit, Discover                    bool
}

// DocumentDiscovery is one default discovery outcome with a fixed code: a family that was
// unavailable or refused, or, with Locator set, one named default location of a family that
// yielded no input. It has no source: nothing was registered or opened for it.
type DocumentDiscovery struct{ Kind, Code, Locator string }

// DocumentFamilyJSON names the Office JSON family in a DocumentDiscovery.
const DocumentFamilyJSON = "office_json"

type DocumentReport struct {
	Sources                              []store.KnowledgeSource
	Discovery                            []DocumentDiscovery
	Captured, Unchanged, Skipped, Failed int
}

var (
	documentsNow      = time.Now
	documentsPlatform = runtime.GOOS
	documentsDiscover = officecapture.DiscoverJSON
	documentsObserve  = officecapture.ObserveJSON
	documentsCapture  = officecapture.WithJSONCapture
	// The registry family's own discovery, observation and capture.
	documentsRegistryDiscover = officecapture.DiscoverRegistry
	documentsRegistryObserve  = officecapture.ObserveRegistry
	documentsRegistryCapture  = officecapture.WithRegistryCapture
	// The OneDrive usage family's own discovery, observation and capture.
	documentsUsageDiscover = officecapture.DiscoverOneDriveUsage
	documentsUsageObserve  = officecapture.ObserveOneDriveUsage
	documentsUsageCapture  = officecapture.WithOneDriveUsageCapture
	documentsOpen          = store.Open
	documentsRead          = store.OpenReadOnly
	documentsAcquire       = store.AcquireLock
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

// participant keys a locator by kind as well as path: one path may be registered under two kinds.
func participant(source store.KnowledgeSource) string { return source.Kind + "\x00" + source.Locator }

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
		if participating != nil && !participating[participant(source)] {
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
	selections := 0
	supplied := options.Root != "" || options.RegistryFile != "" || options.UsageFile != ""
	for _, selected := range []bool{options.Root != "", options.RegistryFile != "", options.UsageFile != "", options.SourceID != 0} {
		if selected {
			selections++
		}
	}
	if options.SourceID < 0 || options.MaxAge < 0 || selections > 1 ||
		(options.Discover && !options.Explicit) || (supplied && !options.Discover) {
		return DocumentReport{}, errors.New("documents: invalid source selection")
	}
	if options.RegistryFile != "" {
		if err := CheckRegistryFile(options.DBPath, options.RegistryFile); err != nil {
			return DocumentReport{}, err
		}
	}
	if options.UsageFile != "" {
		if err := CheckUsageFile(options.DBPath, options.UsageFile); err != nil {
			return DocumentReport{}, err
		}
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
	addJSON := func(discovered []officecapture.DocumentInput) {
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
	// addFile adds one SQLite file of a single-file family unless it is already registered.
	addFile := func(kind, path string) {
		found := slices.ContainsFunc(locators, func(source store.KnowledgeSource) bool {
			return source.Locator == path && source.Kind == kind
		})
		if !found {
			locators = append(locators, store.KnowledgeSource{Kind: kind, Platform: documentsPlatform, Locator: path})
		}
	}
	// onlyFile keeps the one supplied file of a single-file family: never another family, another
	// file or a default location.
	onlyFile := func(kind, path string) {
		locators = slices.DeleteFunc(locators, func(source store.KnowledgeSource) bool {
			return source.Kind != kind || source.Locator != path
		})
		addFile(kind, path)
	}
	switch {
	case !options.Discover:
	case options.RegistryFile != "":
		onlyFile(store.RegistryDocumentKind, options.RegistryFile)
	case options.UsageFile != "":
		onlyFile(store.UsageDocumentKind, options.UsageFile)
	case options.Root != "":
		// JSON-only: the supplied directory, never a single-file family.
		discovered, err := documentsDiscover(ctx, options.Root)
		if err != nil {
			return report, err
		}
		locators = slices.DeleteFunc(locators, func(source store.KnowledgeSource) bool {
			return fileFamilies[source.Kind] != nil || filepath.Dir(source.Locator) != options.Root
		})
		addJSON(discovered)
	default:
		// Each default family is tried on its own: one absent or refused family is a fixed
		// outcome in the report and never suppresses the others or the registered sources.
		discovered, err := documentsDiscover(ctx, "")
		if outcome, err := discoveryOutcome(ctx, DocumentFamilyJSON, err); err != nil {
			return report, err
		} else if outcome != nil {
			report.Discovery = append(report.Discovery, *outcome)
		}
		addJSON(discovered)
		registry, err := documentsRegistryDiscover(ctx)
		if outcome, err := discoveryOutcome(ctx, store.RegistryDocumentKind, err); err != nil {
			return report, err
		} else if outcome != nil {
			report.Discovery = append(report.Discovery, *outcome)
		}
		for _, input := range registry {
			addFile(store.RegistryDocumentKind, input.Path)
		}
		usage, err := documentsUsageDiscover(ctx)
		if outcome, err := discoveryOutcome(ctx, store.UsageDocumentKind, err); err != nil {
			return report, err
		} else if outcome != nil {
			report.Discovery = append(report.Discovery, *outcome)
		}
		for _, input := range usage.Inputs {
			addFile(store.UsageDocumentKind, input.Path)
		}
		for _, outcome := range usage.Outcomes {
			report.Discovery = append(report.Discovery, DocumentDiscovery{Kind: store.UsageDocumentKind, Code: outcome.Code, Locator: outcome.Locator})
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
		participating[participant(source)] = true
		file := fileFamilies[source.Kind]
		input, discovered := inputs[source.Locator]
		if file == nil && !discovered {
			surface, application, err := st.DocumentLocatorInput(ctx, source.LocatorID)
			if err != nil {
				return report, err
			}
			input = officecapture.DocumentInput{Path: source.Locator, Surface: surface, Application: application}
		}
		observe, family := documentsObserve, jsonFamily
		if file != nil {
			observe, family = file.observe(), file.documentFamily
		}
		signature, observeErr := observe(ctx, source.Locator)
		now := documentsNow().UTC()
		recent := source.AttemptedAt != nil && now.Sub(*source.AttemptedAt) < options.MaxAge
		same := signature == source.AcquisitionSignature
		// A registry or usage file can be hundreds of megabytes, and a capture holds its writer out
		// for the whole copy. A read's own refresh therefore never re-captures one whose observed
		// signature is the one already captured and whose last attempt settled, however old that
		// attempt is. An explicit sync, a changed signature and a failed attempt still capture.
		settled := file != nil && observeErr == nil && slices.Contains([]string{"complete", "partial", "unchanged"}, source.Status)
		if !options.Explicit && same && (recent || settled) {
			report.Skipped++
			continue
		}
		knowledge := store.KnowledgeInput{Kind: source.Kind, Platform: source.Platform,
			Locator: source.Locator, AcquisitionSignature: signature, At: now}
		var attempt store.KnowledgeAttempt
		if file != nil {
			attempt, err = file.begin(ctx, st, knowledge)
		} else {
			attempt, err = st.BeginDocumentAttempt(ctx, knowledge, input.Surface, input.Application)
		}
		if err != nil {
			return report, err
		}
		if observeErr != nil {
			if err := recordDocumentFailure(ctx, st, attempt, signature, observeErr, family); err != nil {
				return report, err
			}
			report.Failed++
			continue
		}
		var status string
		if file != nil {
			status, err = file.capture(ctx, st, attempt, source, signature)
		} else {
			status, err = captureDocuments(ctx, st, attempt, source, input, signature)
		}
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
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, err, jsonFamily)
	}
	if digest == "" || metadata.AcquiredAt == nil || metadata.SourceObservedAt == nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, errors.New("documents: capture result unavailable"), jsonFamily)
	}
	status, reason := "complete", ""
	if len(result.Losses) > 0 {
		status = "partial"
	} else if len(result.Documents) == 0 {
		reason = officedocuments.NoDocumentsListed
		if input.Surface == officedocuments.Dialog || input.Application == "supplied" {
			reason = officedocuments.FileListedNoDocuments
		}
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
		return "", errors.Join(err, recordDocumentFailure(ctx, st, attempt, observedSignature, err, jsonFamily))
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

// documentFamily holds the fixed failure reasons of one capture family.
type documentFamily struct {
	failed, cancelled string
	reasons           []string
}

// fileFamily is a capture family whose source is one SQLite file with no surface or application:
// how it is observed, registered and captured.
type fileFamily struct {
	documentFamily
	observe func() func(context.Context, string) (string, error)
	begin   func(context.Context, *store.Store, store.KnowledgeInput) (store.KnowledgeAttempt, error)
	capture func(context.Context, *store.Store, store.KnowledgeAttempt, store.KnowledgeSource, string) (string, error)
}

// fileFamilies are the single-file families by knowledge kind. observe is read at call time so a
// test seam set after start-up is honoured.
var fileFamilies = map[string]*fileFamily{
	store.RegistryDocumentKind: {documentFamily: registryFamily,
		observe: func() func(context.Context, string) (string, error) { return documentsRegistryObserve },
		begin: func(ctx context.Context, st *store.Store, input store.KnowledgeInput) (store.KnowledgeAttempt, error) {
			return st.BeginRegistryDocumentAttempt(ctx, input)
		}, capture: captureRegistry},
	store.UsageDocumentKind: {documentFamily: usageFamily,
		observe: func() func(context.Context, string) (string, error) { return documentsUsageObserve },
		begin: func(ctx context.Context, st *store.Store, input store.KnowledgeInput) (store.KnowledgeAttempt, error) {
			return st.BeginUsageDocumentAttempt(ctx, input)
		}, capture: captureUsage},
}

var (
	jsonFamily = documentFamily{failed: "office_json_capture_failed", cancelled: "office_json_cancelled",
		reasons: []string{"office_json_input_unqualified", "office_json_capture_failed",
			"office_json_cleanup_failed", "office_json_policy_restore_failed", "office_surface_unknown", "office_json_unreadable",
			"office_json_too_large", "office_json_unrecognized"}}
	registryFamily = documentFamily{failed: "office_registry_capture_failed", cancelled: "office_registry_cancelled",
		reasons: []string{"office_registry_input_unqualified", "office_registry_capture_failed", "office_registry_cleanup_failed",
			"office_registry_policy_restore_failed", "office_registry_source_busy", "office_registry_index_unreadable",
			"office_registry_index_too_large", "office_registry_index_unrecognized"}}
	usageFamily = documentFamily{failed: "onedrive_usage_capture_failed", cancelled: "onedrive_usage_cancelled",
		reasons: []string{"onedrive_usage_input_unqualified", "onedrive_usage_capture_failed", "onedrive_usage_cleanup_failed",
			"onedrive_usage_policy_restore_failed", "onedrive_usage_source_busy", "onedrive_usage_index_unreadable",
			"onedrive_usage_index_too_large", "onedrive_usage_index_unrecognized"}}
)

// discoveryOutcome turns a default family's discovery error into its fixed outcome. Cancellation
// and anything that is not a fixed capture refusal stay command errors.
func discoveryOutcome(ctx context.Context, kind string, err error) (*DocumentDiscovery, error) {
	if err == nil {
		return nil, nil
	}
	var typed *officecapture.ReadError
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &typed) {
		return nil, err
	}
	return &DocumentDiscovery{Kind: kind, Code: typed.Code}, nil
}

// captureRegistry stages the decoded registry result inside the capture wrapper and commits it
// only after the wrapper returned success: its late validation, private-copy removal, unlock,
// source close and policy restoration have all succeeded by then.
func captureRegistry(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, previous store.KnowledgeSource, signature string) (string, error) {
	var result officeregistry.Result
	var metadata store.RegistryCaptureMetadata
	var digest, observedSignature string
	err := safeRegistryCapture(ctx, officecapture.RegistryInput{Path: previous.Locator}, func(c *officecapture.RegistryCapture) error {
		var err error
		result, err = c.ReadRegistry(ctx)
		if err != nil {
			return err
		}
		digest, observedSignature = c.ContentDigest, c.AcquisitionSignature
		acquired, modified := c.AcquiredAt, c.SourceModified
		metadata = store.RegistryCaptureMetadata{AcquiredAt: &acquired, SourceObservedAt: &modified}
		return nil
	})
	if err != nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, err, registryFamily)
	}
	if digest == "" || metadata.AcquiredAt == nil || metadata.SourceObservedAt == nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, errors.New("documents: capture result unavailable"), registryFamily)
	}
	status, reason := "complete", ""
	if len(result.Losses) > 0 {
		status = "partial"
	} else if len(result.Documents) == 0 {
		reason = RegistryListedNoDocuments
	}
	if previous.AdmittedID > 0 && previous.ContentDigest == digest && previous.Qualification == store.RegistryDocumentQualification {
		status = "unchanged"
		result = officeregistry.Result{}
	}
	outcome := store.KnowledgeOutcome{Status: status, Reason: reason, ContentDigest: digest, AcquisitionSignature: observedSignature,
		Qualification: store.RegistryDocumentQualification, SourceObservedAt: *metadata.SourceObservedAt, At: documentsNow().UTC()}
	if err := st.CommitRegistryDocumentObservations(ctx, attempt, outcome, result, metadata); err != nil {
		if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			return "", err
		}
		return "", errors.Join(err, recordDocumentFailure(ctx, st, attempt, observedSignature, err, registryFamily))
	}
	return status, nil
}

// RegistryListedNoDocuments is the reason of a complete registry capture that listed no documents.
const RegistryListedNoDocuments = "office_registry_listed_no_documents"

func safeRegistryCapture(ctx context.Context, input officecapture.RegistryInput, consume func(*officecapture.RegistryCapture) error) (result error) {
	defer func() {
		if recover() != nil {
			result = &officecapture.ReadError{Code: "office_registry_capture_failed"}
		}
	}()
	return documentsRegistryCapture(ctx, input, consume)
}

// UsageListedNothing is the reason of a complete usage capture that listed no recent row, no
// formatted document and no collaborator.
const UsageListedNothing = "onedrive_usage_listed_nothing"

// captureUsage stages the decoded usage result inside the capture wrapper and commits it only
// after the wrapper returned success: its late validation, private-copy removal, unlock, source
// close and policy restoration have all succeeded by then.
func captureUsage(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, previous store.KnowledgeSource, signature string) (string, error) {
	var result onedriveusage.Result
	var metadata store.UsageCaptureMetadata
	var digest, observedSignature string
	err := safeUsageCapture(ctx, officecapture.UsageInput{Path: previous.Locator}, func(c *officecapture.UsageCapture) error {
		var err error
		result, err = c.ReadUsage(ctx)
		if err != nil {
			return err
		}
		digest, observedSignature = c.ContentDigest, c.AcquisitionSignature
		acquired, modified := c.AcquiredAt, c.SourceModified
		metadata = store.UsageCaptureMetadata{AcquiredAt: &acquired, SourceObservedAt: &modified}
		return nil
	})
	if err != nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, err, usageFamily)
	}
	if digest == "" || metadata.AcquiredAt == nil || metadata.SourceObservedAt == nil {
		return "failed", recordDocumentFailure(ctx, st, attempt, signature, errors.New("documents: capture result unavailable"), usageFamily)
	}
	status, reason := "complete", ""
	if len(result.Losses) > 0 {
		status = "partial"
	} else if len(result.Recent)+len(result.Documents)+len(result.Collaborators) == 0 {
		reason = UsageListedNothing
	}
	if previous.AdmittedID > 0 && previous.ContentDigest == digest && previous.Qualification == store.UsageDocumentQualification {
		status = "unchanged"
		result = onedriveusage.Result{}
	}
	outcome := store.KnowledgeOutcome{Status: status, Reason: reason, ContentDigest: digest, AcquisitionSignature: observedSignature,
		Qualification: store.UsageDocumentQualification, SourceObservedAt: *metadata.SourceObservedAt, At: documentsNow().UTC()}
	if err := st.CommitUsageDocumentObservations(ctx, attempt, outcome, result, metadata); err != nil {
		if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			return "", err
		}
		return "", errors.Join(err, recordDocumentFailure(ctx, st, attempt, observedSignature, err, usageFamily))
	}
	return status, nil
}

func safeUsageCapture(ctx context.Context, input officecapture.UsageInput, consume func(*officecapture.UsageCapture) error) (result error) {
	defer func() {
		if recover() != nil {
			result = &officecapture.ReadError{Code: "onedrive_usage_capture_failed"}
		}
	}()
	return documentsUsageCapture(ctx, input, consume)
}

func recordDocumentFailure(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, signature string, cause error, family documentFamily) error {
	status, reason := "failed", family.failed
	var typed *officecapture.ReadError
	if errors.As(cause, &typed) && slices.Contains(family.reasons, typed.Code) {
		reason = typed.Code
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status, reason = "cancelled", family.cancelled
	}
	diagnosticCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return st.RecordKnowledgeFailure(diagnosticCtx, attempt, store.KnowledgeOutcome{
		Status: status, Reason: reason, AcquisitionSignature: signature, At: documentsNow().UTC()})
}

// ErrRegistryFileUnusable is a supplied registry file that is missing, not a regular file, or one
// of the archive's own files. Nothing is registered for it.
var ErrRegistryFileUnusable = errors.New("documents: registry file is missing, not a regular file, or the archive itself")

// ErrUsageFileUnusable is the same refusal of a supplied OneDrive usage file.
var ErrUsageFileUnusable = errors.New("documents: OneDrive usage file is missing, not a regular file, or the archive itself")

var documentsLstat = os.Lstat

// CheckRegistryFile refuses a supplied registry file before anything is registered or opened. It
// only reads directory metadata: opening and closing a descriptor of the archive database would
// release this process's own SQLite locks on it.
func CheckRegistryFile(dbPath, file string) error {
	if !suppliedFileUsable(dbPath, file) {
		return ErrRegistryFileUnusable
	}
	return nil
}

// CheckUsageFile refuses a supplied OneDrive usage file in the same way, before anything is
// registered or opened.
func CheckUsageFile(dbPath, file string) error {
	if !suppliedFileUsable(dbPath, file) {
		return ErrUsageFileUnusable
	}
	return nil
}

// suppliedFileUsable reports whether file is an existing regular file other than the archive
// database and its own companion files.
func suppliedFileUsable(dbPath, file string) bool {
	info, err := documentsLstat(file)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	archive := filepath.Clean(dbPath)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal", ".lock"} {
		own, err := documentsLstat(archive + suffix)
		if filepath.Clean(file) == archive+suffix || (err == nil && os.SameFile(info, own)) {
			return false
		}
	}
	return true
}
