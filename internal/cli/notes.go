package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type notesGroup struct {
	Sync    notesSyncCmd    `cmd:"" help:"Capture OneNote indexed notes locally; register default Mac indexes or an explicit directory."`
	List    notesListCmd    `cmd:"" help:"List indexed notes with stable note IDs and source coverage."`
	Search  notesSearchCmd  `cmd:"" help:"Search archived note titles, text and OCR."`
	Show    notesShowCmd    `cmd:"" help:"Show a note's text, native elements, flags and indexed hierarchy."`
	Sources notesSourcesCmd `cmd:"" help:"Show registered note source health, capture history and indexed coverage."`
}

func (notesGroup) Help() string {
	return "Notes are local OneNote indexed-cache observations, not full document layout or service completeness. Native notebook IDs qualify a cache boundary, not account ownership. Partial captures retain prior page text, OCR and flags. Missing pages are retained, not treated as deletions.\n" +
		"Run `notes sync` to register available Mac indexes or `notes sync --root DIR` for explicit rollback-mode SQLite index copies on Mac, Windows or Linux. Sources are never modified; WAL, active journals and unsafe files are refused. Ordinary reads refresh only registered participating locators; --no-sync disables capture. No browser, network, hydration or app launch.\n" +
		"Use `notes sources` for coverage and `notes show note:ID` to read a search hit. Dates are native modified times; unknown dates do not match bounded windows. Source IDs are archive-local, and --account is not supported."
}

type noteItem struct {
	ID               string     `json:"id"`
	SourceID         int64      `json:"source_id"`
	CaptureID        int64      `json:"capture_id"`
	GOID             string     `json:"goid"`
	GUID             string     `json:"guid"`
	GOSID            string     `json:"gosid"`
	ParentGOID       string     `json:"parent_goid"`
	GrandparentGOIDs string     `json:"grandparent_goids"`
	Title            string     `json:"title"`
	Text             string     `json:"text"`
	ModifiedAt       *time.Time `json:"modified_at"`
	CapturedAt       *time.Time `json:"captured_at"`
	SourceObservedAt *time.Time `json:"source_observed_at"`
	TextTruncated    bool       `json:"text_truncated,omitempty"`
}

func noteItemOf(row store.NoteRow, maxText int) noteItem {
	text, truncated := truncateRunes(row.Text, maxText)
	return noteItem{ID: fmt.Sprintf("note:%d", row.ID), SourceID: row.SourceID, CaptureID: row.CaptureID,
		GOID: row.GOID, GUID: row.GUID, GOSID: row.GOSID, ParentGOID: row.ParentGOID, GrandparentGOIDs: row.GrandparentGOIDs,
		Title: row.Title, Text: text, ModifiedAt: row.ModifiedAt, CapturedAt: row.CapturedAt,
		SourceObservedAt: row.SourceObservedAt, TextTruncated: truncated}
}

func noteDetailKeys() []string {
	return append(typeKeys[noteItem](), typeKeys[noteDetailItem]()...)
}

func noteStamp(at *time.Time) string {
	if at == nil {
		return "-"
	}
	return stamp(*at)
}

type noteElementItem struct {
	RowID          int64  `json:"native_rowid"`
	EntityRowID    int64  `json:"native_entity_rowid"`
	GOID           string `json:"goid"`
	Text           string `json:"text"`
	Kind           string `json:"kind"`
	Jcid           int64  `json:"jcid"`
	HashtagMarkers int    `json:"hashtag_markers"`
	TextTruncated  bool   `json:"text_truncated,omitempty"`
}

type noteFlagItem struct {
	RowID        int64  `json:"native_rowid"`
	ElementRowID int64  `json:"element_native_rowid"`
	Type         int64  `json:"type"`
	Shape        int64  `json:"shape"`
	Status       int64  `json:"status"`
	Label        string `json:"label"`
}

type noteAncestorItem struct {
	GOID  string `json:"goid"`
	Title string `json:"title"`
	Type  int64  `json:"type"`
}

type noteDetailItem struct {
	noteItem
	Ordering  string             `json:"ordering"`
	Elements  []noteElementItem  `json:"elements"`
	Flags     []noteFlagItem     `json:"flags"`
	Ancestors []noteAncestorItem `json:"ancestors"`
}

