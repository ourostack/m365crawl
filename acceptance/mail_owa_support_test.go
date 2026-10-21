//go:build acceptance

package acceptance

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/hxstore"
	"github.com/ourostack/m365crawl/internal/outlookowa"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
)

type owaObservation struct {
	failure       *owaFailureState
	ctx           context.Context
	root, account string
	ownedSnapshot string
	profile       outlookowa.Profile
	census        owaRawCensus
	stable        bool
	digest        [32]byte
}

func owaStable(before, after string) bool { return before != "" && before == after }

func owaCapture(t *testing.T, p outlookowa.Profile, states ...*owaFailureState) owaObservation {
	t.Helper()
	state := owaFailureOf(states)
	defer state.phase(t, protocol.StageCapture)()
	ctx := owaWorkContext(t)
	if err := owaCheckSource(ctx, p.Source); err != nil {
		state.record(protocol.StageCapture, "source_resource_or_context_refused")
		t.Fatal("source_resource_or_context_refused")
	}
	before, fresh, err := outlookowa.Fingerprint(p)
	if err != nil {
		state.record(protocol.StageCapture, "source_fingerprint_failed")
		t.Fatal("source_fingerprint_failed")
	}
	dir, cleanup, err := owaOwnedSnapshot(ctx, p.Source, teamsdesktop.Snapshot)
	if err != nil {
		cleanup()
		state.record(protocol.StageCapture, "snapshot_refused")
		t.Fatal("snapshot_refused")
	}
	release := func() {
		state.cleanup(t, func() {
			cleanup()
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				state.record(protocol.StageCleanup, "snapshot_cleanup_failed")
				t.Error("snapshot_cleanup_failed")
			}
		})
	}
	calMu.Lock()
	calCleanups = append(calCleanups, cleanup)
	calMu.Unlock()
	t.Cleanup(release)
	after, afterFresh, err := outlookowa.Fingerprint(p)
	if err != nil {
		state.record(protocol.StageCapture, "source_fingerprint_failed")
		t.Fatal("source_fingerprint_failed")
	}
	root := filepath.Join(dir, "EBWebView")
	profileDir := filepath.Join(root, p.Name)
	idb := filepath.Join(profileDir, "IndexedDB")
	if err := os.MkdirAll(idb, 0o700); err != nil {
		state.record(protocol.StageCapture, "snapshot_shape_failed")
		t.Fatal("snapshot_shape_failed")
	}
	level := filepath.Join(idb, p.Source.Origin+".indexeddb.leveldb")
	blob := filepath.Join(idb, p.Source.Origin+".indexeddb.blob")
	for i, pair := range [][2]string{{filepath.Join(dir, "leveldb"), level}, {filepath.Join(dir, "blob"), blob}} {
		if err := os.Rename(pair[0], pair[1]); err != nil {
			state.record(protocol.StageCapture, "snapshot_shape_failed")
			var code syscall.Errno
			if errors.As(err, &code) {
				t.Fatalf("snapshot_shape_failed operation=rename index=%d errno=%d", i, code)
			}
			t.Fatal("snapshot_shape_failed operation=rename")
		}
	}
	p.Dir = profileDir
	p.Source.LevelDBDir, p.Source.BlobDir = level, blob
	return owaObservation{failure: state, ctx: ctx, root: root, ownedSnapshot: dir, account: "outlook/" + p.Name, profile: p, stable: owaStable(before, after) && fresh.Equal(afterFresh), digest: owaPayloadDigestContext(t, ctx, p.Source, state)}
}

func owaObserve(t *testing.T, p outlookowa.Profile, states ...*owaFailureState) owaObservation {
	t.Helper()
	s := owaCapture(t, p, states...)
	owaCensusObserved(t, &s)
	return s
}

func owaCensusObserved(t *testing.T, s *owaObservation) {
	t.Helper()
	owaReadIndependentCensus(t, s)
	owaCapturedIntegrity(t, s)
	if !s.stable {
		// A bounded captured-copy check can still run, but this is not live absence evidence.
		t.Log("unstable_source_observation")
	}
}

