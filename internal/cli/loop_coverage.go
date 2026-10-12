package cli

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ourostack/m365crawl/internal/render"
	"github.com/ourostack/m365crawl/internal/store"
)

// loopCoverage is the participating Loop source state of a result. It sits outside the items, so
// --fields, clipping and zero hits never hide it.
type loopCoverage struct {
	Scope        string           `json:"scope"`
	State        string           `json:"state"`
	Sources      []loopSourceItem `json:"sources"`
	RefreshError *syncError       `json:"refresh_error,omitempty"`
	Diagnostics  []string         `json:"diagnostics,omitempty"`
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
	Effect                string         `json:"effect,omitempty"`
	Fix                   string         `json:"fix,omitempty"`
}

// loopFailureFix is the remedy of each fixed Loop failure reason, as docs/commands.md lists them.
var loopFailureFix = map[string]string{
	"loop_cache_identity_unqualified": "the record's identity or owner fields exceed the reader's bounds or are not UTF-8, so they were not read",
	"loop_cache_record_missing":       "the archived record is gone (a credential rule may have cleared it); nothing can be projected until Teams caches it again and m365crawl sync archives it",
	"loop_cache_value_missing":        "the archived record holds no value (Teams cached the key without a readable value); run m365crawl sync, then retry",
	"loop_cache_value_too_large":      "the archived value is larger than 8 MiB, so it was not loaded",
	"loop_cache_value_unqualified":    "the archived value or its clocks are not in the text form the reader accepts",
	"loop_cache_snapshot_unsupported": "the cached value is not a Loop snapshot shape this version reads; upgrade m365crawl, then retry",
	"loop_cache_snapshot_too_large":   "the snapshot exceeds the reader's blob, channel, segment or nesting limits",
	"loop_cache_reader_failed":        "the reader failed unexpectedly; retry, and open an issue at " + issuesURL + " if it repeats",
	"loop_cache_input_changed":        "the archived record changed while it was projected (a sync ran at the same time); retry",
	"loop_cache_cancelled":            "the projection was interrupted; retry",
	"stale_attempt":                   "another projection of the same source finished first; run m365crawl loop sources to see the current state",
}

func loopCoverageOf(sources []store.LoopSource, diagnostics []string) *loopCoverage {
	out := &loopCoverage{Scope: "loop_cache_channels", State: "unknown", Sources: []loopSourceItem{}}
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
		if fix, failed := loopFailureFix[health.Reason]; failed && (health.Status == "failed" || health.Status == "cancelled") {
			item.Fix = fix + "; retry with " + retry
			item.Effect = "nothing has been projected from this source yet, so it adds no channel"
			if source.LastSnapshotID > 0 {
				item.Effect = "the last good snapshot stays current, so its channels are still listed"
			}
		} else if health.Coverage == "metadata_only" {
			item.Effect = "the latest snapshot has no channel this version reads, so it adds no channel; earlier snapshots stay listed with retained=true"
			item.Fix = "nothing to fix; after Teams caches more of the component and m365crawl sync archives it, retry with " + retry
		}
		out.Sources = append(out.Sources, item)
	}
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
	if coverage == nil || (len(coverage.Sources) == 0 && coverage.RefreshError == nil && len(coverage.Diagnostics) == 0) {
		return
	}
	_, _ = fmt.Fprintf(rt.stdout, "loop coverage: %s (%s)\n", coverage.State, coverage.Scope)
	rows := [][]string{}
	for _, source := range coverage.Sources {
		rows = append(rows, []string{strconv.FormatInt(source.SourceID, 10), source.Status, source.Coverage, source.SnapshotMatch,
			strconv.FormatInt(source.LastSnapshotID, 10), source.Reason})
	}
	render.Table(rt.stdout, []string{"source", "status", "coverage", "snapshot_match", "last_snapshot", "reason"}, rows, rt.color)
	for _, source := range coverage.Sources {
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

// loopRowState is the text table's state column: whether the row is the source's latest admitted
// snapshot and whether the archived record still holds exactly its bytes.
func loopRowState(item loopChannelItem) string {
	state := "current"
	if item.Retained {
		state = "retained"
	}
	if !item.MatchesRetainedValue {
		state += ", raw differs"
	}
	if !item.RawPresent {
		state += ", raw removed"
	}
	return state
}
