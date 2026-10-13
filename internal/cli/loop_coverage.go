package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
)

// loopCoverage is the participating Loop source state of a result. It sits outside the items, so
// --fields, clipping and zero hits never hide it. Loop commands carry the full source rows; shared
// search carries the compact ones.
type loopCoverage struct {
	Scope        string     `json:"scope"`
	State        string     `json:"state"`
	Totals       loopTotals `json:"totals"`
	Sources      any        `json:"sources"` // []loopSourceItem, or []loopSearchSource in shared search
	RefreshError *syncError `json:"refresh_error,omitempty"`
	Diagnostics  []string   `json:"diagnostics,omitempty"`
	items        []loopSourceItem
}

// loopTotals counts the participating sources by their latest attempt and by snapshot_match.
type loopTotals struct {
	Registered   int `json:"registered"`
	Failed       int `json:"failed"`
	MetadataOnly int `json:"metadata_only"`
	Matches      int `json:"matches"`
	Differs      int `json:"differs"`
	Unavailable  int `json:"unavailable"`
	NoAdmission  int `json:"no_admission"`
}

// loopSearchSource is one source in shared search: enough to see that it participated and whether
// it is healthy, and the command that shows the rest.
type loopSearchSource struct {
	SourceID      int64  `json:"source_id"`
	State         string `json:"state"`
	SnapshotMatch string `json:"snapshot_match"`
	RawPresent    bool   `json:"raw_present"`
	Reason        string `json:"reason"`
	Inspect       string `json:"inspect"`
}

// compact is the coverage shared search carries: the same totals, short source rows and no tuple.
func (c *loopCoverage) compact() *loopCoverage {
	out := *c
	sources := make([]loopSearchSource, len(c.items))
	for i, item := range c.items {
		state := item.Status
		if item.Coverage == "metadata_only" && state != "failed" && state != "cancelled" {
			state = "metadata_only"
		}
		sources[i] = loopSearchSource{SourceID: item.SourceID, State: state, SnapshotMatch: item.SnapshotMatch, RawPresent: item.RawPresent,
			Reason: firstOf(item.Reason, item.RawReason), Inspect: "m365crawl loop sources --source-id " + strconv.FormatInt(item.SourceID, 10)}
	}
	out.Sources = sources
	return &out
}

type loopSourceItem struct {
	SourceID              int64          `json:"source_id"`
	Kind                  string         `json:"kind"`
	Tuple                 loopTupleItem  `json:"tuple"`
	Status                string         `json:"status"`
	Coverage              string         `json:"coverage"`
	Reason                string         `json:"reason"`
	RawReason             string         `json:"raw_reason"`
	AttemptID             int64          `json:"attempt_id"`
	AdmittedAttemptID     int64          `json:"admitted_attempt_id"`
	IncarnationID         int64          `json:"incarnation_id"`
	LastSnapshotID        int64          `json:"last_snapshot_id"`
	SnapshotMatch         string         `json:"snapshot_match"`
	Qualification         string         `json:"qualification"`
	SnapshotQualification string         `json:"snapshot_qualification"`
	RawPresent            bool           `json:"raw_present"`
	RawValuePresent       bool           `json:"raw_value_present"`
	RawUpdatedAt          *time.Time     `json:"raw_updated_at"`
	RawRemovedAt          *time.Time     `json:"raw_removed_at"`
	CurrentTenantID       string         `json:"current_tenant_id"`
	CurrentUserID         string         `json:"current_user_id"`
	SnapshotTenantID      string         `json:"snapshot_tenant_id"`
	SnapshotUserID        string         `json:"snapshot_user_id"`
	AttemptedAt           *time.Time     `json:"attempted_at"`
	AdmittedAt            *time.Time     `json:"admitted_at"`
	Losses                map[string]int `json:"losses"`
	Retry                 string         `json:"retry"`
	// RetryNeeded says the retry command can help now: the latest attempt failed or was cancelled.
	// A metadata-only source was read whole, so a retry changes nothing until its record changes.
	RetryNeeded bool   `json:"retry_needed"`
	Effect      string `json:"effect,omitempty"`
	Fix         string `json:"fix,omitempty"`
}

// loopFailureFix is the remedy of each fixed Loop failure reason, as docs/commands.md lists them.
// {retry} is the source's own retry command, named once where the remedy needs it.
var loopFailureFix = map[string]string{
	"loop_cache_identity_unqualified": "the archived record's identity or owner fields are now outside the reader's bounds or not UTF-8, so they are not read or shown; after m365crawl sync archives a readable record, run {retry}",
	"loop_cache_record_missing":       "the archived record is gone, so nothing can be projected; after Teams caches it again and m365crawl sync archives it, run {retry}",
	"loop_cache_value_missing":        "the archived record holds no value (Teams cached the key without a readable value); run m365crawl sync, then {retry}",
	"loop_cache_value_too_large":      "the archived value is larger than 8 MiB, so it was not loaded; open an issue at " + issuesURL + " if Loop wrote it",
	"loop_cache_value_unqualified":    "the archived value or its clocks are not in the text form the reader accepts; run m365crawl sync, then {retry}",
	"loop_cache_snapshot_unsupported": "the cached value is not a Loop snapshot shape this version reads; upgrade m365crawl, then run {retry}",
	"loop_cache_snapshot_too_large":   "the snapshot exceeds the reader's blob, channel, segment or nesting limits; open an issue at " + issuesURL + " if Loop wrote it",
	"loop_cache_reader_failed":        "the value could not be read or parsed; run {retry}, and open an issue at " + issuesURL + " if it repeats",
	"loop_cache_input_changed":        "the archived record changed while it was projected; run {retry}",
	"loop_cache_cancelled":            "the projection was interrupted; run {retry}",
	"stale_attempt":                   "another projection of the same source finished first; run m365crawl loop sources to see the current state",
}

