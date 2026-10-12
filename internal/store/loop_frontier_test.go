package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/loopcache"
)

// stagedLoop is an acquired and parsed frame whose attempt has begun but not published.
type stagedLoop struct {
	attempt KnowledgeAttempt
	frame   LoopRawFrame
	result  loopcache.Result
}

func stageLoop(t testing.TB, s *Store, registrationID int64, at time.Time) stagedLoop {
	t.Helper()
	ctx := context.Background()
	acquisition, err := s.LoopRawFrame(ctx, registrationID)
	if err != nil || acquisition.Frame == nil {
		t.Fatalf("stage acquisition: %+v %v", acquisition, err)
	}
	return stagedLoop{attempt: mustBeginLoop(t, s, *acquisition.Registration, at), frame: *acquisition.Frame,
		result: mustRead(t, acquisition.Frame.Value)}
}

func (staged stagedLoop) commit(s *Store, at time.Time) error {
	return s.CommitLoopObservations(context.Background(), staged.attempt, staged.frame, staged.result, at)
}

// registerLoop registers one archived record through discovery and admits it, returning the
// registration ID.
func registerLoop(t testing.TB, s *Store, recordID int64, at time.Time) int64 {
	t.Helper()
	for _, candidate := range discover(t, s) {
		if candidate.RecordID != recordID {
			continue
		}
		attempt := mustBeginLoop(t, s, *candidate.Registration, at)
		if candidate.Frame == nil {
			if err := s.RecordLoopFailure(context.Background(), attempt, candidate.RefusalCode, at); err != nil {
				t.Fatal(err)
			}
			return attempt.LocatorID
		}
		if err := s.CommitLoopObservations(context.Background(), attempt, *candidate.Frame, mustRead(t, candidate.Frame.Value), at); err != nil {
			t.Fatal(err)
		}
		return attempt.LocatorID
	}
	t.Fatalf("record %d not discovered", recordID)
	return 0
}

func loopSource(t testing.TB, s *Store, id int64) LoopSource {
	t.Helper()
	snapshot, err := s.LoopSnapshot(context.Background(), LoopFilter{SourceID: id})
	if err != nil || len(snapshot.Sources) != 1 {
		t.Fatalf("source %d: %+v %v", id, snapshot.Sources, err)
	}
	return snapshot.Sources[0]
}

func loopRows(t testing.TB, s *Store, filter LoopFilter) []LoopChannelRow {
	t.Helper()
	snapshot, err := s.LoopSnapshot(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Rows
}

func TestLoopRawFrameRaceRejectsPublication(t *testing.T) {
	for _, change := range []struct {
		name, assignment string
		args             []any
	}{
		{"value", "value_json=?", []any{"Y"}},
		{"owner tenant", "tenant_id=?", []any{"tenant-b"}},
		{"owner user", "user_id=?", []any{"user-b"}},
		{"clock", "updated_at=?", []any{"2026-09-02T00:00:00.000Z"}},
		{"removal", "removed_at=?", []any{"2026-09-02T00:00:00.000Z"}},
		{"raw digest", "raw_digest=?", []any{[]byte{1, 2}}},
		{"redaction", "value_redacted=1", nil},
		{"replaced record", "rowid=rowid+1000", nil},
	} {
		t.Run(change.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "private", "archive.db")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			record := insertLoopRecord(t, s, loopFixtureSource, "race", loopText(t, "admitted words"), "tenant-a", "user-a")
			id := registerLoop(t, s, record, base)
			before := loopSource(t, s, id)
			updateLoopRecord(t, s, record, "value_json=?", loopText(t, "staged words"))
			staged := stageLoop(t, s, id, base.Add(time.Minute))
			peer, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = peer.Close() }()
			args := change.args
			if change.name == "value" {
				args = []any{loopText(t, "peer words")}
			}
			updateLoopRecord(t, peer, record, change.assignment, args...)
			if err := staged.commit(s, base.Add(2*time.Minute)); !errors.Is(err, ErrLoopInputChanged) {
				t.Fatalf("changed %s published: %v", change.name, err)
			}
			after := loopSource(t, s, id)
			if after.LastSnapshotID != before.LastSnapshotID || after.Health.AdmittedID != before.Health.AdmittedID ||
				loopTableCount(t, s, "loop_snapshots") != 1 || loopTableCount(t, s, "loop_channels") != 1 ||
				loopTableCount(t, s, "loop_fts") != 1 || loopTableCount(t, s, "knowledge_sources") != 1 {
				t.Fatalf("refused publication leaked rows or moved the admitted pointer: %+v", after)
			}
			if rows := loopRows(t, s, LoopFilter{Query: "staged"}); len(rows) != 0 {
				t.Fatal("staged text became searchable")
			}
			if err := s.RecordLoopFailure(ctx, staged.attempt, "loop_cache_input_changed", base.Add(3*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if failed := loopSource(t, s, id); failed.Health.Status != "failed" || failed.Health.Reason != "loop_cache_input_changed" ||
				failed.LastSnapshotID != before.LastSnapshotID {
				t.Fatalf("changed input not recorded on the current attempt: %+v", failed.Health)
			}
		})
	}
}