func owaCapturedIntegrity(t *testing.T, s *owaObservation) {
	t.Helper()
	defer s.failure.phase(t, protocol.StageCapture)()
	if s.digest != owaPayloadDigestContext(t, s.ctx, s.profile.Source, s.failure) {
		s.failure.record(protocol.StageCapture, "private_source_mutated")
		t.Fatal("private_source_mutated")
	}
}

func owaReadIndependentCensus(t *testing.T, s *owaObservation) {
	t.Helper()
	defer s.failure.phase(t, protocol.StageCensus)()
	var err error
	s.census, err = owaCensusContext(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		owaRecordCensusReason(s.failure, err)
		var coverage *owaCoverageRefusal
		if errors.As(err, &coverage) {
			s.failure.recordFields(protocol.StageCensus, protocol.CensusReason(err), coverage.fields)
			t.Fatalf("detail_metadata_coverage_refused fields=%v", coverage.fields)
		}
		t.Fatal("raw_census_refused")
	}
}

func owaArchive(t *testing.T, path string, states ...*owaFailureState) *store.Store {
	t.Helper()
	state := owaFailureOf(states)
	st, err := store.OpenReadOnly(owaWorkContext(t), path)
	if err != nil {
		stage := protocol.StageImport
		if state != nil && state.stage == protocol.StageAccounts {
			stage = protocol.StageAccounts
		}
		state.record(stage, "archive_open_failed")
		t.Fatal("archive_open_failed")
	}
	t.Cleanup(func() { state.cleanup(t, func() { owaCloseArchive(t, st, state) }) })
	return st
}

// Register exact flat archive cleanup with the existing TestMain owner, and verify it at the test boundary after all later-registered handles have closed.
func owaScratch(t *testing.T, states ...*owaFailureState) string {
	t.Helper()
	state := owaFailureOf(states)
	dir := calendarScratch(t)
	t.Cleanup(func() {
		state.cleanup(t, func() {
			removeFlat(dir)
			if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
				state.record(protocol.StageCleanup, "archive_cleanup_failed")
				t.Error("archive_cleanup_failed")
			}
		})
	})
	return dir
}

func owaCloseArchive(t *testing.T, st *store.Store, states ...*owaFailureState) {
	t.Helper()
	if err := st.Close(); err != nil {
		owaFailureOf(states).record(protocol.StageCleanup, "archive_close_failed")
		t.Error("archive_close_failed")
	}
}

func owaQuery(st *store.Store, q string) ([][]any, error) {
	return owaQueryContext(context.Background(), st, q)
}

func owaScalar(t *testing.T, st *store.Store, q string) int {
	t.Helper()
	rows, err := owaQuery(st, q)
	if err != nil || len(rows) != 1 || len(rows[0]) != 1 {
		t.Fatal("archive_count_refused")
	}
	n, ok := rows[0][0].(int64)
	if !ok || n < 0 || n > 1<<30 {
		t.Fatal("archive_count_invalid")
	}
	return int(n)
}

func owaExercise(t *testing.T, db string, c owaObservation) {
	t.Helper()
	defer c.failure.phase(t, protocol.StageImport)()
	opts := syncer.Options{Root: filepath.Join(c.root, "no-teams"), DBPath: db, OutlookRoot: c.root, OutlookEnabled: true, OutlookMinReadInterval: -1}
	var baseline map[string][][]any
	for step := 0; step < 3; step++ {
		opts.FullRead = step == 2
		start := time.Now()
		rep, _, err := syncer.Run(c.ctx, opts)
		owaSyncChecks(t, rep, err, c, step)
		st := owaArchive(t, db, c.failure)
		findings := owaCompareContext(c.ctx, st, c.account, c.census, c.failure)
		state := owaArchiveState(c.ctx, t, st, c.failure)
		c.failure.cleanup(t, func() { owaCloseArchive(t, st, c.failure) })
		owaArchiveFindings(t, c.failure, findings)
		if step == 0 {
			baseline = state
		} else {
			owaRepeatState(t, c.failure, state, baseline)
		}
		t.Logf("step=%d messages=%d folders=%d wall_ms=%d", step, len(c.census.messages), len(c.census.folders), time.Since(start).Milliseconds())
	}
	owaCapturedIntegrity(t, &c)
}

