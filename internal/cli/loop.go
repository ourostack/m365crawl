package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

type loopGroup struct {
	Sync    loopSyncCmd    `cmd:"" help:"Register archived cached Loop snapshot records and project their channels; reads only the archive."`
	List    loopListCmd    `cmd:"" help:"List retained Loop channel observations with stable IDs and source coverage."`
	Search  loopSearchCmd  `cmd:"" help:"Search retained Loop channel text and paths."`
	Show    loopShowCmd    `cmd:"" help:"Inspect one loop-channel:ID: that one channel of one cached snapshot, with its snapshot and source state."`
	Sources loopSourcesCmd `cmd:"" help:"Inspect registered Loop sources, raw-match state and failures without projecting."`
}

const loopSemanticsHelp = "Each row is one channel of one immutable cached snapshot: partial channel text (text_state=partial_snapshot), not a full or current Loop page, table, author or server history. m365crawl never fetches a component, replays operations or opens Loop.\n" +
	"source_id is the stable registration ID of one archived record tuple (source, database, store, key_json); it is allocated when `loop sync` first sees the tuple, before any successful projection, so a failed or metadata-only source has one too. snapshot_id names one admitted version of that record (its exact value digest and reader qualification); id loop-channel:N names one channel of one snapshot and never moves to a newer channel.\n" +
	"sequence is the channel's native sequence number; snapshot_sequence and latest_sequence are the snapshot's. pending_operations counts operations the cache held but m365crawl did not apply; zero is still not a claim of service-current content.\n" +
	"modified_at is always null: the cache records no native modification time. record_updated_at_at_admission is the archive record's update clock when this snapshot was first admitted and projected_at when it was first projected; neither is native edit time. raw_updated_at and raw_removed_at are the archived record's current clocks.\n" +
	"matches_retained_value says the archived record's current bytes and reader version are this snapshot's (compared by the record fingerprint the archive stored at admission, so an edit made outside m365crawl that leaves content_hash unchanged is not detected); latest_cached_snapshot says the row comes from the newest snapshot m365crawl has admitted for its source: the latest cached snapshot, which is still not the current Loop page. retained is its inverse, true on a row of an older cached snapshot. raw_present=false means the archived record is gone from the app's cache (it left Teams' cache at raw_removed_at); the archive keeps its last bytes and nothing was deleted.\n"

// loopRecoveryHelp says when a retry helps and what loop sync can and cannot recover.
const loopRecoveryHelp = "retry_needed is true on a failed or cancelled source, where `loop sync --source-id N` (the source's retry command) can help. A metadata-only source is false: its latest snapshot was read and has no channel text, so a retry changes nothing until Teams caches more of the component and `m365crawl sync` archives it; its fix says so. On a healthy source the command only refreshes. " +
	"loop sync reads only the archive. When the archived record is gone from the app's cache (raw_present false) the archive still holds its last bytes: loop sync can project those again, and it cannot recover anything they lack, such as missing blobs (missing_blobs, loss loop_cache_blob_missing) or later edits. Those arrive only after Teams caches the component again and `m365crawl sync` archives it.\n"

func (loopGroup) Help() string {
	return "Loop channels are text projected from cached Loop snapshot records already in the archive (database fluidDriverCache, store driverStorage.V3).\n" +
		"Run `loop sync` to register and project them, then `loop list`, `loop search WORDS` or `search WORDS --source loop`, and follow a returned loop-channel:ID with `loop show loop-channel:ID`. `loop sources` lists each registration, its snapshot_match (no_admission, matches, differs, unavailable) and why it failed; a failed or cancelled source is retried with `loop sync --source-id N`.\n" +
		loopSemanticsHelp + loopRecoveryHelp +
		"archive_age_seconds on a Loop result is the age of the last complete `m365crawl sync`, the sync that archives the cached records Loop is projected from; it is null when no sync has completed, and it is not the age of the Loop text.\n" +
		"Ordinary reads refresh registered sources whose last attempt is older than --max-age; --max-age 0 and `loop sources` only read the archive. Date, account, author, conversation, team, folder, mention, deletion, HTML and profile filters cannot qualify Loop channels: with --source loop they are refused, and they leave Loop out of --source all with a notice.\n" +
		fmt.Sprintf("`loop sync` registers at most %d new archived records per run and never fails on a larger backlog: while its result has discovery_remaining true, run it again.", store.LoopDiscoveryBatch)
}

