package cli

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/sharepointcontent"
	"github.com/ourostack/m365crawl/internal/store"
)

type contentGroup struct {
	Capture contentCaptureCmd `cmd:"" help:"Explicitly capture one guarded already-resident Office file or selected original native file reference."`
	Fetch   contentFetchGroup `cmd:"" help:"Explicitly acquire one supported page or selected Stream transcript using the tool-owned headless browser."`
	Signin  contentSigninCmd  `cmd:"" help:"Explicit user-agreed visible window for the tool-owned profile."`
	List    contentListCmd    `cmd:"" help:"List immutable retained observations offline, known native file-modified newest first."`
	Search  contentSearchCmd  `cmd:"" help:"Search supported retained text/title/native metadata offline."`
	Show    contentShowCmd    `cmd:"" help:"Read one cloud-observation:N or local-text-observation:N offline, with first100 occurrences and full archive SQL."`
	Sources contentSourcesCmd `cmd:"" help:"Read registered source coverage offline, including failed and metadata-only zero-hit participants."`
}
type contentFetchGroup struct {
	Page   contentFetchPageCmd   `cmd:"" help:"Explicitly acquire a supported rich-text page projection (always partial)."`
	Stream contentFetchStreamCmd `cmd:"" help:"Explicitly acquire one verified selected transcript, or verified metadata without a selected body."`
}

const contentHelp = "Explicit fetch only: `content fetch page URL` or `content fetch stream URL [--transcript-id ID]`. Supported HTTPS tenant.sharepoint.com or tenant.sharepoint-df.com /sites/NAME or /teams/NAME targets: /SitePages/NAME.aspx, or /_layouts/15/stream.aspx?id=ENCODED_NATIVE_FILE_PATH. No sharing-ticket replay, library crawl or automatic retry.\n" +
	"Reads and search are archive-only, regardless of --max-age. Pages are supported rich-text projections, always partial, never rendered layouts. Stream complete means only one verified selected native transcript; no_transcript and metadata_only have no selected body. Last-good body keeps its original file/account/selection and clocks; retained is not current content.\n" +
	"registration_id selects an explicit retry, source_id an admitted file/account instance, cloud-observation:N an immutable representation. Distinct requested selections register separately. Sign-in is only `content signin URL --user-agreed` after the user agrees; no credentials or auth state are exported. Native Windows acquisition remains unqualified; offline archive reads are portable.\n"

func (contentGroup) Help() string      { return localContentHelp + contentHelp }
func (contentFetchGroup) Help() string { return contentHelp }
func splitContentSite(path string) string {
	parts := strings.Split(path, "/")
	return "/" + parts[1] + "/" + parts[2]
}
func (rt *runtime) contentFlags() error {
	if rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("cloud service account and local file ownership evidence are unresolved; content does not accept account or Outlook profile filters")
	}
	return nil
}
func contentError(err error) error {
	if errors.Is(err, store.ErrKnowledgeSourceUnknown) || errors.Is(err, store.ErrNoArchive) {
		return &errs.Coded{Code: "content_unknown", Exit: errs.ExitUsage, Message: "content observation, source or registration not found",
			Fix: "Read `m365crawl content sources` or `content list`; explicitly content capture --file FILE for a resident Office file, or fetch a supported page/Stream URL."}
	}
	return asCoded(err)
}

type contentListCmd struct {
	SourceID      int64  `help:"Only this admitted native instance ID (not registration retry ID)."`
	ModifiedSince string `help:"Native file-modified lower bound; unknown clocks excluded."`
	ModifiedUntil string `help:"Native file-modified upper bound; unknown clocks excluded."`
	Limit         int    `default:"50" help:"Maximum returned observations; inspect truncated."`
}
type contentSearchCmd struct {
	Query string `arg:"" help:"Words, quoted phrases or trailing-prefix * to search."`
	contentListCmd
}
type contentShowCmd struct {
	ID string `arg:"" help:"Positive decimal cloud-observation:N or local-text-observation:N ID."`
}
type contentSourcesCmd struct {
	SourceID int64 `help:"Only this admitted instance ID; zero reads all registrations and instances."`
}