func owaSyncChecks(t *testing.T, rep syncer.Report, err error, c owaObservation, step int) {
	t.Helper()
	if err != nil {
		c.failure.record(protocol.StageImport, "sync_refused")
		t.Fatal("sync_refused")
	}
	src, found := mailSource(rep)
	var cal syncer.SourceReport
	for _, row := range rep.Sources {
		if row.Source == "outlook|"+c.profile.Name {
			cal = row
		}
	}
	if !found || len(rep.Sources) != 2 || src.Source != "outlook|"+c.profile.Name+"|mail" ||
		cal.Source == "" || cal.Error != nil || len(cal.Omissions) != 0 || len(src.Omissions) != 0 {
		if found && len(src.Omissions) != 0 {
			c.failure.sourceOmissions(src.Omissions)
		} else {
			c.failure.record(protocol.StageImport, "sync_source_refused")
		}
		t.Fatal("sync_source_refused")
	}
	wantStatus := syncer.StatusOK
	if step == 1 {
		wantStatus = syncer.StatusUnchanged
	}
	if src.Status != wantStatus || cal.Status != wantStatus {
		c.failure.record(protocol.StageImport, "sync_status_mismatch")
		t.Fatal("sync_status_mismatch")
	}
	m := mailCountsOf(src)
	if m.Gone != 0 || m.Evicted != 0 || m.Withheld != 0 || m.Replaced != 0 || step > 0 && (m.Added != 0 || m.Updated != 0) || step == 0 && m.Added != len(c.census.messages) {
		c.failure.record(protocol.StageImport, "sync_counts_mismatch")
		t.Fatal("sync_counts_mismatch")
	}
}

func owaArchiveFindings(t *testing.T, s *owaFailureState, findings []string) {
	t.Helper()
	if len(findings) != 0 {
		s.recordFields(protocol.StageImport, "archive_mismatch", findings)
		t.Fatalf("archive_mismatch fields=%v", findings)
	}
}

func owaRepeatState(t *testing.T, s *owaFailureState, state, baseline map[string][][]any) {
	t.Helper()
	if !reflect.DeepEqual(state, baseline) {
		s.record(protocol.StageImport, "repeat_archive_unstable")
		t.Fatal("repeat_archive_unstable")
	}
}

func owaArchiveState(ctx context.Context, t *testing.T, st *store.Store, states ...*owaFailureState) map[string][][]any {
	t.Helper()
	return owaArchiveStateLimit(ctx, t, st, owaMaxStateBytes, owaFailureOf(states))
}

func owaArchiveStateLimit(ctx context.Context, t *testing.T, st *store.Store, maxBytes int64, s *owaFailureState) map[string][][]any {
	t.Helper()
	state, err := owaArchiveStateBudget(ctx, st, maxBytes)
	if err != nil {
		reason := "archive_state_refused"
		if err.Error() == "archive_state_budget" {
			reason = "archive_state_budget"
		}
		s.record(protocol.StageImport, reason)
		t.Fatal("archive_state_refused")
	}
	return state
}