type loopTupleItem struct {
	Source   string `json:"source"`
	Database string `json:"database"`
	Store    string `json:"store"`
	KeyJSON  string `json:"key_json"`
}

type loopChannelItem struct {
	ID                         string         `json:"id"`
	SourceID                   int64          `json:"source_id"`
	SnapshotID                 int64          `json:"snapshot_id"`
	FileID                     string         `json:"file_id"`
	Path                       string         `json:"path"`
	PathTruncated              bool           `json:"path_truncated,omitempty"`
	Text                       string         `json:"text"`
	TextTruncated              bool           `json:"text_truncated,omitempty"`
	Sequence                   int64          `json:"sequence"`
	SnapshotSequence           int64          `json:"snapshot_sequence"`
	LatestSequence             int64          `json:"latest_sequence"`
	Markers                    int            `json:"markers"`
	PendingOperations          int            `json:"pending_operations"`
	SnapshotPendingOperations  int            `json:"snapshot_pending_operations"`
	MissingBlobs               int            `json:"missing_blobs"`
	Partial                    bool           `json:"partial"`
	Losses                     map[string]int `json:"losses"`
	ModifiedAt                 *time.Time     `json:"modified_at"`
	RecordUpdatedAtAtAdmission time.Time      `json:"record_updated_at_at_admission"`
	ProjectedAt                time.Time      `json:"projected_at"`
	RawUpdatedAt               *time.Time     `json:"raw_updated_at"`
	RawRemovedAt               *time.Time     `json:"raw_removed_at"`
	RawPresent                 bool           `json:"raw_present"`
	MatchesRetainedValue       bool           `json:"matches_retained_value"`
	Retained                   bool           `json:"retained"`
	LatestCachedSnapshot       bool           `json:"latest_cached_snapshot"`
	SnapshotTenantID           string         `json:"snapshot_tenant_id"`
	SnapshotUserID             string         `json:"snapshot_user_id"`
	AssociationState           string         `json:"association_state"`
	TextState                  string         `json:"text_state"`
}

// loopDetailItem is the one channel loop show returns: the same fields, with a detail block in text.
type loopDetailItem struct{ loopChannelItem }

func loopChannelItemOf(row store.LoopChannelRow, maxText int) loopChannelItem {
	text, textCut := truncateRunes(row.Channel.Text, maxText)
	path, pathCut := truncateRunes(row.Channel.Path, maxText)
	losses := map[string]int{}
	for _, loss := range row.Snapshot.Losses {
		losses[loss.Code] += loss.Count
	}
	return loopChannelItem{ID: fmt.Sprintf("loop-channel:%d", row.ID), SourceID: row.RegistrationID, SnapshotID: row.SnapshotID,
		FileID: row.Snapshot.FileID, Path: path, PathTruncated: pathCut, Text: text, TextTruncated: textCut,
		Sequence: row.Channel.Sequence, SnapshotSequence: row.Snapshot.Sequence, LatestSequence: row.Snapshot.LatestSequence,
		Markers: row.Channel.Markers, PendingOperations: row.Channel.PendingOperations,
		SnapshotPendingOperations: row.Snapshot.PendingOperations, MissingBlobs: row.Snapshot.MissingBlobs,
		Partial: row.Snapshot.Partial, Losses: losses, RecordUpdatedAtAtAdmission: row.RecordUpdatedAtAtAdmission.UTC(),
		ProjectedAt: row.ProjectedAt.UTC(), RawUpdatedAt: utcPointer(row.RawUpdatedAt), RawRemovedAt: utcPointer(row.RawRemovedAt),
		RawPresent: row.RawPresent, MatchesRetainedValue: row.MatchesRetainedValue, Retained: row.Retained, LatestCachedSnapshot: !row.Retained,
		SnapshotTenantID: row.SnapshotTenantID, SnapshotUserID: row.SnapshotUserID,
		AssociationState: "unresolved", TextState: "partial_snapshot"}
}

func utcPointer(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	u := at.UTC()
	return &u
}

var runLoopSync = syncer.SyncLoop