func TestLoopOwnerChangeRetriesSameRegistration(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	record := insertLoopRecord(t, s, loopFixtureSource, "owned", loopText(t, "owned words"), "tenant-a", "user-a")
	id := registerLoop(t, s, record, base)
	first := loopSource(t, s, id)
	staged := stageLoop(t, s, id, base.Add(time.Minute))
	updateLoopRecord(t, s, record, "tenant_id='tenant-b',user_id='user-b'")
	if err := staged.commit(s, base.Add(time.Minute)); !errors.Is(err, ErrLoopInputChanged) {
		t.Fatalf("owner change published: %v", err)
	}
	if err := s.RecordLoopFailure(ctx, staged.attempt, "loop_cache_input_changed", base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	retry := stageLoop(t, s, id, base.Add(2*time.Minute))
	if retry.attempt.LocatorID != id || retry.frame.TenantID != "tenant-b" {
		t.Fatalf("owner change allocated another registration: %+v", retry.attempt)
	}
	if err := retry.commit(s, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	source := loopSource(t, s, id)
	if source.LastSnapshotID != first.LastSnapshotID || source.LastSnapshotTenantID != "tenant-a" ||
		source.LastSnapshotUserID != "user-a" || source.Registration.TenantID != "tenant-b" || source.Registration.UserID != "user-b" ||
		source.SnapshotMatch != "matches" || source.Health.Status != "complete" {
		t.Fatalf("owner evidence not separated: %+v", source)
	}
	var owners string
	if err := s.db.QueryRow(`select group_concat(tenant_id,',') from (select tenant_id from loop_attempt_evidence order by attempt_id)`).Scan(&owners); err != nil {
		t.Fatal(err)
	}
	if owners != "tenant-a,tenant-a,tenant-b" {
		t.Fatalf("attempt owner evidence not preserved: %s", owners)
	}
	if rows := loopRows(t, s, LoopFilter{}); len(rows) != 1 || rows[0].SnapshotTenantID != "tenant-a" || rows[0].Retained {
		t.Fatalf("reused snapshot rows changed: %+v", rows)
	}
}

func TestLoopImmutableTransitions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	x, y := loopText(t, "alpha original"), loopText(t, "bravo replacement")
	record := insertLoopRecord(t, s, loopFixtureSource, "transitions", x, "", "")
	id := registerLoop(t, s, record, base)
	xRows := loopRows(t, s, LoopFilter{Query: "alpha"})
	if len(xRows) != 1 || xRows[0].Retained || !xRows[0].MatchesRetainedValue || !xRows[0].RawPresent {
		t.Fatalf("admitted X: %+v", xRows)
	}
	xSnapshot, xClock := xRows[0].SnapshotID, xRows[0].ProjectedAt
	commitAt := func(value any, at time.Time) error {
		updateLoopRecord(t, s, record, "value_json=?,updated_at=?", value, fmtTime(at))
		return stageLoop(t, s, id, at).commit(s, at)
	}
	// X -> valid Y: Y current, X retained and still searchable.
	if err := commitAt(y, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rows := loopRows(t, s, LoopFilter{Query: "alpha"}); len(rows) != 1 || !rows[0].Retained || rows[0].MatchesRetainedValue {
		t.Fatalf("X not retained after Y: %+v", rows)
	}
	if source := loopSource(t, s, id); source.SnapshotMatch != "matches" || source.LastSnapshotID == xSnapshot {
		t.Fatalf("Y not current: %+v", source)
	}
	// Y -> fatal: Y preserved, latest failed, raw no longer matches.
	updateLoopRecord(t, s, record, "value_json='{}'")
	acquisition, err := s.LoopRawFrame(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	attempt := mustBeginLoop(t, s, *acquisition.Registration, base.Add(2*time.Hour))
	if _, err := loopcache.ReadSnapshot(ctx, acquisition.Frame.Value); err == nil {
		t.Fatal("fatal fixture parsed")
	}
	if err := s.RecordLoopFailure(ctx, attempt, "loop_cache_snapshot_unsupported", base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if source := loopSource(t, s, id); source.SnapshotMatch != "differs" || source.Health.Status != "failed" ||
		len(loopRows(t, s, LoopFilter{Query: "bravo"})) != 1 {
		t.Fatalf("fatal Y erased last good: %+v", source)
	}
	// NULL value: unavailable, prior rows remain.
	updateLoopRecord(t, s, record, "value_json=null")
	if source := loopSource(t, s, id); source.SnapshotMatch != "unavailable" || source.RawValuePresent || !source.RawPresent ||
		source.Reason != "loop_cache_value_missing" {
		t.Fatalf("missing value: %+v", source)
	}
	if rows := loopRows(t, s, LoopFilter{Query: "bravo"}); rows[0].MatchesRetainedValue || rows[0].Retained {
		t.Fatalf("missing value still matched: %+v", rows)
	}
	// Metadata-only Y: admitted with zero channels; X and Y remain searchable as retained.
	if err := commitAt(loopEnvelope(t, "synthetic-file", 11, 11), base.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if source := loopSource(t, s, id); source.Health.Coverage != "metadata_only" || source.SnapshotMatch != "matches" {
		t.Fatalf("metadata-only: %+v", source)
	}
	if rows := loopRows(t, s, LoopFilter{}); len(rows) != 2 || !rows[0].Retained || !rows[1].Retained {
		t.Fatalf("prior channels not retained: %+v", rows)
	}
	// A -> B -> A: X reuses its snapshot and original clocks.
	if err := commitAt(x, base.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows := loopRows(t, s, LoopFilter{Query: "alpha"})
	if len(rows) != 1 || rows[0].SnapshotID != xSnapshot || !rows[0].ProjectedAt.Equal(xClock) ||
		!rows[0].RecordUpdatedAtAtAdmission.Equal(base) || rows[0].Retained || !rows[0].MatchesRetainedValue {
		t.Fatalf("A->B->A did not reuse X: %+v", rows)
	}
	// Unchanged X removal and reappearance: presence and raw clock change, the match does not.
	updateLoopRecord(t, s, record, "removed_at=?,raw_digest=null", fmtTime(base.Add(5*time.Hour)))
	source := loopSource(t, s, id)
	if source.SnapshotMatch != "matches" || source.RawPresent || source.RawRemovedAt == nil {
		t.Fatalf("removed byte-identical X: %+v", source)
	}
	if rows := loopRows(t, s, LoopFilter{Query: "alpha"}); rows[0].RawPresent || !rows[0].MatchesRetainedValue {
		t.Fatalf("removed row state: %+v", rows)
	}
	updateLoopRecord(t, s, record, "removed_at=null,updated_at=?", fmtTime(base.Add(6*time.Hour)))
	if err := stageLoop(t, s, id, base.Add(6*time.Hour)).commit(s, base.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows = loopRows(t, s, LoopFilter{Query: "alpha"})
	if !rows[0].RawPresent || rows[0].RawUpdatedAt == nil || !rows[0].RawUpdatedAt.Equal(base.Add(6*time.Hour)) ||
		!rows[0].RecordUpdatedAtAtAdmission.Equal(base) || !rows[0].ProjectedAt.Equal(xClock) {
		t.Fatalf("reappearance rewrote immutable clocks: %+v", rows[0])
	}
	// Qualification change: same bytes differ until projected again under the new qualification.
	previous := LoopQualification
	LoopQualification = "loop-cache-v2/mapper-v1"
	t.Cleanup(func() { LoopQualification = previous })
	if source := loopSource(t, s, id); source.SnapshotMatch != "differs" {
		t.Fatalf("qualification change still matched: %+v", source)
	}
	if err := stageLoop(t, s, id, base.Add(7*time.Hour)).commit(s, base.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if source := loopSource(t, s, id); source.SnapshotMatch != "matches" || source.SnapshotQualification != LoopQualification ||
		source.LastSnapshotID == xSnapshot {
		t.Fatalf("qualification backfill: %+v", source)
	}
	if rows := loopRows(t, s, LoopFilter{Query: "alpha"}); len(rows) != 2 {
		t.Fatalf("prior qualification rows not retained: %d", len(rows))
	}
}

type loopPublicationReader struct {
	knowledgeReader
	publish func()
}

func (r *loopPublicationReader) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := r.knowledgeReader.QueryContext(ctx, query, args...)
	if err == nil && r.publish != nil && strings.Contains(query, "from loop_channels c") {
		publish := r.publish
		r.publish = nil
		publish()
	}
	return rows, err
}

func TestLoopCoherentZeroHitAndDetail(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	admitted := insertLoopRecord(t, s, loopFixtureSource, "admitted", loopText(t, "xray words"), "", "")
	id := registerLoop(t, s, admitted, base)
	peer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	published := false
	reader := &loopPublicationReader{knowledgeReader: tx, publish: func() {
		updateLoopRecord(t, peer, admitted, "value_json=?", loopText(t, "yankee words"))
		if err := stageLoop(t, peer, id, base.Add(time.Hour)).commit(peer, base.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		published = true
	}}
	snapshot, err := readLoopSnapshot(ctx, reader, LoopFilter{Query: "xray"}, 0)
	if err != nil || !published || len(snapshot.Rows) != 1 || snapshot.Rows[0].Retained || !snapshot.Rows[0].MatchesRetainedValue ||
		len(snapshot.Sources) != 1 || snapshot.Sources[0].SnapshotMatch != "matches" || snapshot.Sources[0].Health.Status != "complete" ||
		snapshot.Sources[0].LastSnapshotID != snapshot.Rows[0].SnapshotID {
		t.Fatalf("publication crossed the read snapshot: %+v %v", snapshot, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// After Y, zero hits still carry explicit source state for every case.
	missing := insertLoopRecord(t, s, loopFixtureSource, "missing", loopText(t, "kilo"), "", "")
	missingID := registerLoop(t, s, missing, base)
	updateLoopRecord(t, s, missing, "value_json=null")
	oversized := insertLoopRecord(t, s, loopFixtureSource, "oversized", loopText(t, "lima"), "", "")
	oversizedID := registerLoop(t, s, oversized, base)
	updateLoopRecord(t, s, oversized, "value_json=?", strings.Repeat(" ", LoopValueLimit+1))
	removed := insertLoopRecord(t, s, loopFixtureSource, "removed", loopText(t, "mike"), "", "")
	removedID := registerLoop(t, s, removed, base)
	updateLoopRecord(t, s, removed, "removed_at=?", fmtTime(base))
	never := insertLoopRecord(t, s, loopFixtureSource, "never", nil, "", "")
	neverID := registerLoop(t, s, never, base)
	changed := insertLoopRecord(t, s, loopFixtureSource, "changed", loopText(t, "november"), "", "")
	changedID := registerLoop(t, s, changed, base)
	updateLoopRecord(t, s, changed, "value_json=?", loopText(t, "oscar"))
	zero, err := s.LoopSnapshot(ctx, LoopFilter{Query: "nothingmatches"})
	if err != nil || len(zero.Rows) != 0 || len(zero.Sources) != 6 {
		t.Fatalf("zero hits: %+v %v", zero, err)
	}
	want := map[int64]string{id: "matches", missingID: "unavailable", oversizedID: "unavailable", removedID: "matches",
		neverID: "no_admission", changedID: "differs"}
	for _, source := range zero.Sources {
		if source.SnapshotMatch != want[source.Registration.ID] {
			t.Fatalf("source %d match %s, want %s", source.Registration.ID, source.SnapshotMatch, want[source.Registration.ID])
		}
		if source.Registration.ID == removedID && source.RawPresent {
			t.Fatal("removed source reported present")
		}
	}
	row, source, err := s.LoopChannel(ctx, snapshot.Rows[0].ID)
	if err != nil || row.ID != snapshot.Rows[0].ID || !row.Retained || row.MatchesRetainedValue || source.Registration.ID != id ||
		source.SnapshotMatch != "matches" || row.Channel.Text != "xray words" {
		t.Fatalf("detail did not resolve the immutable retained row: %+v %+v %v", row, source, err)
	}
	if _, _, err := s.LoopChannel(ctx, 999); !errors.Is(err, ErrLoopChannelNotFound) {
		t.Fatalf("unknown channel: %v", err)
	}
	if _, _, err := s.LoopChannel(ctx, 0); !errors.Is(err, ErrLoopChannelNotFound) {
		t.Fatalf("zero channel: %v", err)
	}
}