func owaArchiveStateBudget(ctx context.Context, st *store.Store, maxBytes int64) (map[string][][]any, error) {
	if maxBytes < 0 || maxBytes > owaMaxStateBytes {
		return nil, errors.New("archive_state_budget")
	}
	state := map[string][][]any{}
	var used int64
	for _, table := range []string{"mail_messages", "mail_folders", "mail_recipients", "mail_attachments", "mail_source_ids", "mail_absent", "mail_fts"} {
		// Only this fixed table allowlist enters SQL, never a source name or value.
		rows, projectedBytes, err := owaQueryMeasured(ctx, st, "select rowid,* from "+table+" order by rowid", maxBytes-used)
		if err != nil {
			if err.Error() == "query_projection_budget" {
				return nil, errors.New("archive_state_budget")
			}
			return nil, errors.New("archive_state_refused")
		}
		state[table] = rows
		used += projectedBytes
	}
	return state, nil
}

func owaPayloadDigestContext(t *testing.T, ctx context.Context, s teamsdesktop.Source, states ...*owaFailureState) [32]byte {
	t.Helper()
	if err := owaCheckSource(ctx, s); err != nil {
		owaFailureOf(states).record(protocol.StageCapture, "payload_resource_or_context_refused")
		t.Fatal("payload_resource_or_context_refused")
	}
	h := sha256.New()
	for _, dir := range []string{s.LevelDBDir, s.BlobDir} {
		err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return errors.New("payload_walk_failed")
			}
			if e.IsDir() || e.Name() == "LOCK" || strings.HasPrefix(e.Name(), "LOG") {
				return nil
			}
			f, err := os.Open(path) //nolint:gosec // exact private Snapshot-owned source tree
			if err != nil {
				return errors.New("payload_open_failed")
			}
			_, readErr := io.Copy(h, owaContextReader{ctx: ctx, reader: f})
			closeErr := f.Close()
			if readErr != nil || closeErr != nil {
				return errors.New("payload_read_failed")
			}
			return nil
		})
		if err != nil {
			owaFailureOf(states).record(protocol.StageCapture, "payload_digest_failed")
			t.Fatal("payload_digest_failed")
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func owaExpectRefusal(t *testing.T, s owaObservation) {
	t.Helper()
	o, err := outlookowa.Open(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if o != nil {
		if closeErr := o.Close(); closeErr != nil {
			t.Error("origin_close_failed")
		}
	}
	var guard *hxstore.GuardError
	if !errors.As(err, &guard) || guard.Code != "outlook_mail_layout_unsupported" {
		t.Fatal("fixed_layout_refusal_missing")
	}
}

func owaExpectLoss(t *testing.T, s owaObservation) {
	t.Helper()
	o, err := outlookowa.Open(s.ctx, s.profile.Source.LevelDBDir, s.profile.Source.BlobDir)
	if err != nil {
		t.Fatal("origin_open_failed")
	}
	res, readErr := outlookowa.Collect(s.ctx, o, s.account, true)
	closeErr := o.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("origin_read_or_close_failed")
	}
	if len(res.Messages) != 5 || !res.Doubtful || len(res.Losses) != 1 || res.Losses[0].Code != "outlook_owa_reader_blob_missing" {
		t.Fatal("fixed_decoder_loss_missing")
	}
	db := filepath.Join(owaScratch(t), "loss.db")
	rep, _, err := syncer.Run(s.ctx, syncer.Options{Root: filepath.Join(s.root, "no-teams"), DBPath: db, OutlookEnabled: true, OutlookRoot: s.root, OutlookMinReadInterval: -1})
	if err != nil {
		t.Fatal("loss_sync_failed")
	}
	src, found := mailSource(rep)
	if !found || src.Status != syncer.StatusOmissions || len(src.Omissions) != 1 || src.Omissions["outlook_owa_reader_blob_missing"] != 1 {
		t.Fatal("loss_sync_status_mismatch")
	}
	st := owaArchive(t, db)
	defer owaCloseArchive(t, st)
	if owaScalar(t, st, `select count(*) from mail_messages`) != 5 || owaScalar(t, st, `select count(*) from mail_absent`) != 0 {
		t.Fatal("loss_sync_unsafe_absence")
	}
}

func owaSortedFields(fields map[string]bool) []string {
	result := make([]string, 0, len(fields))
	for name := range fields {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
