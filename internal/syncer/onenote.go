package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
)

type NotesOptions struct {
	DBPath, Root       string
	SourceID           int64
	MaxAge             time.Duration
	Explicit, Discover bool
}

type NotesReport struct {
	Sources                              []store.KnowledgeSource
	Captured, Unchanged, Skipped, Failed int
}

var (
	notesNow      = time.Now
	notesPlatform = runtime.GOOS
	notesHome     = os.UserHomeDir
	notesCapture  = onenote.CaptureIndex
	notesObserve  = onenote.ObserveIndex
	notesRead     = onenote.ReadIndex
	notesOpen     = store.Open
	notesOpenRead = store.OpenReadOnly
	notesAcquire  = store.AcquireLock
)

func readNotesSources(ctx context.Context, options NotesOptions) (NotesReport, error) {
	st, err := notesOpenRead(ctx, options.DBPath)
	if errors.Is(err, store.ErrNoArchive) {
		return NotesReport{Sources: []store.KnowledgeSource{}}, nil
	}
	if err != nil {
		return NotesReport{}, err
	}
	report := NotesReport{}
	report.Sources, err = participatingNotesSources(ctx, st, options.SourceID, nil)
	return report, errors.Join(err, st.Close())
}

func participatingNotesSources(ctx context.Context, st *store.Store, sourceID int64, locators map[string]bool) ([]store.KnowledgeSource, error) {
	sources, err := st.KnowledgeSources(ctx, "notes")
	if err != nil {
		return nil, err
	}
	out := []store.KnowledgeSource{}
	for _, source := range sources {
		if sourceID > 0 && source.ID != sourceID {
			continue
		}
		if locators != nil && !locators[source.Locator] {
			continue
		}
		out = append(out, source)
	}
	if sourceID > 0 && len(out) == 0 {
		return nil, store.ErrKnowledgeSourceUnknown
	}
	return out, nil
}

func SyncNotes(ctx context.Context, options NotesOptions) (NotesReport, error) {
	if !options.Explicit && options.MaxAge == 0 {
		return readNotesSources(ctx, options)
	}
	if !options.Explicit && !options.Discover {
		report, err := readNotesSources(ctx, options)
		if err != nil || len(report.Sources) == 0 {
			return report, err
		}
	}
	release, err := notesAcquire(options.DBPath)
	if err != nil {
		return NotesReport{}, err
	}
	defer release()
	st, err := notesOpen(ctx, options.DBPath)
	if err != nil {
		return NotesReport{}, err
	}
	report, runErr := syncNotes(ctx, st, options)
	return report, errors.Join(runErr, st.Close())
}

func syncNotes(ctx context.Context, st *store.Store, options NotesOptions) (NotesReport, error) {
	report := NotesReport{Sources: []store.KnowledgeSource{}}
	locators, err := st.KnowledgeLocators(ctx, options.SourceID)
	if err != nil {
		return report, err
	}
	if options.Discover {
		if !options.Explicit {
			return report, errors.New("notes: discovery requires explicit sync")
		}
		root, rootErr := notesRoot(options.Root)
		if rootErr != nil {
			return report, rootErr
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return report, errors.New("notes: index directory unavailable")
		}
		discovered, discoverErr := discoverNotes(root)
		if discoverErr != nil {
			return report, discoverErr
		}
		if options.SourceID == 0 {
			selected := []store.KnowledgeSource{}
			for _, locator := range locators {
				if filepath.Dir(locator.Locator) == root {
					selected = append(selected, locator)
				}
			}
			locators = selected
		}
		for _, path := range discovered {
			found := false
			for _, locator := range locators {
				found = found || locator.Locator == path
			}
			if !found {
				locators = append(locators, store.KnowledgeSource{Locator: path, Platform: notesPlatform, Kind: "notes"})
			}
		}
	}
	participating := map[string]bool{}
	sort.Slice(locators, func(i, j int) bool { return locators[i].Locator < locators[j].Locator })
	for _, source := range locators {
		participating[source.Locator] = true
		if err := ctx.Err(); err != nil {
			return report, err
		}
		signature, observeErr := notesObserve(source.Locator)
		now := notesNow().UTC()
		recent := source.AttemptedAt != nil && now.Sub(*source.AttemptedAt) < options.MaxAge
		unchangedObservation := signature != "" && signature == source.AcquisitionSignature
		if !options.Explicit && recent && (unchangedObservation || (signature == "" && source.AcquisitionSignature == "")) {
			report.Skipped++
			continue
		}
		attempt, err := st.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{
			Kind: "notes", Platform: source.Platform, Locator: source.Locator, AcquisitionSignature: signature, At: now,
		})
		if err != nil {
			return report, err
		}
		if observeErr != nil {
			if err := recordNotesFailure(ctx, st, attempt, signature, observeErr, source.Locator); err != nil {
				return report, err
			}
			report.Failed++
			continue
		}
		status, err := captureNotes(ctx, st, attempt, source, signature)
		if err != nil {
			return report, err
		}
		switch status {
		case "unchanged":
			report.Unchanged++
		case "complete", "partial":
			report.Captured++
		default:
			report.Failed++
		}
	}
	report.Sources, err = participatingNotesSources(ctx, st, options.SourceID, participating)
	return report, err
}