type noteSourceItem struct {
	Kind                 string         `json:"kind,omitempty"`
	ID                   *int64         `json:"source_id"` // null until a capture of the locator is admitted
	LocatorID            int64          `json:"locator_id"`
	AttemptID            int64          `json:"attempt_id"`
	AdmittedID           int64          `json:"admitted_id"`
	Platform             string         `json:"platform"`
	Locator              string         `json:"locator"`
	Boundary             string         `json:"boundary"`
	Status               string         `json:"status"`
	Coverage             string         `json:"coverage"`
	Qualification        string         `json:"qualification"`
	AcquisitionSignature string         `json:"acquisition_signature"`
	ContentDigest        string         `json:"content_digest"`
	Reason               string         `json:"reason"`
	AttemptedAt          *time.Time     `json:"attempted_at"`
	AdmittedAt           *time.Time     `json:"admitted_at"`
	CompleteAt           *time.Time     `json:"complete_at"`
	SourceObservedAt     *time.Time     `json:"source_observed_at"`
	Losses               map[string]int `json:"losses"`
	// LocatorTail is the locator's last two path elements, enough to tell sources apart in a table.
	LocatorTail string `json:"locator_tail"`
	// Fix and Effect are a failed document source's remedy and what the failure does to listed
	// documents; omitted for healthy sources.
	Fix    string `json:"fix,omitempty"`
	Effect string `json:"effect,omitempty"`
}

