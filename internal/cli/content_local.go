package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/documenttext"
	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/officecapture"
	"github.com/ourostack/m365crawl/internal/store"
)

type contentCaptureCmd struct {
	File string `help:"Exact absolute ordinary resident local file; mutually exclusive with --from. No discovery/hydration."`
	From string `help:"Selected document-observation:N original native local file URL; never FriendlyPath/title."`
	Kind string `help:"Requested docx, xlsx or pptx intent; absent infers recognized extension. Package verification is still required."`
}

const localSnapshotMatchScope = "current_archived_acquisition_for_this_source_instance"

const localContentHelp = "Explicit resident capture: `content capture --file /absolute/file.docx [--kind docx|xlsx|pptx]` or `content capture --from document-observation:N`. Requested kind is intent, not verified format. Only guarded ordinary resident APFS/no-materialization (Mac) or fixed NTFS/no-recall (Windows) files qualify; no Office launch, discovery, network, hydration, decryption or Linux acquisition.\n" +
	"Supported OOXML text/cached cell values are not layout or recalculated formulas. Compound unreadable has no verified format/body; readable empty supersedes old text. Limits remain source64MiB, member32MiB, selected64MiB, text4MiB, sharedStrings4MiB, entries8192, XML tokens8388608, depth128, paragraphs65536, cells131072, parts512 (inclusive); caller limits never raise them.\n" +
	"Archive admission additionally bounds the complete serialized structured result at32MiB; exceeding it is an error, never a clipped success.\n" +
	"local-text-observation:N reads first100 occurrences in native order. CURRENT/current_snapshot and snapshot_match mean the current archived acquisition for THIS source instance, not the latest incarnation at its locator and never a live source check. An old instance can be retained=true while its own snapshot matches. Own immutable snapshot/source-byte digest differs from representation digest. File modified clocks are filesystem clocks, not cached document/server/capture clocks; year-one/unavailable parsed clocks are null with exact raw evidence retained. Unknown generation is independent; raw observed zeros remain visible.\n"

func (contentCaptureCmd) Help() string { return localContentHelp + contentHelp }

func localFlagPresent(args []string, name string) bool {
	return slices.ContainsFunc(args, func(arg string) bool { return arg == name || strings.HasPrefix(arg, name+"=") })
}

func (c *contentCaptureCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "content capture", ""); err != nil {
		return err
	}
	if err := rt.contentFlags(); err != nil {
		return err
	}
	if localFlagPresent(rt.args, "--file") == localFlagPresent(rt.args, "--from") ||
		(localFlagPresent(rt.args, "--file") && c.File == "") || (localFlagPresent(rt.args, "--from") && c.From == "") ||
		(localFlagPresent(rt.args, "--kind") && c.Kind == "") {
		return errs.Usage("content capture needs exactly one nonempty --file or --from; --kind if supplied must be docx, xlsx or pptx")
	}
	var st *store.Store
	if c.From != "" {
		if _, err := documentObservationID(c.From); err != nil {
			return err
		}
		var err error
		st, err = store.OpenReadOnly(rt.ctx, rt.dbPath)
		if err != nil {
			return contentError(err)
		}
	}
	input, ref, err := localTextInput(rt.ctx, st, *c)
	if st != nil {
		err = errors.Join(err, st.Close())
	}
	if err != nil {
		return err
	}
	origin := "explicit"
	if c.Kind == "" {
		origin = "extension"
	}
	return rt.captureLocalText(input, origin, ref)
}

