package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type documentsGroup struct {
	Sync    documentsSyncCmd    `cmd:"" help:"Register and capture ordinary local Office JSON metadata; never opens a referenced document."`
	List    documentsListCmd    `cmd:"" help:"List document observations with stable IDs and source coverage."`
	Search  documentsSearchCmd  `cmd:"" help:"Search retained document metadata, native URLs and sharing/person claims."`
	Show    documentsShowCmd    `cmd:"" help:"Inspect one document observation, raw native claims and unresolved URL references."`
	Sources documentsSourcesCmd `cmd:"" help:"Inspect registered document source health without capture."`
}

func (documentsGroup) Help() string {
	return "Documents are retained Office recent/shared/recommended/dialog metadata observations, not original document text, resolved identities or a complete library export.\n" +
		"Register once with `docs sync` for named Mac Office caches, or `docs sync --root DIR` for ordinary local recent.json/shared.json/recommended.json/dialog.json copies. Windows accepts qualified explicit ordinary NTFS inputs, not default Office discovery. Unknown/provider/reparse/dataless inputs are refused; no document URL or friendly path is opened, no hydration or network fallback.\n" +
		"Then use `docs list --query WORDS --limit 10`, `docs search WORDS`, or `search WORDS --source documents`. Follow a returned document-observation:ID with `docs show document-observation:ID`; `docs sources` lists source IDs and coverage. Ordinary reads refresh only registered sources; --max-age 0 is archive-only, and docs sources never captures. Partial/failed inputs keep last-good observations. A successful Office response that lists no documents is complete with reason office_response_listed_no_documents (earlier rows become retained); it does not mean the account has none.\n" +
		"--since/--until use native document modification time, not opened/shared/file/capture time; unknown dates do not match bounded windows. Native sharing/activity are source claims, not counted new events. Account/author/chat/mail filters cannot qualify these unresolved documents. Text state is not_captured; there is no docs read/text command."
}

type documentItem struct {
	ID               string                  `json:"id"`
	SourceID         int64                   `json:"source_id"`
	CaptureID        int64                   `json:"capture_id"`
	Application      string                  `json:"application"`
	Surface          officedocuments.Surface `json:"surface"`
	Title            string                  `json:"title"`
	URL              string                  `json:"url"`
	WebURL           string                  `json:"web_url"`
	Native           documentNative          `json:"native"`
	ModifiedAt       *time.Time              `json:"modified_at"`
	CapturedAt       *time.Time              `json:"captured_at"`
	SourceObservedAt *time.Time              `json:"source_observed_at"`
	Retained         bool                    `json:"retained"`
	AssociationState string                  `json:"association_state"`
	TextState        string                  `json:"text_state"`
	TextTruncated    bool                    `json:"text_truncated,omitempty"`
}

func documentItemOf(row store.DocumentObservationRow, maxText int) documentItem {
	native := row.Document
	title, truncated := truncateRunes(native.Title, maxText)
	native.Title = title
	var cut bool
	for _, field := range []*string{&native.FriendlyPath, &native.SiteTitle, &native.TeamsChannelTitle} {
		*field, cut = truncateRunes(*field, maxText)
		truncated = truncated || cut
	}
	for _, person := range []*officedocuments.Person{&native.Creator, &native.Modifier} {
		person.DisplayName, cut = truncateRunes(person.DisplayName, maxText)
		truncated = truncated || cut
	}
	if native.Sharing != nil {
		sharing := *native.Sharing
		sharing.DisplayName, cut = truncateRunes(sharing.DisplayName, maxText)
		truncated = truncated || cut
		native.Sharing = &sharing
	}
	if native.Activity != nil {
		activity := *native.Activity
		activity.MessageFormat, cut = truncateRunes(activity.MessageFormat, maxText)
		truncated = truncated || cut
		activity.Users = append([]officedocuments.Person{}, activity.Users...)
		for i := range activity.Users {
			activity.Users[i].DisplayName, cut = truncateRunes(activity.Users[i].DisplayName, maxText)
			truncated = truncated || cut
		}
		native.Activity = &activity
	}
	return documentItem{ID: fmt.Sprintf("document-observation:%d", row.ID), SourceID: row.SourceID,
		CaptureID: row.CaptureID, Application: row.Application, Surface: row.Document.Surface,
		Title: title, URL: row.Document.URL, WebURL: row.Document.WebURL, Native: documentNative(native),
		ModifiedAt: searchTime(row.Document.Modified.Value), CapturedAt: row.CapturedAt,
		SourceObservedAt: row.SourceObservedAt, Retained: row.Retained,
		AssociationState: "unresolved", TextState: "not_captured", TextTruncated: truncated}
}