// sourceID is a source ID as JSON: null for a locator that has no admitted source yet.
func sourceID(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// locatorTail is the last two elements of a source path, "account/x-rec-en-US-sr.json".
func locatorTail(locator string) string {
	parts := strings.FieldsFunc(locator, func(r rune) bool { return r == '/' || r == '\\' })
	return strings.Join(parts[max(len(parts)-2, 0):], "/")
}

func sourceLabel(id *int64) string {
	if id == nil {
		return "-"
	}
	return strconv.FormatInt(*id, 10)
}

type noteCoverage struct {
	Scope         string               `json:"scope"`
	State         string               `json:"state"`
	Sources       []noteSourceItem     `json:"sources"`
	RefreshError  *syncError           `json:"refresh_error,omitempty"`
	RefreshErrors map[string]syncError `json:"refresh_errors,omitempty"`
	Notices       []string             `json:"notices,omitempty"`
}

func notesCoverage(sources []store.KnowledgeSource) *noteCoverage {
	out := &noteCoverage{Scope: "onenote_indexed_cache", State: "unknown", Sources: []noteSourceItem{}}
	for _, source := range sources {
		out.State = "registered"
		out.Sources = append(out.Sources, noteSourceItem{Kind: source.Kind, ID: sourceID(source.ID), LocatorID: source.LocatorID, LocatorTail: locatorTail(source.Locator),
			AttemptID: source.AttemptID, AdmittedID: source.AdmittedID, Platform: source.Platform, Locator: source.Locator,
			Boundary: source.Boundary, Status: source.Status, Coverage: source.Coverage, Qualification: source.Qualification,
			AcquisitionSignature: source.AcquisitionSignature, ContentDigest: source.ContentDigest, Reason: source.Reason,
			AttemptedAt: source.AttemptedAt, AdmittedAt: source.AdmittedAt, CompleteAt: source.CompleteAt,
			SourceObservedAt: source.SourceObservedAt, Losses: source.Losses})
	}
	return out
}

var runNotesSync = syncer.SyncNotes

func (rt *runtime) refreshNotes(sourceID int64) (*noteCoverage, error) {
	var report syncer.NotesReport
	refreshErr := rt.besideOtherRun(func() (err error) {
		report, err = runNotesSync(rt.ctx, syncer.NotesOptions{DBPath: rt.dbPath, SourceID: sourceID, MaxAge: rt.maxAge})
		return err
	})
	if refreshErr == nil {
		return notesCoverage(report.Sources), nil
	}
	if rt.ctx.Err() != nil || errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return nil, refreshErr
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	sources, readErr := st.KnowledgeSources(rt.ctx, "notes")
	closeErr := st.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	selected := []store.KnowledgeSource{}
	for _, source := range sources {
		if sourceID == 0 || source.ID == sourceID {
			selected = append(selected, source)
		}
	}
	if sourceID > 0 && len(selected) == 0 {
		return nil, refreshErr
	}
	coverage := notesCoverage(selected)
	var codedErr *errs.Coded
	if !errors.As(refreshErr, &codedErr) {
		codedErr = errs.DBError(refreshErr)
	}
	coded := bodyOf(codedErr)
	coverage.RefreshError = &syncError{Code: coded.Code, Message: coded.Message}
	return coverage, nil
}

func (rt *runtime) notesRead(label string, sourceID int64, fn func(*store.Store) (*listResult, error)) error {
	if rt.g.Account != "" {
		return errs.Usage("notes have unresolved account ownership; --account is not supported")
	}
	if sourceID < 0 {
		return errs.Usage("--source must be a positive archive source ID")
	}
	coverage, err := rt.refreshNotes(sourceID)
	if err != nil {
		return asCoded(err)
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	switch {
	case errors.Is(err, store.ErrNoArchive):
		st = nil
	case err != nil:
		return asCoded(err)
	default:
		defer func() { _ = st.Close() }()
	}
	result, err := fn(st)
	if err != nil {
		return asCoded(err)
	}
	result.SourceCoverage = coverage
	result.sourceOnly = true
	if result.Count == 0 {
		if len(coverage.Sources) == 0 {
			result.Note = "no notes captured yet; run m365crawl notes sync"
		} else {
			result.Note = "no indexed note matched; source_coverage describes retained or unavailable input"
		}
	}
	rt.noteRegisteredSyncOff(result, "notes", sourceAttempts(coverage))
	return rt.write(label, result)
}

type notesSyncCmd struct {
	Root string `help:"Directory of supported OneNote rollback SQLite .db indexes; default: Mac OneNote FullTextSearchIndex." placeholder:"DIR"`
}

type notesSyncResult struct {
	Captured       int           `json:"captured"`
	Unchanged      int           `json:"unchanged"`
	Skipped        int           `json:"skipped"`
	Failed         int           `json:"failed"`
	SourceCoverage *noteCoverage `json:"source_coverage"`
	// Discovery is docs sync's default-family outcomes; other sync commands have none.
	Discovery []documentDiscoveryItem `json:"discovery,omitempty"`
}

func (c *notesSyncCmd) Run(rt *runtime) error {
	if rt.g.Account != "" {
		return errs.Usage("notes sync does not infer account ownership; drop --account")
	}
	report, err := runNotesSync(rt.ctx, syncer.NotesOptions{DBPath: rt.dbPath, Root: c.Root, Explicit: true, Discover: true})
	if err != nil {
		return asCoded(err)
	}
	coverage := notesCoverage(report.Sources)
	if len(report.Sources) == 0 {
		coverage.State = "examined_empty_directory"
	}
	return rt.write("notes sync", notesSyncResult{Captured: report.Captured, Unchanged: report.Unchanged,
		Skipped: report.Skipped, Failed: report.Failed, SourceCoverage: coverage})
}

type notesListCmd struct {
	Source int64  `help:"Only this archive-local source ID; notes sources lists available IDs."`
	Query  string `help:"Words to find in title, indexed text or OCR."`
	Since  string `help:"Native modified time at or after this date/time/relative age."`
	Until  string `help:"Native modified time before this time; a date alone includes that whole local day, a time is exclusive; unknown timestamps are excluded."`
	Limit  int    `default:"50" help:"Maximum notes to return; check truncated."`
	label  string // the result label: notes search runs this command under its own name
}

func (c *notesListCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "notes list", ""); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	since, until, err := rt.window("--since", c.Since, "--until", c.Until)
	if err != nil {
		return err
	}
	return rt.notesRead(firstOf(c.label, "notes list"), c.Source, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		rows, truncated, err := st.Notes(rt.ctx, store.NoteFilter{SourceID: c.Source, Query: c.Query, Since: since, Until: until, Limit: c.Limit})
		if err != nil {
			return nil, err
		}
		items := make([]noteItem, len(rows))
		for i, row := range rows {
			items[i] = noteItemOf(row, rt.g.MaxText)
		}
		return newList(shape(rt, items), truncated), nil
	})
}

type notesSearchCmd struct {
	Query  string `arg:"" help:"Words to find in indexed note titles, text or OCR."`
	Source int64  `help:"Only this archive source ID."`
	Since  string `help:"Only notes with native modified times at or after this time."`
	Until  string `help:"Only notes with native modified times before this time; a date alone includes that whole local day, a time is exclusive."`
	Limit  int    `default:"50" help:"Maximum hits; check truncated."`
}

func (c *notesSearchCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "notes search", ""); err != nil {
		return err
	}
	return (&notesListCmd{Source: c.Source, Query: c.Query, Since: c.Since, Until: c.Until, Limit: c.Limit, label: "notes search"}).Run(rt)
}

type notesShowCmd struct {
	ID string `arg:"" help:"Stable note:ID printed by notes list or search."`
}