func loopError(err error) error {
	switch {
	case errors.Is(err, store.ErrKnowledgeSourceUnknown):
		return &errs.Coded{Code: "loop_source_unknown", Exit: errs.ExitUsage,
			Message: "Loop source not found", Fix: "Run `m365crawl loop sources` for registered source IDs."}
	case errors.Is(err, store.ErrLoopChannelNotFound) || errors.Is(err, store.ErrNoArchive):
		return &errs.Coded{Code: "loop_channel_unknown", Exit: errs.ExitUsage,
			Message: "Loop channel not found", Fix: "Run `m365crawl loop list` for retained loop-channel:IDs."}
	}
	return asCoded(err)
}

func (rt *runtime) loopFlags() error {
	if rt.g.Account != "" || rt.g.OutlookAccount != "" || rt.g.OutlookProfile != "" {
		return errs.Usage("Loop channels have no resolved account or profile; drop --account, --outlook-account and --outlook-profile")
	}
	return nil
}

// refreshLoop runs the participating refresh of registered sources. A source or archive failure is
// served as refresh_error beside the retained sources; cancellation is the command's error.
func (rt *runtime) refreshLoop(sourceID int64) (*loopCoverage, error) {
	// Every caller reads the sources again with its rows, so a run with nothing due reads none.
	report, refreshErr := runLoopSync(rt.ctx, syncer.LoopOptions{DBPath: rt.dbPath, SourceID: sourceID, MaxAge: rt.maxAge, Probe: true})
	if refreshErr == nil {
		return rt.loopCoverageOf(report.Sources, report.Diagnostics), nil
	}
	if rt.ctx.Err() != nil || errors.Is(refreshErr, context.Canceled) || errors.Is(refreshErr, context.DeadlineExceeded) {
		return nil, refreshErr
	}
	retained, err := runLoopSync(rt.ctx, syncer.LoopOptions{DBPath: rt.dbPath, SourceID: sourceID})
	if err != nil {
		return nil, errors.Join(refreshErr, err)
	}
	coverage := rt.loopCoverageOf(retained.Sources, nil)
	coded := errs.DBError(refreshErr)
	errors.As(loopError(refreshErr), &coded)
	coverage.RefreshError = &syncError{Code: coded.Code, Message: coded.Message}
	return coverage, nil
}

// loopRead refreshes, then reads the archive in one function, and writes the result with
// loop_coverage outside the item shaping.
func (rt *runtime) loopRead(label string, sourceID int64, fn func(*store.Store) (*listResult, error)) error {
	if err := rt.loopFlags(); err != nil {
		return err
	}
	if sourceID < 0 {
		return errs.Usage("--source-id must be a positive Loop source ID")
	}
	coverage, err := rt.refreshLoop(sourceID)
	if err != nil {
		return loopError(err)
	}
	rt.loopCoverage = coverage
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if errors.Is(err, store.ErrNoArchive) {
		st = nil
	} else if err != nil {
		return loopError(err)
	}
	result, readErr := fn(st)
	var closeErr error
	if st != nil {
		closeErr = st.Close()
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		return loopError(err)
	}
	if result.LoopCoverage == nil {
		result.LoopCoverage = coverage
	}
	result.LoopCoverage.RefreshError = coverage.RefreshError
	result.LoopCoverage.Diagnostics = coverage.Diagnostics
	result.sourceOnly = true
	result.ArchiveAgeSeconds = rt.loopArchiveAge()
	if result.Count == 0 && result.Note == "" {
		result.Note = loopEmptyNote(result.LoopCoverage)
	}
	return rt.write(label, result)
}

// loopArchiveAge is archive_age_seconds of a Loop result: the seconds since the last complete
// sync, which archives the cached records Loop is projected from. It is nil when no sync has
// completed, or the archive is missing or cannot say.
func (rt *runtime) loopArchiveAge() *int64 {
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return nil
	}
	defer func() { _ = st.Close() }()
	last, err := rt.freshness(st)
	if err != nil || last.IsZero() {
		return nil
	}
	age := max(int64(rt.now().Sub(last).Seconds()), 0)
	return &age
}

func loopEmptyNote(coverage *loopCoverage) string {
	if len(coverage.items) == 0 {
		return "no Loop source is registered; run m365crawl loop sync"
	}
	return "no Loop channel matched; loop_coverage describes each registered source, and m365crawl loop sources shows failures and retries"
}