type documentDetailItem struct {
	documentItem
	References          []store.DocumentReferenceObservation `json:"references"`
	ReferencesTotal     int                                  `json:"references_total"`
	ReferencesTruncated bool                                 `json:"references_truncated"`
}

func documentDetailKeys() []string {
	return append(typeKeys[documentItem](), typeKeys[documentDetailItem]()...)
}

var runDocumentsSync = syncer.SyncDocuments

func documentsCoverage(sources []store.KnowledgeSource) *noteCoverage {
	out := notesCoverage(sources)
	out.Scope = "office_json_metadata"
	listedNone := false
	for i := range sources {
		out.Sources[i].Kind = sources[i].Kind
		listedNone = listedNone || sources[i].Reason == officedocuments.NoDocumentsListed
	}
	if listedNone {
		out.Notices = append(out.Notices, documentsListedNoneNotice)
	}
	return out
}

const documentsListedNoneNotice = officedocuments.NoDocumentsListed + ": each source with this reason last saved a successful Office response that listed no documents, so its capture is complete with zero rows. " +
	"This describes the saved response, not whether the account has documents; earlier observations from the source stay in the archive with retained=true."

func documentError(err error) error {
	var source *officecapture.ReadError
	if errors.As(err, &source) {
		return &errs.Coded{Code: source.Code, Exit: errs.ExitEnvironment,
			Message: "Office document metadata input was refused; no document was opened or fetched",
			Fix:     "Use supported ordinary local Office JSON copies on a qualified local disk with `docs sync --root DIR`; provider/placeholder, linked and unrecognized inputs are not accepted."}
	}
	if errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		return &errs.Coded{Code: "document_source_unknown", Exit: errs.ExitUsage,
			Message: "document source not found", Fix: "Run `m365crawl docs sources` for registered source IDs."}
	}
	if errors.Is(err, store.ErrDocumentObservationNotFound) || errors.Is(err, store.ErrNoArchive) {
		return &errs.Coded{Code: "document_observation_unknown", Exit: errs.ExitUsage,
			Message: "document observation not found", Fix: "Run `m365crawl docs list` for retained document-observation:IDs."}
	}
	return asCoded(err)
}

func (rt *runtime) documentFlags() error {
	if rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("document account/subject ownership is unresolved; drop account/link/profile filters")
	}
	return nil
}

func (rt *runtime) refreshDocuments(sourceID int64) (*noteCoverage, error) {
	report, refreshErr := runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, SourceID: sourceID, MaxAge: rt.maxAge})
	if refreshErr == nil {
		return documentsCoverage(report.Sources), nil
	}
	if rt.ctx.Err() != nil || errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return nil, refreshErr
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	sources, readErr := st.DocumentSources(rt.ctx)
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
	coverage := documentsCoverage(selected)
	coded := errs.DBError(refreshErr)
	errors.As(documentError(refreshErr), &coded)
	coverage.RefreshError = &syncError{Code: coded.Code, Message: coded.Message}
	return coverage, nil
}