func captureNotes(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, previous store.KnowledgeSource, signature string) (string, error) {
	captureCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	capture, err := notesCapture(captureCtx, previous.Locator)
	if err != nil {
		return "failed", recordNotesFailure(ctx, st, attempt, signature, err, previous.Locator)
	}
	result, readErr := notesRead(captureCtx, capture.Path)
	closeErr := capture.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return "failed", recordNotesFailure(ctx, st, attempt, capture.AcquisitionSignature, err, previous.Locator)
	}
	status := "complete"
	if len(result.Losses) > 0 {
		status = "partial"
	}
	if previous.AdmittedID > 0 && previous.ContentDigest == capture.ContentDigest && previous.Qualification == store.NotesQualification {
		status = "unchanged"
	}
	outcome := store.KnowledgeOutcome{
		Status: status, AcquisitionSignature: capture.AcquisitionSignature, ContentDigest: capture.ContentDigest,
		Qualification: store.NotesQualification, SourceObservedAt: capture.SourceModified, At: notesNow().UTC(),
	}
	if err := st.CommitNotes(ctx, attempt, outcome, result); err != nil {
		if errors.Is(err, store.ErrStaleKnowledgeAttempt) {
			return "", err
		}
		diagnosticErr := recordNotesFailure(ctx, st, attempt, capture.AcquisitionSignature, err, previous.Locator)
		return "", errors.Join(err, diagnosticErr)
	}
	return status, nil
}

func recordNotesFailure(ctx context.Context, st *store.Store, attempt store.KnowledgeAttempt, signature string, cause error, path string) error {
	status := "failed"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		status = "cancelled"
	} else if _, err := os.Lstat(path); os.IsNotExist(err) {
		status = "unavailable"
	}
	diagnostic, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := st.RecordKnowledgeFailure(diagnostic, attempt, store.KnowledgeOutcome{
		Status: status, Reason: "onenote_snapshot_unavailable", AcquisitionSignature: signature, At: notesNow().UTC(),
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

func notesRoot(explicit string) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if notesPlatform != "darwin" {
		return "", errors.New("notes: default index discovery unavailable on this platform; use --root")
	}
	home, err := notesHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Containers", "com.microsoft.onenote.mac", "Data", "Library",
		"Application Support", "Microsoft User Data", "OneNote", "15.0", "FullTextSearchIndex"), nil
}

func discoverNotes(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, errors.New("notes: index directory unavailable")
	}
	paths := []string{}
	for _, entry := range entries {
		if !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			continue
		}
		if len(paths) >= 64 {
			return nil, errors.New("notes: index directory exceeds 64 inputs")
		}
		// Preserve unsafe candidate leaves as independent failures rather than
		// silently excluding them and claiming the examined directory is empty.
		paths = append(paths, filepath.Join(root, entry.Name()))
	}
	return paths, nil
}