func (c *notesShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "notes show", ""); err != nil {
		return err
	}
	raw, prefixed := strings.CutPrefix(c.ID, "note:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !prefixed || err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
		return errs.Usage("note ID must be note: followed by a positive decimal ID")
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return asCoded(err)
	}
	note, readErr := st.Note(rt.ctx, id)
	closeErr := st.Close()
	if errors.Is(readErr, store.ErrNoteNotFound) {
		return errs.Usage("note not found; notes list shows retained note IDs")
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return asCoded(err)
	}
	return rt.notesRead("notes show", note.SourceID, func(st *store.Store) (*listResult, error) {
		detail, err := st.Note(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		item := noteDetailItem{noteItem: noteItemOf(detail.NoteRow, rt.g.MaxText),
			Ordering: "index_rowid_not_document_layout", Elements: []noteElementItem{}, Flags: []noteFlagItem{}, Ancestors: []noteAncestorItem{}}
		for _, element := range detail.Elements {
			text, truncated := truncateRunes(element.Text, rt.g.MaxText)
			item.Elements = append(item.Elements, noteElementItem{RowID: element.RowID, EntityRowID: element.EntityRowID,
				GOID: element.GOID, Text: text, Kind: element.Kind, Jcid: element.Jcid,
				HashtagMarkers: element.HashtagMarkers, TextTruncated: truncated})
		}
		for _, flag := range detail.Flags {
			item.Flags = append(item.Flags, noteFlagItem{RowID: flag.RowID, ElementRowID: flag.ElementRowID,
				Type: flag.Type, Shape: flag.Shape, Status: flag.Status, Label: flag.Label})
		}
		for _, ancestor := range detail.Ancestors {
			item.Ancestors = append(item.Ancestors, noteAncestorItem{GOID: ancestor.GOID, Title: ancestor.Title, Type: ancestor.Type})
		}
		return newList(shape(rt, []noteDetailItem{item}), false), nil
	})
}

type notesSourcesCmd struct{}

func (*notesSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "notes sources", ""); err != nil {
		return err
	}
	return rt.notesRead("notes sources", 0, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		sources, err := st.KnowledgeSources(rt.ctx, "notes")
		if err != nil {
			return nil, err
		}
		return newList(shape(rt, notesCoverage(sources).Sources), false), nil
	})
}

func (rt *runtime) renderNoteCoverage(coverage *noteCoverage) {
	if coverage == nil {
		return
	}
	name := "notes"
	switch coverage.Scope {
	case "outlook_native_people_observations":
		name = "people"
	case "office_document_metadata":
		name = "documents"
	case "registered_optional_sources":
		name = "notes and documents"
	}
	_, _ = fmt.Fprintf(rt.stdout, "%s coverage: %s (%s)\n", name, coverage.State, coverage.Scope)
	rows := [][]string{}
	for _, source := range coverage.Sources {
		rows = append(rows, []string{sourceLabel(source.ID), source.Kind, source.Status, source.Coverage, source.LocatorTail, source.Reason})
	}
	render.Table(rt.stdout, []string{"source", "kind", "status", "coverage", "locator", "reason"}, rows, rt.color)
	for _, source := range coverage.Sources {
		if source.Fix != "" {
			_, _ = fmt.Fprintf(rt.stdout, "%s %s: %s %s. %s.\n  fix: %s\n", source.Kind, source.LocatorTail, source.Status, source.Reason, source.Effect, source.Fix)
		}
	}
	for _, notice := range coverage.Notices {
		_, _ = fmt.Fprintf(rt.stdout, "notice: %s\n", notice)
	}
	if coverage.RefreshError != nil {
		_, _ = fmt.Fprintf(rt.stdout, "%s refresh error: %s: %s (serving retained archive)\n", name, coverage.RefreshError.Code, coverage.RefreshError.Message)
	}
}

func (rt *runtime) renderNoteDetails(result *listResult) {
	for _, item := range result.Items {
		detail, ok := item.(noteDetailItem)
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(rt.stdout, "\n%s\n%s\n", detail.Title, detail.Text)
		for _, ancestor := range detail.Ancestors {
			_, _ = fmt.Fprintf(rt.stdout, "ancestor: %s (%s, type%d)\n", ancestor.Title, ancestor.GOID, ancestor.Type)
		}
		for _, element := range detail.Elements {
			_, _ = fmt.Fprintf(rt.stdout, "element %d [%s; %s]: %s\n", element.RowID, element.Kind, element.GOID, element.Text)
		}
		for _, flag := range detail.Flags {
			_, _ = fmt.Fprintf(rt.stdout, "flag %d on element%d: %s (type%d shape%d status%d)\n",
				flag.RowID, flag.ElementRowID, flag.Label, flag.Type, flag.Shape, flag.Status)
		}
	}
}