func (rt *runtime) documentsRead(label string, sourceID int64, fn func(*store.Store) (*listResult, error)) error {
	if err := rt.documentFlags(); err != nil {
		return err
	}
	if sourceID < 0 {
		return errs.Usage("--source-id must be a positive document source ID")
	}
	coverage, err := rt.refreshDocuments(sourceID)
	if err != nil {
		return documentError(err)
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		st = nil
	} else if err != nil {
		return documentError(err)
	}
	result, readErr := fn(st)
	var closeErr error
	if st != nil {
		closeErr = st.Close()
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return documentError(err)
	}
	if result.SourceCoverage == nil {
		result.SourceCoverage = documentsCoverage(nil)
	}
	result.SourceCoverage.RefreshError = coverage.RefreshError
	result.sourceOnly = true
	if result.Count == 0 {
		if len(result.SourceCoverage.Sources) == 0 {
			result.Note = "no document metadata captured yet; run m365crawl docs sync or docs sync --root DIR"
		} else {
			result.Note = "no document metadata matched; source_coverage describes retained or unavailable inputs"
		}
	}
	return rt.write(label, result)
}

type documentsSyncCmd struct {
	Root     string `help:"Ordinary local directory with recent.json, shared.json, recommended.json or dialog.json; never a provider/placeholder path." placeholder:"DIR"`
	SourceID int64  `name:"source-id" help:"Refresh only this positive registered document source ID; mutually exclusive with --root."`
}

func (c *documentsSyncCmd) Run(rt *runtime) error {
	if err := rt.documentFlags(); err != nil {
		return err
	}
	if c.SourceID < 0 || (c.SourceID != 0 && c.Root != "") {
		return errs.Usage("choose --root DIR or one positive --source-id, not both")
	}
	report, err := runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, Root: c.Root,
		SourceID: c.SourceID, Explicit: true, Discover: c.SourceID == 0})
	if err != nil {
		return documentError(err)
	}
	return rt.write("docs sync", notesSyncResult{Captured: report.Captured, Unchanged: report.Unchanged,
		Skipped: report.Skipped, Failed: report.Failed, SourceCoverage: documentsCoverage(report.Sources)})
}

type documentsListCmd struct {
	SourceID int64  `name:"source-id" help:"Only this registered document source ID; docs sources lists IDs."`
	Query    string `help:"Words in document title, native URLs/IDs or source sharing/person metadata."`
	Since    string `help:"Native modification time at or after this date/time; unknown dates excluded."`
	Until    string `help:"Native modification time before this time; a date includes its whole day."`
	Limit    int    `default:"50" help:"Maximum observations; 0 returns all. Check truncated."`
}

const documentEvidenceHelp = "Each row is an immutable native metadata representation, not a resolved file/owner or a newly occurring sharing event. Identical sharing claims and repeat capture do not create new events; missing native sharing/activity dates stay unknown.\n" +
	"capture_id and captured_at identify the representation's original private acquisition, not its most recent check. source_observed_at is the acquired input file's modification time, not native document/sharing time or admission time. Later checks do not overwrite these original clocks. retained means this representation was absent from the last admitted capture; absence does not mean deletion or revoked sharing.\n" +
	"Native timestamps keep exact source text in Raw; Value is parsed UTC or null when missing/unrecognized. They are never replaced with file/capture time. modified_at is the native document modification time or null; bounded date windows exclude unknown values. text_state=not_captured means original document contents were not acquired."

func (documentsListCmd) Help() string { return documentEvidenceHelp }

func (c *documentsListCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs list", ""); err != nil {
		return err
	}
	if c.Limit < 0 {
		return errs.Usage("--limit must be nonnegative; 0 returns all document observations")
	}
	since, err := rt.when("--since", c.Since)
	if err != nil {
		return err
	}
	until, err := rt.when("--until", c.Until)
	if err != nil {
		return err
	}
	until = documentUpperBound(until, c.Until)
	return rt.documentsRead("docs list", c.SourceID, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		snapshot, err := st.DocumentSnapshot(rt.ctx, store.DocumentFilter{Query: c.Query,
			SourceID: c.SourceID, ModifiedSince: since, ModifiedUntil: until, Limit: c.Limit})
		if err != nil {
			return nil, err
		}
		items := make([]documentItem, len(snapshot.Rows))
		for i, row := range snapshot.Rows {
			items[i] = documentItemOf(row, rt.g.MaxText)
		}
		result := newList(shape(rt, items), snapshot.Truncated)
		result.SourceCoverage = documentsCoverage(snapshot.Sources)
		return result, nil
	})
}