func (contentListCmd) Help() string   { return localContentHelp + contentHelp }
func (contentSearchCmd) Help() string { return localContentHelp + contentHelp }
func (contentShowCmd) Help() string {
	return localContentHelp + contentHelp + "Preview has first100 text/entry occurrences in native order. Cloud native_id_prefix and native_id_truncated describe at most128 Unicode runes, never a complete clipped identifier. Local native IDs are complete strings. full_text_sql uses a generated internal row ID against this same archive, not a native identifier."
}
func (contentSourcesCmd) Help() string          { return localContentHelp + contentHelp }
func (c *contentListCmd) Run(rt *runtime) error { return c.run(rt, "content list", "") }
func (c *contentSearchCmd) Run(rt *runtime) error {
	if strings.TrimSpace(c.Query) == "" {
		return errs.Usage("content search needs words; use content list")
	}
	return c.run(rt, "content search", c.Query)
}
func (c *contentListCmd) run(rt *runtime, label, query string) error {
	if err := checkCommandFields(rt, label, ""); err != nil {
		return err
	}
	if err := checkLimit(c.Limit); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be positive")
	}
	since, err := rt.when("--modified-since", c.ModifiedSince)
	if err != nil {
		return err
	}
	until, err := rt.when("--modified-until", c.ModifiedUntil)
	if err != nil {
		return err
	}
	return rt.contentRead(label, func(st *store.Store) (*listResult, error) {
		return contentListResult(rt, st, *c, query, since, until)
	})
}
func (c *contentShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "content show", ""); err != nil {
		return err
	}
	if strings.HasPrefix(c.ID, "local-text-observation:") {
		id, err := localObservationID(c.ID)
		if err != nil {
			return err
		}
		return rt.contentRead("content show", func(st *store.Store) (*listResult, error) {
			if st == nil {
				return nil, store.ErrKnowledgeSourceUnknown
			}
			detail, err := st.LocalTextDetail(rt.ctx, id)
			if err != nil {
				return nil, err
			}
			item := localItemOf(detail.Observation, detail.Source, rt.g.MaxText)
			item.References, item.ReferencesTotal, item.ReferencesTruncated = detail.References, detail.ReferencesTotal, detail.ReferencesTruncated
			res := newList(shape(rt, []any{item}), false)
			res.LocalContentCoverage = localCoverageOf([]store.LocalTextSource{detail.Source}, rt.g.MaxText)
			return res, nil
		})
	}
	id, err := cloudObservationID(c.ID)
	if err != nil {
		return err
	}
	return rt.contentRead("content show", func(st *store.Store) (*listResult, error) {
		if st == nil {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		row, source, err := st.CloudDetail(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		res := newList(shape(rt, []any{cloudItemOf(row, source, rt.g.MaxText)}), false)
		res.ContentCoverage = cloudCoverageOf([]store.CloudSource{source}, rt.g.MaxText)
		return res, nil
	})
}
func (c *contentSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "content sources", ""); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be positive")
	}
	return rt.contentRead("content sources", func(st *store.Store) (*listResult, error) {
		sources := []store.CloudSource{}
		local := []store.LocalTextSource{}
		if st != nil {
			var err error
			sources, err = st.CloudSources(rt.ctx, 0)
			if err != nil {
				return nil, err
			}
			local, err = st.LocalTextSources(rt.ctx, 0)
			if err != nil {
				return nil, err
			}
		} else if c.SourceID > 0 {
			return nil, store.ErrKnowledgeSourceUnknown
		}
		if c.SourceID > 0 {
			sources = slices.DeleteFunc(sources, func(s store.CloudSource) bool { return s.Source.ID != c.SourceID })
			local = slices.DeleteFunc(local, func(s store.LocalTextSource) bool { return s.Source.ID != c.SourceID })
			if len(sources)+len(local) == 0 {
				return nil, store.ErrKnowledgeSourceUnknown
			}
		}
		coverage := cloudCoverageOf(sources, rt.g.MaxText)
		localCoverage := localCoverageOf(local, rt.g.MaxText)
		out := make([]any, len(coverage.Sources))
		for i, item := range coverage.Sources {
			out[i] = item
		}
		for _, item := range localCoverage.Sources {
			out = append(out, item)
		}
		res := newList(shape(rt, out), false)
		res.ContentCoverage = coverage
		res.LocalContentCoverage = localCoverage
		return res, nil
	})
}
func cloudObservationID(value string) (int64, error) {
	raw, ok := strings.CutPrefix(value, "cloud-observation:")
	if !ok || raw == "" {
		return 0, errs.Usage("expected cloud-observation:N with a positive decimal int64")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errs.Usage("expected cloud-observation:N with a positive decimal int64")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errs.Usage("expected cloud-observation:N with a positive decimal int64")
	}
	return id, nil
}
func (rt *runtime) contentRead(label string, fn func(*store.Store) (*listResult, error)) error {
	if err := rt.contentFlags(); err != nil {
		return err
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		st = nil
	} else if err != nil {
		return contentError(err)
	}
	res, readErr := fn(st)
	if st != nil {
		readErr = errors.Join(readErr, st.Close())
	}
	if readErr != nil {
		return contentError(readErr)
	}
	res.sourceOnly = true
	if res.Count == 0 {
		res.Note = "no matching archived content; inspect content sources, explicitly content capture --file FILE or content fetch page/stream URL"
	}
	return rt.write(label, res)
}