func localTextInput(ctx context.Context, st *store.Store, c contentCaptureCmd) (officecapture.TextInput, *store.LocalTextReference, error) {
	if (c.File == "") == (c.From == "") {
		return officecapture.TextInput{}, nil, errs.Usage("select one --file or --from")
	}
	path := c.File
	var ref *store.LocalTextReference
	if c.From != "" {
		id, err := documentObservationID(c.From)
		if err != nil {
			return officecapture.TextInput{}, nil, err
		}
		if st == nil {
			return officecapture.TextInput{}, nil, contentError(store.ErrNoArchive)
		}
		detail, err := st.DocumentObservation(ctx, id)
		if err != nil {
			return officecapture.TextInput{}, nil, err
		}
		native := store.OriginalDocumentURL(detail.Observation)
		path, err = localFileURL(native, goruntime.GOOS)
		if err != nil {
			return officecapture.TextInput{}, nil, errs.Usage("selected original native URL is not an unambiguous supported local file; select an absolute resident path with content capture --file instead (no cloud Office fetch)")
		}
		ref = &store.LocalTextReference{DocumentObservationID: id, OriginalURL: native, AssociationState: "reference_locator_match"}
	}
	kind := c.Kind
	if kind == "" {
		kind = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	input := officecapture.TextInput{Path: path, RequestedKind: kind}
	if !officecapture.ValidTextInput(input) {
		return input, nil, errs.Usage("capture requires an exact absolute clean UTF8 local path and requested docx, xlsx or pptx kind")
	}
	return input, ref, nil
}

func localFileURL(raw, nativePlatform string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || u.Opaque != "" || u.User != nil || (u.Host != "" && u.Host != "localhost") ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return "", errors.New("unsupported file URL")
	}
	path := u.Path // net/url performs exactly one percent decoding.
	pathPlatform := "darwin"
	if !utf8.ValidString(path) || strings.ContainsAny(path, "\x00\\%") || strings.HasPrefix(path, "//") ||
		!strings.HasPrefix(path, "/") || len(path) > 32768 {
		return "", errors.New("ambiguous file URL")
	}
	if nativePlatform == "windows" {
		pathPlatform = "windows"
		if len(path) < 4 || path[0] != '/' || path[2] != ':' || path[3] != '/' {
			return "", errors.New("file URL has no local drive")
		}
		path = strings.ReplaceAll(path[1:], "/", `\`)
		if strings.Contains(path[2:], ":") {
			return "", errors.New("alternate stream URL")
		}
	} else if strings.Contains(path, ":") {
		return "", errors.New("ambiguous file URL colon")
	}
	if !officecapture.ValidTextRegistration(officecapture.TextInput{Path: path, RequestedKind: "docx"}, pathPlatform) {
		return "", errors.New("unclean file URL")
	}
	return path, nil
}

func localObservationID(value string) (int64, error) {
	raw, ok := strings.CutPrefix(value, "local-text-observation:")
	if !ok || raw == "" {
		return 0, errs.Usage("expected positive decimal local-text-observation:N")
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, errs.Usage("expected positive decimal local-text-observation:N")
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errs.Usage("expected positive decimal local-text-observation:N")
	}
	return id, nil
}

func documentObservationID(value string) (int64, error) {
	raw, ok := strings.CutPrefix(value, "document-observation:")
	if !ok {
		return 0, errs.Usage("--from requires document-observation:N")
	}
	id, err := localObservationID("local-text-observation:" + raw)
	if err != nil {
		return 0, errs.Usage("--from requires positive decimal document-observation:N")
	}
	return id, nil
}

func (rt *runtime) captureLocalText(input officecapture.TextInput, origin string, ref *store.LocalTextReference) error {
	var report *listResult
	err := rt.cloudWrite(rt.ctx, func(st *store.Store) error {
		a, err := st.BeginLocalTextAttempt(rt.ctx, store.KnowledgeInput{Kind: store.LocalTextKind, Platform: goruntime.GOOS,
			Locator: store.LocalTextLocator(input), At: transcriptsNow()}, input)
		if err != nil {
			return err
		}
		var capture store.LocalTextCapture
		err = officecapture.WithTextCapture(rt.ctx, input, func(c *officecapture.TextCapture) error {
			result, err := c.ReadText(rt.ctx)
			if err != nil {
				return err
			}
			capture = store.LocalTextCapture{RequestedKind: input.RequestedKind, Evidence: c.Evidence, SourceByteDigest: c.SourceByteDigest, Result: result, AcquiredAt: c.AcquiredAt}
			if result.State == "text_observations" {
				kind := result.Kind
				capture.VerifiedKind = &kind
			}
			return nil
		})
		if err != nil {
			code, status := "local_text_capture_failed", "failed"
			var fixed *officecapture.ReadError
			if errors.As(err, &fixed) {
				code = fixed.Code
			}
			if rt.ctx.Err() != nil {
				code, status = "local_text_cancelled", "cancelled"
			}
			ctx, cancel := context.WithTimeout(context.WithoutCancel(rt.ctx), 10*time.Second)
			defer cancel()
			recordErr := st.RecordLocalTextFailure(ctx, a, store.KnowledgeOutcome{Status: status, Reason: code, At: transcriptsNow()})
			if recordErr != nil {
				return errors.Join(err, recordErr)
			}
			return &errs.Coded{Code: code, Exit: errs.ExitUsage, Message: fmt.Sprintf("explicit resident capture refused; registration_id=%d retained for diagnostics", a.LocatorID),
				Fix: "Inspect content sources; retry explicitly with content capture --file " + strconv.Quote(input.Path) + " --kind " + input.RequestedKind}
		}
		if err := st.CommitLocalText(rt.ctx, a, store.LocalTextCommit{Capture: capture, RequestOrigin: origin, Reference: ref}); err != nil {
			return err
		}
		snapshot, err := st.LocalTextSnapshot(rt.ctx, store.LocalTextFilter{})
		if err != nil {
			return err
		}
		report, err = localCaptureResult(rt, snapshot, a.ID)
		return err
	})
	if err != nil {
		return contentError(err)
	}
	report.sourceOnly = true
	return rt.write("content capture", report)
}

func localCaptureResult(rt *runtime, snapshot store.LocalTextSnapshot, attemptID int64) (*listResult, error) {
	for _, src := range snapshot.Sources {
		if src.Source.AdmittedID != attemptID {
			continue
		}
		for _, row := range snapshot.Rows {
			if row.SourceID == src.Source.ID && !row.Retained {
				report := newList(shape(rt, []any{localItemOf(row, src, rt.g.MaxText)}), false)
				report.LocalContentCoverage = localCoverageOf([]store.LocalTextSource{src}, rt.g.MaxText)
				return report, nil
			}
		}
	}
	return nil, errors.New("local text: admitted result missing")
}

type LocalTextItem struct {
	ID                   string                               `json:"id"`
	Kind                 string                               `json:"kind"`
	SourceID             int64                                `json:"source_id"`
	CaptureID            int64                                `json:"capture_id"`
	RegistrationID       int64                                `json:"registration_id"`
	RepresentationDigest string                               `json:"representation_digest"`
	Snapshot             store.LocalFileSnapshot              `json:"snapshot"`
	CurrentSnapshot      *store.LocalFileSnapshot             `json:"current_snapshot"`
	RequestedKind        string                               `json:"requested_kind"`
	VerifiedKind         *string                              `json:"verified_kind"`
	State                string                               `json:"state"`
	Retained             bool                                 `json:"retained"`
	Partial              bool                                 `json:"partial"`
	Losses               []documenttext.Loss                  `json:"losses"`
	Paragraphs           []documenttext.Paragraph             `json:"paragraphs"`
	Cells                []documenttext.Cell                  `json:"cells"`
	OccurrencesTotal     int                                  `json:"occurrences_total"`
	PreviewTruncated     bool                                 `json:"preview_truncated"`
	TextTruncated        bool                                 `json:"text_truncated"`
	FileLabel            string                               `json:"file_label"`
	FileLabelTruncated   bool                                 `json:"file_label_truncated"`
	ModifiedAt           *time.Time                           `json:"file_modified_at"`
	ClockDomain          string                               `json:"clock_domain"`
	CapturedAt           time.Time                            `json:"captured_at"`
	SnapshotMatch        string                               `json:"snapshot_match"`
	SnapshotMatchScope   string                               `json:"snapshot_match_scope"`
	FullTextSQL          string                               `json:"full_text_sql"`
	References           []store.LocalTextReferenceOccurrence `json:"references,omitempty"`
	ReferencesTotal      int                                  `json:"references_total,omitempty"`
	ReferencesTruncated  bool                                 `json:"references_truncated,omitempty"`
}

func cloneLocalSnapshot(s store.LocalFileSnapshot) store.LocalFileSnapshot {
	b, _ := json.Marshal(s)
	var out store.LocalFileSnapshot
	_ = json.Unmarshal(b, &out) // known concrete DTO, no unsupported values or float transport
	return out
}

func shapeLocalText(row store.LocalTextRow, maxText int) LocalTextItem {
	r := row.Capture.Result
	item := LocalTextItem{ID: fmt.Sprintf("local-text-observation:%d", row.ID), Kind: store.LocalTextKind, SourceID: row.SourceID, CaptureID: row.CaptureID,
		RepresentationDigest: row.RepresentationDigest, Snapshot: cloneLocalSnapshot(row.Snapshot), RequestedKind: row.Capture.RequestedKind,
		State: r.State, Retained: row.Retained, Partial: r.Partial, Losses: slices.Clone(r.Losses), OccurrencesTotal: len(r.Paragraphs) + len(r.Cells),
		ClockDomain: "native_filesystem_modified", CapturedAt: row.CapturedAt, SnapshotMatch: "unavailable", SnapshotMatchScope: localSnapshotMatchScope,
		FullTextSQL: fmt.Sprintf("SELECT o.*, n.* FROM local_text_observations AS o JOIN local_file_snapshots AS n ON n.rowid=o.snapshot_id WHERE o.rowid=%d;", row.ID)}
	if row.Capture.VerifiedKind != nil {
		v := *row.Capture.VerifiedKind
		item.VerifiedKind = &v
	}
	item.ModifiedAt = item.Snapshot.Evidence.ModifiedAt
	item.Paragraphs = slices.Clone(r.Paragraphs[:min(100, len(r.Paragraphs))])
	item.Cells = slices.Clone(r.Cells[:min(100-len(item.Paragraphs), len(r.Cells))])
	item.PreviewTruncated = item.OccurrencesTotal > 100
	for i := range item.Paragraphs {
		value, cut := truncateRunes(item.Paragraphs[i].Text, maxText)
		item.Paragraphs[i].Text = value
		item.TextTruncated = item.TextTruncated || cut
	}
	for i := range item.Cells {
		value, cut := truncateRunes(item.Cells[i].Value, maxText)
		item.Cells[i].Value = value
		item.TextTruncated = item.TextTruncated || cut
	}
	return item
}

func localItemOf(row store.LocalTextRow, source store.LocalTextSource, maxText int) LocalTextItem {
	item := shapeLocalText(row, maxText)
	item.RegistrationID = source.RegistrationID
	item.FileLabel, item.FileLabelTruncated = truncateRunes(source.Path, maxText)
	if source.CurrentSnapshot != nil {
		current := cloneLocalSnapshot(*source.CurrentSnapshot)
		item.CurrentSnapshot = &current
		if row.Snapshot.Evidence.GenerationQualified && current.Evidence.GenerationQualified {
			item.SnapshotMatch = "differs"
			if row.Snapshot.ID == current.ID {
				item.SnapshotMatch = "matches"
			}
		}
	}
	return item
}

type localSourceItem struct {
	RegistrationID        int64                    `json:"registration_id"`
	SourceID              *int64                   `json:"source_id"`
	Kind                  string                   `json:"kind"`
	State                 string                   `json:"state"`
	Scope                 string                   `json:"scope"`
	Reason                string                   `json:"reason"`
	FileLabel             string                   `json:"file_label"`
	FileLabelTruncated    bool                     `json:"file_label_truncated"`
	RequestedKind         string                   `json:"requested_kind"`
	CurrentSnapshot       *store.LocalFileSnapshot `json:"current_snapshot"`
	LastGoodObservationID *string                  `json:"last_good_observation_id"`
	SnapshotMatch         string                   `json:"snapshot_match"`
	SnapshotMatchScope    string                   `json:"snapshot_match_scope"`
	Losses                map[string]int           `json:"losses"`
	AttemptedAt           *time.Time               `json:"attempted_at"`
	AdmittedAt            *time.Time               `json:"admitted_at"`
	Retry                 string                   `json:"retry"`
}
type localCoverage struct {
	Scope   string            `json:"scope"`
	Sources []localSourceItem `json:"sources"`
}

func localCoverageOf(sources []store.LocalTextSource, maxText int) *localCoverage {
	out := &localCoverage{Scope: "explicit_archived_resident_observations", Sources: []localSourceItem{}}
	for _, s := range sources {
		item := localSourceItem{RegistrationID: s.RegistrationID, Kind: store.LocalTextKind, State: s.Source.Status, Scope: s.Source.Coverage, Reason: s.Source.Reason,
			RequestedKind: s.RequestedKind, SnapshotMatch: s.SnapshotMatch, SnapshotMatchScope: localSnapshotMatchScope, Losses: maps.Clone(s.Source.Losses), AttemptedAt: s.Source.AttemptedAt, AdmittedAt: s.Source.AdmittedAt,
			Retry: "m365crawl content capture --file " + strconv.Quote(s.Path) + " --kind " + s.RequestedKind}
		item.FileLabel, item.FileLabelTruncated = truncateRunes(s.Path, maxText)
		if s.Source.ID > 0 {
			id := s.Source.ID
			item.SourceID = &id
		}
		if s.CurrentSnapshot != nil {
			current := cloneLocalSnapshot(*s.CurrentSnapshot)
			item.CurrentSnapshot = &current
		}
		if s.LastGoodObservationID != nil {
			id := fmt.Sprintf("local-text-observation:%d", *s.LastGoodObservationID)
			item.LastGoodObservationID = &id
		}
		out.Sources = append(out.Sources, item)
	}
	return out
}

func localSourceFor(sources []store.LocalTextSource, id int64) store.LocalTextSource {
	for _, src := range sources {
		if src.Source.ID == id {
			return src
		}
	}
	return store.LocalTextSource{}
}

func contentKeys() []string {
	return dedupe(append(typeKeys[cloudItem](), typeKeys[LocalTextItem]()...))
}
func contentSourceKeys() []string {
	return dedupe(append(typeKeys[cloudSourceItem](), typeKeys[localSourceItem]()...))
}

func LocalPreviewText(item LocalTextItem) string {
	var values []string
	for _, p := range item.Paragraphs {
		values = append(values, p.Text)
	}
	for _, c := range item.Cells {
		values = append(values, c.Value)
	}
	return strings.Join(values, "\n")
}

func contentListResult(rt *runtime, st *store.Store, c contentListCmd, query string, since, until time.Time) (*listResult, error) {
	cloud := store.CloudSnapshot{}
	local := store.LocalTextSnapshot{}
	if st != nil {
		var err error
		cloud, err = st.CloudSnapshot(rt.ctx, store.CloudFilter{Query: query, ModifiedSince: since, ModifiedUntil: until})
		if err != nil {
			return nil, err
		}
		local, err = st.LocalTextSnapshot(rt.ctx, store.LocalTextFilter{Query: query, ModifiedSince: since, ModifiedUntil: until})
		if err != nil {
			return nil, err
		}
	}
	if c.SourceID > 0 {
		cloud.Sources = slices.DeleteFunc(cloud.Sources, func(s store.CloudSource) bool { return s.Source.ID != c.SourceID })
		local.Sources = slices.DeleteFunc(local.Sources, func(s store.LocalTextSource) bool { return s.Source.ID != c.SourceID })
		cloud.Rows = slices.DeleteFunc(cloud.Rows, func(r store.CloudObservationRow) bool { return r.SourceID != c.SourceID })
		local.Rows = slices.DeleteFunc(local.Rows, func(r store.LocalTextRow) bool { return r.SourceID != c.SourceID })
		if len(cloud.Sources)+len(local.Sources) == 0 {
			return nil, store.ErrKnowledgeSourceUnknown
		}
	}
	hits := []searchHit{}
	for _, row := range cloud.Rows {
		at, label := time.Time{}, ""
		if row.Result.File.ModifiedAt != nil {
			at = *row.Result.File.ModifiedAt
		}
		if row.Result.File.Title != nil {
			label = *row.Result.File.Title
		}
		hits = append(hits, searchHit{at: at, source: searchContent, cloudLabel: label, cloudSourceID: row.SourceID, cloudObservationID: row.ID, item: cloudItemOf(row, cloudSourceFor(cloud.Sources, row.SourceID), rt.g.MaxText)})
	}
	for _, row := range local.Rows {
		at := time.Time{}
		if row.Snapshot.Evidence.ModifiedAt != nil {
			at = *row.Snapshot.Evidence.ModifiedAt
		}
		src := localSourceFor(local.Sources, row.SourceID)
		hits = append(hits, searchHit{at: at, source: searchContent, localText: true, cloudLabel: src.Path, cloudSourceID: row.SourceID, cloudObservationID: row.ID, item: localItemOf(row, src, rt.g.MaxText)})
	}
	sortSearchHits(hits)
	truncated := cloud.Truncated || local.Truncated || len(hits) > c.Limit
	hits = hits[:min(c.Limit, len(hits))]
	items := make([]any, len(hits))
	for i, h := range hits {
		items[i] = h.item
	}
	out := newList(shape(rt, items), truncated)
	out.ContentCoverage = cloudCoverageOf(cloud.Sources, rt.g.MaxText)
	out.LocalContentCoverage = localCoverageOf(local.Sources, rt.g.MaxText)
	return out, nil
}