func documentUpperBound(until time.Time, raw string) time.Time {
	if dateOnly.MatchString(raw) {
		return until.In(time.Local).AddDate(0, 0, 1)
	}
	return until
}

type documentsSearchCmd struct {
	Query    string `arg:"" optional:"" help:"Words in archived document metadata; required unless a date filter is supplied."`
	SourceID int64  `name:"source-id" help:"Only this registered document source ID."`
	Since    string `help:"Native modification time at or after this date/time."`
	Until    string `help:"Native modification time before this time; unknown dates excluded."`
	Limit    int    `default:"50" help:"Maximum observations; 0 returns all. Check truncated."`
}

func (documentsSearchCmd) Help() string { return documentEvidenceHelp }

func (c *documentsSearchCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs search", ""); err != nil {
		return err
	}
	if strings.TrimSpace(c.Query) == "" && c.Since == "" && c.Until == "" {
		return errs.Usage("docs search needs words or a native date filter; use docs list for an unfiltered list")
	}
	return (&documentsListCmd{Query: c.Query, SourceID: c.SourceID, Since: c.Since, Until: c.Until, Limit: c.Limit}).Run(rt)
}

type documentsShowCmd struct {
	ID string `arg:"" help:"document-observation:ID returned by docs list/search or shared search; not a URL/native ID."`
}

func (documentsShowCmd) Help() string {
	return documentEvidenceHelp + "\nURL references are bounded exact occurrences, not resolved identity or proof of local availability. source_coverage describes this observation's selected source; use docs sources for referenced peers' coverage."
}

func (c *documentsShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs show", ""); err != nil {
		return err
	}
	if err := rt.documentFlags(); err != nil {
		return err
	}
	raw, prefixed := strings.CutPrefix(c.ID, "document-observation:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !prefixed || err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
		return errs.Usage("document ID must be document-observation: followed by a positive decimal ID; use docs list")
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return documentError(err)
	}
	detail, readErr := st.DocumentObservation(rt.ctx, id)
	closeErr := st.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return documentError(err)
	}
	return rt.documentsRead("docs show", detail.Observation.SourceID, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return nil, store.ErrDocumentObservationNotFound
		}
		current, err := st.DocumentObservation(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		item := documentDetailItem{documentItem: documentItemOf(current.Observation, rt.g.MaxText),
			References: current.References, ReferencesTotal: current.ReferencesTotal, ReferencesTruncated: current.ReferencesTruncated}
		result := newList(shape(rt, []documentDetailItem{item}), false)
		result.SourceCoverage = documentsCoverage([]store.KnowledgeSource{current.Source})
		return result, nil
	})
}

type documentsSourcesCmd struct {
	SourceID int64 `name:"source-id" help:"Only this positive registered document source ID; this command never captures."`
}

func (c *documentsSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "docs sources", ""); err != nil {
		return err
	}
	if err := rt.documentFlags(); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be a positive document source ID")
	}
	report, err := runDocumentsSync(rt.ctx, syncer.DocumentOptions{DBPath: rt.dbPath, SourceID: c.SourceID, MaxAge: 0})
	if err != nil {
		return documentError(err)
	}
	coverage := documentsCoverage(report.Sources)
	result := newList(shape(rt, coverage.Sources), false)
	result.SourceCoverage, result.sourceOnly = coverage, true
	if result.Count == 0 {
		result.Note = "no document sources registered; run m365crawl docs sync or docs sync --root DIR"
	}
	return rt.write("docs sources", result)
}