func loopCoverageOf(sources []store.LoopSource, diagnostics []string) *loopCoverage {
	out := &loopCoverage{Scope: "loop_cache_channels", State: "not_registered", items: []loopSourceItem{}}
	if len(diagnostics) > 0 {
		out.Diagnostics = diagnostics
	}
	for _, source := range sources {
		out.State = "registered"
		health := source.Health
		retry := "m365crawl loop sync --source-id " + strconv.FormatInt(source.Registration.ID, 10)
		item := loopSourceItem{SourceID: source.Registration.ID, Kind: store.LoopKind,
			Tuple: loopTupleItem{Source: source.Registration.Tuple.Source, Database: source.Registration.Tuple.Database,
				Store: source.Registration.Tuple.Store, KeyJSON: source.Registration.Tuple.KeyJSON},
			Status: health.Status, Coverage: health.Coverage, Reason: health.Reason, RawReason: source.Reason,
			AttemptID: health.AttemptID, AdmittedAttemptID: health.AdmittedID, IncarnationID: health.ID,
			LastSnapshotID: source.LastSnapshotID, SnapshotMatch: source.SnapshotMatch, Qualification: store.LoopQualification,
			SnapshotQualification: source.SnapshotQualification, RawPresent: source.RawPresent, RawValuePresent: source.RawValuePresent,
			RawUpdatedAt: utcPointer(source.RawUpdatedAt), RawRemovedAt: utcPointer(source.RawRemovedAt),
			CurrentTenantID: source.Registration.TenantID, CurrentUserID: source.Registration.UserID,
			SnapshotTenantID: source.LastSnapshotTenantID, SnapshotUserID: source.LastSnapshotUserID,
			AttemptedAt: health.AttemptedAt, AdmittedAt: health.AdmittedAt, Losses: health.Losses, Retry: retry}
		if item.Losses == nil {
			item.Losses = map[string]int{}
		}
		out.Totals.Registered++
		failed := health.Status == "failed" || health.Status == "cancelled"
		if fix, known := loopFailureFix[health.Reason]; known && failed {
			item.Fix = strings.ReplaceAll(fix, "{retry}", retry)
			item.Effect = "nothing has been projected from this source yet, so it adds no channel"
			if source.LastSnapshotID > 0 {
				item.Effect = "the last good snapshot stays the latest cached snapshot, so its channels are still listed"
			}
		} else if health.Coverage == "metadata_only" {
			item.Effect = "the latest snapshot has no channel this version reads, so it adds no channel; earlier snapshots stay listed with retained=true"
			item.Fix = "nothing to fix, and a retry now changes nothing: the snapshot was read and holds no channel text; after Teams caches more of the component and m365crawl sync archives it, run " + retry
		}
		switch {
		case failed:
			out.Totals.Failed++
		case health.Coverage == "metadata_only":
			out.Totals.MetadataOnly++
		}
		item.RetryNeeded = failed
		switch source.SnapshotMatch {
		case "matches":
			out.Totals.Matches++
		case "differs":
			out.Totals.Differs++
		case "unavailable":
			out.Totals.Unavailable++
		default:
			out.Totals.NoAdmission++
		}
		out.items = append(out.items, item)
	}
	out.Sources = out.items
	return out
}

// loopSnapshotCoverage is the coverage read with the rows, carrying the refresh's error and diagnostics.
func loopSnapshotCoverage(rt *runtime, sources []store.LoopSource) *loopCoverage {
	coverage := loopCoverageOf(sources, nil)
	if rt.loopCoverage != nil {
		coverage.RefreshError, coverage.Diagnostics = rt.loopCoverage.RefreshError, rt.loopCoverage.Diagnostics
	}
	return coverage
}