type cloudNativeID struct {
	NativeIDPrefix    *string `json:"native_id_prefix"`
	NativeIDTruncated bool    `json:"native_id_truncated"`
}

func cloudNativePrefix(id *string) cloudNativeID {
	if id == nil {
		return cloudNativeID{}
	}
	value, cut := *id, false
	runes := []rune(value)
	if len(runes) > 128 {
		value, cut = string(runes[:128]), true
	}
	return cloudNativeID{NativeIDPrefix: &value, NativeIDTruncated: cut}
}

type cloudSourceItem struct {
	RegistrationID        int64          `json:"registration_id"`
	SourceID              *int64         `json:"source_id"`
	Kind                  string         `json:"kind"`
	State                 string         `json:"state"`
	Scope                 string         `json:"scope"`
	Reason                string         `json:"reason"`
	RequestedSelection    cloudNativeID  `json:"requested_selection"`
	CurrentSelection      cloudNativeID  `json:"current_selection"`
	LastGoodObservationID *string        `json:"last_good_observation_id"`
	SelectionMatch        string         `json:"selection_match"`
	AttemptedAt           *time.Time     `json:"attempted_at"`
	AdmittedAt            *time.Time     `json:"admitted_at"`
	SourceObservedAt      *time.Time     `json:"source_observed_at"`
	Losses                map[string]int `json:"losses"`
	Retry                 string         `json:"retry"`
}
type cloudCoverage struct {
	Scope   string            `json:"scope"`
	Sources []cloudSourceItem `json:"sources"`
}

func cloudCoverageOf(sources []store.CloudSource, _ int) *cloudCoverage {
	coverage := &cloudCoverage{Scope: "explicit_archived_observations", Sources: []cloudSourceItem{}}
	for _, source := range sources {
		item := cloudSourceItem{RegistrationID: source.RegistrationID, Kind: source.Source.Kind, State: source.Source.Status,
			Scope: source.Source.Coverage, Reason: source.Source.Reason, RequestedSelection: cloudNativePrefix(source.RequestedTranscriptID),
			CurrentSelection: cloudNativePrefix(source.CurrentTranscriptID), SelectionMatch: source.SelectionMatch,
			AttemptedAt: source.Source.AttemptedAt, AdmittedAt: source.Source.AdmittedAt, SourceObservedAt: source.Source.SourceObservedAt, Losses: source.Source.Losses}
		if source.Source.ID > 0 {
			id := source.Source.ID
			item.SourceID = &id
		}
		if source.LastGoodObservationID != nil {
			value := fmt.Sprintf("cloud-observation:%d", *source.LastGoodObservationID)
			item.LastGoodObservationID = &value
		}
		kind := "page"
		if source.Source.Kind == store.CloudStreamKind {
			kind = "stream"
		}
		item.Retry = fmt.Sprintf("m365crawl content fetch %s --registration-id %d", kind, source.RegistrationID)
		coverage.Sources = append(coverage.Sources, item)
	}
	return coverage
}
func cloudSourceFor(sources []store.CloudSource, id int64) store.CloudSource {
	for _, source := range sources {
		if source.Source.ID == id {
			return source
		}
	}
	return store.CloudSource{}
}

type cloudControlPreview struct {
	Ordinal     int    `json:"ordinal"`
	TextOrdinal int    `json:"text_ordinal"`
	Type        *int64 `json:"type"`
	State       string `json:"state"`
	cloudNativeID
	Text          string `json:"text"`
	TextTruncated bool   `json:"text_truncated"`
}
type cloudEntryPreview struct {
	Ordinal int `json:"ordinal"`
	cloudNativeID
	Text          string  `json:"text"`
	TextTruncated bool    `json:"text_truncated"`
	Speaker       *string `json:"speaker"`
	StartRaw      *string `json:"start_raw"`
	EndRaw        *string `json:"end_raw"`
	StartMS       *int64  `json:"start_ms"`
	EndMS         *int64  `json:"end_ms"`
}
type cloudItem struct {
	ID                         string                          `json:"id"`
	SourceID                   int64                           `json:"source_id"`
	RegistrationID             int64                           `json:"registration_id"`
	CaptureID                  int64                           `json:"capture_id"`
	Kind                       string                          `json:"kind"`
	Title                      *string                         `json:"title"`
	Text                       string                          `json:"text"`
	TextTruncated              bool                            `json:"text_truncated"`
	DescriptiveFieldsTruncated bool                            `json:"descriptive_fields_truncated"`
	State                      string                          `json:"state"`
	Scope                      string                          `json:"scope"`
	Retained                   bool                            `json:"retained"`
	SelectionMatch             string                          `json:"selection_match"`
	SelectedTranscript         cloudNativeID                   `json:"selected_transcript"`
	NativeFile                 sharepointcontent.NativeFile    `json:"native_file"`
	NativeAccount              sharepointcontent.SourceAccount `json:"native_account"`
	ModifiedAt                 *time.Time                      `json:"modified_at"`
	CapturedAt                 time.Time                       `json:"captured_at"`
	Losses                     []sharepointcontent.Loss        `json:"losses"`
	ControlsTotal              int                             `json:"controls_total"`
	TextOccurrencesTotal       int                             `json:"text_occurrences_total"`
	EntriesTotal               int                             `json:"entries_total"`
	ControlsPreview            []cloudControlPreview           `json:"controls_preview"`
	EntriesPreview             []cloudEntryPreview             `json:"entries_preview"`
	PreviewTruncated           bool                            `json:"preview_truncated"`
	FullTextSQL                string                          `json:"full_text_sql"`
}