type loopSyncResult struct {
	Projected int `json:"projected"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	// InvalidRecords counts unregistered archived records whose identity is outside the reader's
	// bounds; diagnostics name the first of them by records rowid.
	InvalidRecords int `json:"invalid_records"`
	// DiscoveryRemaining says this run registered one full batch and left
	// DiscoveryRemainingRecords unregistered records for the next run.
	DiscoveryRemaining        bool          `json:"discovery_remaining"`
	DiscoveryRemainingRecords int           `json:"discovery_remaining_records"`
	Notice                    string        `json:"notice,omitempty"`
	LoopCoverage              *loopCoverage `json:"loop_coverage"`
}

type loopSyncCmd struct {
	SourceID int64 `name:"source-id" help:"Refresh only this registered Loop source ID (loop sources lists them); without it, also register new archived records."`
}

func (loopSyncCmd) Help() string {
	return fmt.Sprintf("Without --source-id, loop sync refreshes every registered source, then registers and projects new archived records in record order, at most %d per run and one value at a time. "+
		"When more remain, the result has discovery_remaining true, discovery_remaining_records and a notice: run m365crawl loop sync again to continue. That is not an error; the run exits 0.\n"+
		"A record whose identity or owner fields are outside the reader's bounds is never registered and never counts toward the %d: invalid_records counts them and diagnostics name the first %d as `records rowid N` "+
		"(inspect one with m365crawl sql \"select database, store, octet_length(key_json) from records where rowid=N\"); no archived string is printed.\n"+
		"A source that cannot be read or parsed is still registered and recorded as failed with a fixed reason, and keeps its last good snapshot. If the run itself fails after writing, the result on stdout still reports what it committed.\n"+
		loopRecoveryHelp,
		store.LoopDiscoveryBatch, store.LoopDiscoveryBatch, store.LoopDiagnosticLimit)
}

func (c *loopSyncCmd) Run(rt *runtime) error {
	if err := rt.loopFlags(); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be a positive Loop source ID")
	}
	report, err := runLoopSync(rt.ctx, syncer.LoopOptions{DBPath: rt.dbPath, SourceID: c.SourceID, Explicit: true, Discover: c.SourceID == 0})
	result := loopSyncResult{Projected: report.Projected, Unchanged: report.Unchanged, Skipped: report.Skipped, Failed: report.Failed,
		InvalidRecords: report.InvalidRecords, DiscoveryRemaining: report.DiscoveryRemaining,
		DiscoveryRemainingRecords: report.DiscoveryRemainingRecords, LoopCoverage: rt.loopCoverageOf(report.Sources, report.Diagnostics)}
	if report.DiscoveryRemaining {
		result.Notice = fmt.Sprintf("this run registered %d new Loop sources, the most one run takes; %s still unregistered: run %s again",
			store.LoopDiscoveryBatch, plural(report.DiscoveryRemainingRecords, "archived record is", "archived records are"),
			rt.recoveryCommand(false, "", "loop", "sync"))
	}
	if err != nil {
		// A run that failed after writing still reports what it committed, then fails.
		if report.Projected+report.Unchanged+report.Failed > 0 {
			_ = rt.write("loop sync", result) // the run's own error is the one to return
		}
		return loopError(err)
	}
	return rt.write("loop sync", result)
}

const loopOrderHelp = "Order: full channel path (ASCII case-insensitive), source ID, snapshot ID, then channel ID, before --limit."

type loopListCmd struct {
	SourceID int64  `name:"source-id" help:"Only this registered Loop source ID; loop sources lists IDs."`
	Query    string `help:"Words in channel text or path."`
	Limit    int    `default:"50" help:"Maximum channels; 0 returns all. Check truncated."`
	label    string
}

func (loopListCmd) Help() string { return loopSemanticsHelp + loopOrderHelp }

func (c *loopListCmd) Run(rt *runtime) error {
	label := firstOf(c.label, "loop list")
	if err := checkCommandFields(rt, label, ""); err != nil {
		return err
	}
	if c.Limit < 0 {
		return errs.Usage("--limit must be nonnegative; 0 returns all Loop channels")
	}
	return rt.loopRead(label, c.SourceID, func(st *store.Store) (*listResult, error) {
		if st == nil {
			return newList(nil, false), nil
		}
		snapshot, err := st.LoopSnapshot(rt.ctx, store.LoopFilter{Query: c.Query, SourceID: c.SourceID, Limit: c.Limit})
		if err != nil {
			return nil, err
		}
		out := make([]loopChannelItem, len(snapshot.Rows))
		for i, row := range snapshot.Rows {
			out[i] = loopChannelItemOf(row, rt.g.MaxText)
		}
		result := newList(shape(rt, out), snapshot.Truncated)
		result.LoopCoverage = loopSnapshotCoverage(rt, snapshot.Sources)
		if len(out) > 0 {
			result.rowsHint = loopRowsHint
		}
		return result, nil
	})
}

// loopRowsHint is the text line under a Loop channel table that returned rows.
const loopRowsHint = "partial snapshots of cached text, not full or current pages"

type loopSearchCmd struct {
	Query    string `arg:"" help:"Words in channel text or path; required."`
	SourceID int64  `name:"source-id" help:"Only this registered Loop source ID."`
	Limit    int    `default:"50" help:"Maximum channels; 0 returns all. Check truncated."`
}

func (loopSearchCmd) Help() string { return loopSemanticsHelp + loopOrderHelp }

func (c *loopSearchCmd) Run(rt *runtime) error {
	if strings.TrimSpace(c.Query) == "" {
		return errs.Usage("loop search needs words; use loop list for an unfiltered list")
	}
	return (&loopListCmd{Query: c.Query, SourceID: c.SourceID, Limit: c.Limit, label: "loop search"}).Run(rt)
}

type loopShowCmd struct {
	ID string `arg:"" help:"loop-channel:ID returned by loop list/search or shared search."`
}

func (loopShowCmd) Help() string {
	return "loop show returns exactly one item: one channel of one snapshot, with that snapshot's id, sequence numbers, losses and clocks. It does not list the source's other snapshots: `loop list --source-id N` lists every retained channel row of a source, rows of older snapshots included.\n" +
		loopSemanticsHelp + "The ID is resolved to its original source before that source alone is refreshed; the same immutable channel is returned even when a newer snapshot exists (then latest_cached_snapshot=false and retained=true)."
}

func (c *loopShowCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "loop show", ""); err != nil {
		return err
	}
	if err := rt.loopFlags(); err != nil {
		return err
	}
	raw, prefixed := strings.CutPrefix(c.ID, "loop-channel:")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !prefixed || err != nil || id <= 0 || raw != strconv.FormatInt(id, 10) {
		return errs.Usage("Loop channel ID must be loop-channel: followed by a positive decimal ID; use loop list")
	}
	st, err := store.OpenReadOnly(rt.ctx, rt.dbPath)
	if err != nil {
		return loopError(err)
	}
	row, _, readErr := st.LoopChannel(rt.ctx, id)
	closeErr := st.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return loopError(err)
	}
	return rt.loopRead("loop show", row.RegistrationID, func(st *store.Store) (*listResult, error) {
		current, source, err := st.LoopChannel(rt.ctx, id)
		if err != nil {
			return nil, err
		}
		result := newList(shape(rt, []loopDetailItem{{loopChannelItemOf(current, rt.g.MaxText)}}), false)
		result.LoopCoverage = loopSnapshotCoverage(rt, []store.LoopSource{source})
		return result, nil
	})
}

type loopSourcesCmd struct {
	SourceID int64 `name:"source-id" help:"Only this registered Loop source ID; this command never projects."`
}

func (c *loopSourcesCmd) Run(rt *runtime) error {
	if err := checkCommandFields(rt, "loop sources", ""); err != nil {
		return err
	}
	if err := rt.loopFlags(); err != nil {
		return err
	}
	if c.SourceID < 0 {
		return errs.Usage("--source-id must be a positive Loop source ID")
	}
	report, err := runLoopSync(rt.ctx, syncer.LoopOptions{DBPath: rt.dbPath, SourceID: c.SourceID})
	if err != nil {
		return loopError(err)
	}
	coverage := rt.loopCoverageOf(report.Sources, nil)
	result := newList(shape(rt, coverage.items), false)
	result.LoopCoverage, result.sourceOnly = coverage, true
	result.ArchiveAgeSeconds = rt.loopArchiveAge()
	if result.Count == 0 {
		result.Note = loopEmptyNote(coverage)
	}
	return rt.write("loop sources", result)
}