func (rt *runtime) renderLoopCoverage(coverage *loopCoverage) {
	// With nothing registered, the result's note already says how to register; an empty table
	// would only add noise to every shared search.
	if coverage == nil || (len(coverage.items) == 0 && coverage.RefreshError == nil && len(coverage.Diagnostics) == 0) {
		return
	}
	_, _ = fmt.Fprintf(rt.stdout, "loop coverage: %s (%s)\n", coverage.State, coverage.Scope)
	rows := [][]string{}
	for _, source := range coverage.items {
		rows = append(rows, []string{strconv.FormatInt(source.SourceID, 10), source.Status, source.Coverage, source.SnapshotMatch,
			strconv.FormatInt(source.LastSnapshotID, 10), source.Reason})
	}
	render.Table(rt.stdout, []string{"source", "status", "coverage", "snapshot_match", "last_snapshot", "reason"}, rows, rt.color)
	for _, source := range coverage.items {
		if source.Fix != "" {
			_, _ = fmt.Fprintf(rt.stdout, "loop source %d: %s %s. %s.\n  fix: %s\n", source.SourceID, source.Status,
				firstOf(source.Reason, source.Coverage), source.Effect, source.Fix)
		}
	}
	for _, diagnostic := range coverage.Diagnostics {
		_, _ = fmt.Fprintf(rt.stdout, "loop diagnostic: %s\n", diagnostic)
	}
	if coverage.RefreshError != nil {
		_, _ = fmt.Fprintf(rt.stdout, "loop refresh error: %s: %s (serving retained archive)\n", coverage.RefreshError.Code, coverage.RefreshError.Message)
	}
}

// loopRowState is the text table's snapshot column: whether the row is the source's latest cached
// snapshot or an older one, never "current" (no row is the current Loop page), and whether the
// archived record still holds exactly its bytes and is still in the app's cache.
func loopRowState(item loopChannelItem) string {
	state := "latest cached snapshot"
	if item.Retained {
		state = "older cached snapshot"
	}
	if !item.MatchesRetainedValue {
		state += ", archived record differs"
	}
	if !item.RawPresent {
		state += ", " + loopRecordGone
	}
	return state
}

// loopRecordGone is what text output says of a row whose raw_present is false.
const loopRecordGone = "archived record gone from the app's cache"

// loopSnapshotColumn is the text column that holds loopRowState; it is never clipped.
const loopSnapshotColumn = "snapshot"

var loopListColumns = []string{"source", "id", "path", "sequence", "text_state", loopSnapshotColumn, "text"}

// loopTextColumn is the index of the clipped text column of loopListColumns.
const loopTextColumn = 6

func loopListRow(x loopChannelItem) []string {
	return []string{strconv.FormatInt(x.SourceID, 10), x.ID, x.Path, strconv.FormatInt(x.Sequence, 10), x.TextState, loopRowState(x), oneLine(x.Text)}
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func loopStamp(at *time.Time) string {
	if at == nil {
		return "-"
	}
	return stamp(*at)
}

// renderLoopDetails prints what text loop show adds to the list row: the evidence that the text is
// one partial snapshot, its exact versions and its clocks, then the channel text.
func (rt *runtime) renderLoopDetails(result *listResult) {
	for _, item := range result.Items {
		detail, ok := item.(loopDetailItem)
		if !ok {
			continue
		}
		state := "latest cached snapshot of this source"
		if detail.Retained {
			state = "older cached snapshot (a newer snapshot of this source exists)"
		}
		record := "archived record still in the app's cache: yes"
		if !detail.RawPresent {
			record = loopRecordGone
		}
		losses := "none"
		if len(detail.Losses) > 0 {
			codes := make([]string, 0, len(detail.Losses))
			for code, count := range detail.Losses {
				codes = append(codes, code+" "+strconv.Itoa(count))
			}
			slices.Sort(codes)
			losses = strings.Join(codes, ", ")
		}
		_, _ = fmt.Fprintf(rt.stdout, "\n%s  %s\n", detail.ID, detail.Path)
		_, _ = fmt.Fprintf(rt.stdout, "text state: %s (partial channel text of one cached snapshot, not a full or current Loop page)\n", detail.TextState)
		_, _ = fmt.Fprintf(rt.stdout, "source %d, snapshot %d, file %s; association: %s\n", detail.SourceID, detail.SnapshotID, detail.FileID, detail.AssociationState)
		_, _ = fmt.Fprintf(rt.stdout, "sequence %d (snapshot %d, latest %d); pending operations %d (snapshot %d); markers %d; missing blobs %d; partial: %s\n",
			detail.Sequence, detail.SnapshotSequence, detail.LatestSequence, detail.PendingOperations, detail.SnapshotPendingOperations,
			detail.Markers, detail.MissingBlobs, yesNo(detail.Partial))
		_, _ = fmt.Fprintf(rt.stdout, "losses: %s\n", losses)
		_, _ = fmt.Fprintf(rt.stdout, "%s; archived record matches this snapshot: %s; %s\n", state,
			yesNo(detail.MatchesRetainedValue), record)
		_, _ = fmt.Fprintf(rt.stdout, "record updated at admission: %s; projected: %s; raw updated: %s; raw removed: %s\n",
			stamp(detail.RecordUpdatedAtAtAdmission), stamp(detail.ProjectedAt), loopStamp(detail.RawUpdatedAt), loopStamp(detail.RawRemovedAt))
		_, _ = fmt.Fprintln(rt.stdout, "modified: unknown (the cache records no native modification time)")
		_, _ = fmt.Fprintf(rt.stdout, "\n%s\n", detail.Text)
	}
}