func cloudItemOf(row store.CloudObservationRow, source store.CloudSource, maxText int) cloudItem {
	result := row.Result
	it := cloudItem{ID: fmt.Sprintf("cloud-observation:%d", row.ID), SourceID: row.SourceID, RegistrationID: source.RegistrationID,
		CaptureID: row.CaptureID, Kind: result.Kind, Title: result.File.Title, State: result.State, Retained: row.Retained,
		NativeFile: result.File, NativeAccount: result.SourceAccount, ModifiedAt: result.File.ModifiedAt, CapturedAt: row.CapturedAt,
		Losses: result.Losses, ControlsTotal: len(result.PageControls), ControlsPreview: []cloudControlPreview{}, EntriesPreview: []cloudEntryPreview{},
		SelectionMatch: "unavailable", FullTextSQL: fmt.Sprintf("select result_json from cloud_observations where rowid=%d", row.ID)}
	it.Scope = "partial"
	if result.Transcript != nil {
		it.SelectedTranscript = cloudNativePrefix(&result.Transcript.ID)
		if !result.Partial {
			it.Scope = "selected_transcript"
		}
		if source.CurrentTranscriptID != nil {
			it.SelectionMatch = "differs"
			if result.Transcript.ID == *source.CurrentTranscriptID {
				it.SelectionMatch = "matches"
			}
		}
	}
	clip := func(value *string) *string {
		if value == nil {
			return nil
		}
		clipped, cut := truncateRunes(*value, maxText)
		it.DescriptiveFieldsTruncated = it.DescriptiveFieldsTruncated || cut
		return &clipped
	}
	it.Title = clip(it.Title)
	it.NativeFile.Title = it.Title
	it.NativeFile.Path = *clip(&it.NativeFile.Path)
	it.NativeFile.DriveID = clip(it.NativeFile.DriveID)
	it.NativeFile.ItemID = clip(it.NativeFile.ItemID)
	it.NativeFile.ModifiedRaw = clip(it.NativeFile.ModifiedRaw)
	it.NativeAccount.LoginName = *clip(&it.NativeAccount.LoginName)
	it.Text, it.TextTruncated = truncateRunes(store.CloudText(result), maxText)
	for _, control := range result.PageControls {
		for j, text := range control.Texts {
			it.TextOccurrencesTotal++
			if len(it.ControlsPreview) == 100 {
				it.PreviewTruncated = true
				continue
			}
			value, cut := truncateRunes(text, maxText)
			it.ControlsPreview = append(it.ControlsPreview, cloudControlPreview{Ordinal: control.Ordinal, TextOrdinal: j, Type: control.Type, State: control.State,
				cloudNativeID: cloudNativePrefix(control.ID), Text: value, TextTruncated: cut})
		}
	}
	if result.Transcript != nil {
		it.EntriesTotal = len(result.Transcript.Entries)
		for _, entry := range result.Transcript.Entries {
			if len(it.EntriesPreview) == 100 {
				it.PreviewTruncated = true
				break
			}
			value, cut := truncateRunes(entry.Text, maxText)
			it.EntriesPreview = append(it.EntriesPreview, cloudEntryPreview{Ordinal: entry.Ordinal, cloudNativeID: cloudNativePrefix(&entry.ID),
				Text: value, TextTruncated: cut, Speaker: clip(entry.SpeakerDisplayName), StartRaw: clip(entry.StartRaw), EndRaw: clip(entry.EndRaw), StartMS: entry.StartMS, EndMS: entry.EndMS})
		}
	}
	return it
}
