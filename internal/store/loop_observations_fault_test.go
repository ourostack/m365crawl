package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/loopcache"
)

func partialLoopValue(t testing.TB) string {
	t.Helper()
	// Two channels at different sequences, one unrelated missing blob: partial with a loss.
	value := loopEnvelope(t, "synthetic-file", 10, 12, loopChannelSpec{name: "first", text: "partial alpha", sequence: 10},
		loopChannelSpec{name: "second", text: "partial bravo", sequence: 9})
	return strings.Replace(value, `"blobs":{},"trees":{"channels"`, `"blobs":{"orphan":"absent"},"trees":{"channels"`, 1)
}

// setupLoopArchive registers a healthy partial source, a failed source and an unregistered
// candidate set, so reads and discovery meet every row shape.
func setupLoopArchive(t *testing.T, s *Store) {
	t.Helper()
	healthy := insertLoopRecord(t, s, loopFixtureSource, "healthy", partialLoopValue(t), "tenant-a", "user-a")
	registerLoop(t, s, healthy, base)
	failed := insertLoopRecord(t, s, loopFixtureSource, "failed", nil, "", "")
	registerLoop(t, s, failed, base)
	updateLoopRecord(t, s, healthy, "removed_at=?", fmtTime(base.Add(time.Hour)))
	changed := insertLoopRecord(t, s, loopFixtureSource, "changed", loopText(t, "before"), "", "")
	registerLoop(t, s, changed, base)
	updateLoopRecord(t, s, changed, "value_json=?,content_hash='changed'", loopText(t, "after"))
	insertLoopRecord(t, s, loopFixtureSource, "unregistered", loopText(t, "pending"), "", "")
	insertLoopRecord(t, s, "bad\xff", "invalid", "{}", "", "")
}

func TestLoopReadFaultsRemainTerminal(t *testing.T) {
	for name, op := range map[string]func(context.Context, *Store) error{
		"discovery": func(ctx context.Context, s *Store) error { _, err := s.LoopDiscoveryCandidates(ctx); return err },
		"raw frame": func(ctx context.Context, s *Store) error { _, err := s.LoopRawFrame(ctx, 1); return err },
		"sources":   func(ctx context.Context, s *Store) error { _, err := s.LoopSources(ctx, 0); return err },
		"snapshot": func(ctx context.Context, s *Store) error {
			_, err := s.LoopSnapshot(ctx, LoopFilter{Query: "partial", Limit: 1})
			return err
		},
		"channel":  func(ctx context.Context, s *Store) error { _, _, err := s.LoopChannel(ctx, 1); return err },
		"overview": func(ctx context.Context, s *Store) error { _, err := s.LoopOverview(ctx); return err },
	} {
		t.Run(name, func(t *testing.T) {
			sweepReadFaults(t, setupLoopArchive, op)
		})
	}
}

func TestLoopWriteFaultsLeaveArchiveUnchanged(t *testing.T) {
	registration := LoopRegistration{Tuple: LoopTuple{Source: loopFixtureSource, Database: LoopDatabase, Store: LoopStore, KeyJSON: "new"}}
	sweepFaults(t, nil, func(ctx context.Context, s *Store) error {
		_, err := s.BeginLoopAttempt(ctx, registration, base)
		return err
	})
	var staged stagedLoop
	sweepFaults(t, func(t *testing.T, s *Store) {
		record := insertLoopRecord(t, s, loopFixtureSource, "commit", partialLoopValue(t), "tenant-a", "user-a")
		id := registerLoop(t, s, record, base)
		updateLoopRecord(t, s, record, "value_json=?", loopEnvelope(t, "synthetic-file", 3, 4,
			loopChannelSpec{name: "native", text: "staged", sequence: 3}))
		staged = stageLoop(t, s, id, base.Add(time.Minute))
		staged.result.Losses = []loopcache.Loss{{Code: "loop_cache_blob_missing", Count: 1}, {Code: "loop_cache_blob_missing", Count: 1}}
	}, func(ctx context.Context, s *Store) error { return staged.commit(s, base.Add(time.Minute)) })
	sweepFaults(t, func(t *testing.T, s *Store) {
		record := insertLoopRecord(t, s, loopFixtureSource, "failure", nil, "", "")
		staged.attempt = mustBeginLoop(t, s, *discover(t, s)[0].Registration, base)
		_ = record
	}, func(ctx context.Context, s *Store) error {
		return s.RecordLoopFailure(ctx, staged.attempt, "loop_cache_value_missing", base)
	})
}

func TestLoopRefusesInvalidInputs(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	record := insertLoopRecord(t, s, loopFixtureSource, "input", loopText(t, "india words"), "", "")
	other := insertLoopRecord(t, s, loopFixtureSource, "other", loopText(t, "other words"), "", "")
	id := registerLoop(t, s, record, base)
	otherID := registerLoop(t, s, other, base)
	tuple := LoopTuple{Source: loopFixtureSource, Database: LoopDatabase, Store: LoopStore, KeyJSON: "input"}
	for name, registration := range map[string]LoopRegistration{
		"database":     {Tuple: LoopTuple{Source: loopFixtureSource, Database: "other", Store: LoopStore, KeyJSON: "k"}},
		"negative":     {ID: -1, Tuple: tuple},
		"owner":        {Tuple: tuple, TenantID: strings.Repeat("t", LoopOwnerLimit+1)},
		"owner utf8":   {Tuple: tuple, UserID: "\xff"},
		"source utf8":  {Tuple: LoopTuple{Source: "\xff", Database: LoopDatabase, Store: LoopStore, KeyJSON: "k"}},
		"wrong id":     {ID: otherID, Tuple: tuple},
		"unknown id":   {ID: 999, Tuple: LoopTuple{Source: loopFixtureSource, Database: LoopDatabase, Store: LoopStore, KeyJSON: "nope"}},
		"not a source": {ID: 999, Tuple: tuple},
	} {
		if _, err := s.BeginLoopAttempt(ctx, registration, base); err == nil {
			t.Fatalf("%s registration accepted", name)
		}
	}
	if _, err := s.db.Exec(`insert into knowledge_locators(id,kind,platform,locator) values(500,'loop_cache','archive','["x","fluidDriverCache","driverStorage.V3","k"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`insert into loop_locator_metadata(locator_id,source,database,store,key_json) values(500,'y','fluidDriverCache','driverStorage.V3','k')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginLoopAttempt(ctx, LoopRegistration{Tuple: LoopTuple{Source: "x", Database: LoopDatabase, Store: LoopStore, KeyJSON: "k"}}, base); err == nil {
		t.Fatal("changed binding accepted")
	}
	staged := stageLoop(t, s, id, base.Add(time.Minute))
	bad := staged.frame
	bad.ValueDigest = "forged"
	if err := s.CommitLoopObservations(ctx, staged.attempt, bad, staged.result, base); err == nil {
		t.Fatal("forged digest accepted")
	}
	if err := s.CommitLoopObservations(ctx, staged.attempt, staged.frame, staged.result, time.Time{}); err == nil {
		t.Fatal("zero clock accepted")
	}
	lossy := staged.result
	lossy.Losses = []loopcache.Loss{{Code: "", Count: 1}}
	if err := s.CommitLoopObservations(ctx, staged.attempt, staged.frame, lossy, base); err == nil {
		t.Fatal("invalid loss accepted")
	}
	foreign := stageLoop(t, s, otherID, base.Add(time.Minute))
	if err := s.CommitLoopObservations(ctx, staged.attempt, foreign.frame, foreign.result, base); err == nil || errors.Is(err, ErrLoopInputChanged) {
		t.Fatalf("frame of another registration accepted: %v", err)
	}
	if err := s.RecordLoopFailure(ctx, staged.attempt, "made_up", base); err == nil {
		t.Fatal("unfixed reason accepted")
	}
	newer := stageLoop(t, s, id, base.Add(2*time.Minute))
	if err := staged.commit(s, base.Add(2*time.Minute)); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("superseded attempt published: %v", err)
	}
	if err := s.RecordLoopFailure(ctx, staged.attempt, "loop_cache_cancelled", base); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("stale failure not a sentinel: %v", err)
	}
	if err := s.RecordLoopFailure(ctx, newer.attempt, "loop_cache_cancelled", base.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if source := loopSource(t, s, id); source.Health.Status != "cancelled" {
		t.Fatalf("cancelled status: %+v", source.Health)
	}
	// A frontier newer than the attempt refuses publication inside admission.
	late := stageLoop(t, s, id, base.Add(4*time.Minute))
	if _, err := s.db.Exec(`update knowledge_sources set admitted_attempt=? where locator_id=?`, late.attempt.ID+1, id); err != nil {
		t.Fatal(err)
	}
	if err := late.commit(s, base.Add(4*time.Minute)); !errors.Is(err, ErrStaleKnowledgeAttempt) {
		t.Fatalf("newer frontier overwritten: %v", err)
	}
	if _, err := s.LoopSnapshot(ctx, LoopFilter{Limit: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
	var usage *errs.Coded
	if _, err := s.LoopSnapshot(ctx, LoopFilter{Query: "***"}); !errors.As(err, &usage) {
		t.Fatalf("unsearchable words: %v", err)
	}
	if _, err := s.LoopSnapshot(ctx, LoopFilter{SourceID: 999}); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown source: %v", err)
	}
	if _, err := s.LoopRawFrame(ctx, 0); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("zero registration: %v", err)
	}
}

func TestLoopRegisteredRecordStates(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	record := insertLoopRecord(t, s, loopFixtureSource, "states", loopText(t, "juliet"), "", "")
	id := registerLoop(t, s, record, base)
	updateLoopRecord(t, s, record, "tenant_id=?", strings.Repeat("t", LoopOwnerLimit+1))
	acquisition, err := s.LoopRawFrame(ctx, id)
	if err != nil || acquisition.Registration == nil || acquisition.Registration.ID != id ||
		acquisition.RefusalCode != "loop_cache_identity_unqualified" || acquisition.Frame != nil {
		t.Fatalf("registered source with unqualified owner: %+v %v", acquisition, err)
	}
	if source := loopSource(t, s, id); source.Registration.TenantID != "" || source.SnapshotMatch != "unavailable" {
		t.Fatalf("unqualified owner echoed: %+v", source)
	}
	updateLoopRecord(t, s, record, "tenant_id='',updated_at='not a clock'")
	if acquisition, _ := s.LoopRawFrame(ctx, id); acquisition.RefusalCode != "loop_cache_value_unqualified" {
		t.Fatalf("unparsable clock: %+v", acquisition)
	}
	if source := loopSource(t, s, id); source.RawUpdatedAt != nil || !source.RawPresent {
		t.Fatalf("unparsable clock source: %+v", source)
	}
	updateLoopRecord(t, s, record, "updated_at=?,removed_at='bad'", fmtTime(base))
	if acquisition, _ := s.LoopRawFrame(ctx, id); acquisition.RefusalCode != "loop_cache_value_unqualified" {
		t.Fatalf("unparsable removal: %+v", acquisition)
	}
	if _, err := s.db.Exec(`delete from records where rowid=?`, record); err != nil {
		t.Fatal(err)
	}
	acquisition, err = s.LoopRawFrame(ctx, id)
	if err != nil || acquisition.RefusalCode != "loop_cache_record_missing" || acquisition.Registration.ID != id {
		t.Fatalf("vanished record: %+v %v", acquisition, err)
	}
	if source := loopSource(t, s, id); source.RawPresent || source.SnapshotMatch != "unavailable" || source.Reason != "loop_cache_record_missing" {
		t.Fatalf("vanished record source: %+v", source)
	}
}

func TestLoopReadsBeforeSchema13(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	for _, table := range []string{"loop_fts", "loop_channels"} {
		if _, err := s.db.Exec(`drop table ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if sources, err := s.LoopSources(ctx, 0); err != nil || len(sources) != 0 {
		t.Fatalf("old schema sources: %v %v", sources, err)
	}
	if snapshot, err := s.LoopSnapshot(ctx, LoopFilter{}); err != nil || len(snapshot.Rows) != 0 || len(snapshot.Sources) != 0 {
		t.Fatalf("old schema snapshot: %+v %v", snapshot, err)
	}
	if _, err := s.LoopSnapshot(ctx, LoopFilter{SourceID: 1}); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("old schema selected source: %v", err)
	}
	if _, err := s.LoopRawFrame(ctx, 1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("old schema raw frame: %v", err)
	}
	if _, _, err := s.LoopChannel(ctx, 1); !errors.Is(err, ErrLoopChannelNotFound) {
		t.Fatalf("old schema channel: %v", err)
	}
}

func TestLoopStoredClockCorruptionIsAnError(t *testing.T) {
	s := newStore(t)
	record := insertLoopRecord(t, s, loopFixtureSource, "clock", loopText(t, "kilo"), "", "")
	registerLoop(t, s, record, base)
	if _, err := s.db.Exec(`update loop_snapshots set projected_at='bad'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoopSnapshot(context.Background(), LoopFilter{}); err == nil {
		t.Fatal("corrupt stored clock became a row")
	}
}

func TestLoopReadsOnClosedArchiveFail(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoopDiscoveryCandidates(ctx); err == nil {
		t.Fatal("closed discovery succeeded")
	}
	if _, err := s.LoopRawFrame(ctx, 1); err == nil || errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("closed raw frame: %v", err)
	}
	if _, err := s.LoopSources(ctx, 0); err == nil {
		t.Fatal("closed sources succeeded")
	}
	if _, err := s.LoopSnapshot(ctx, LoopFilter{}); err == nil {
		t.Fatal("closed snapshot succeeded")
	}
	if _, _, err := s.LoopChannel(ctx, 1); err == nil || errors.Is(err, ErrLoopChannelNotFound) {
		t.Fatalf("closed channel: %v", err)
	}
}

// The overview counts come from registrations, attempts and channel rows only: no value is loaded.
func TestLoopOverviewCountsWithoutValues(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if overview, err := s.LoopOverview(ctx); err != nil || overview != (LoopOverview{}) {
		t.Fatalf("empty archive: %+v %v", overview, err)
	}
	setupLoopArchive(t, s)
	metadata := insertLoopRecord(t, s, loopFixtureSource, "metadata", loopEnvelope(t, "synthetic-file", 1, 1), "", "")
	metadataID := registerLoop(t, s, metadata, base.Add(time.Hour))
	reads := countValueReads(t)
	overview, err := s.LoopOverview(ctx)
	if err != nil || overview.ArchivedRecords != 6 || overview.Registered != 4 || overview.Failed != 1 || overview.MetadataOnly != 1 ||
		overview.Channels != 3 || overview.LastProjectedAt == nil || !overview.LastProjectedAt.Equal(base.Add(time.Hour)) || *reads != 0 {
		t.Fatalf("overview: %+v %v reads=%d", overview, err, *reads)
	}
	// A source projected again under a new reader version is still one registered source.
	previous := LoopQualification
	LoopQualification = "loop-cache-v2/mapper-v1"
	t.Cleanup(func() { LoopQualification = previous })
	if err := stageLoop(t, s, metadataID, base.Add(2*time.Hour)).commit(s, base.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if again, err := s.LoopOverview(ctx); err != nil || again.Registered != 4 || again.MetadataOnly != 1 {
		t.Fatalf("overview after a reader change: %+v %v", again, err)
	}
	for _, table := range []string{"loop_fts", "loop_channels"} {
		if _, err := s.db.Exec(`drop table ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if old, err := s.LoopOverview(ctx); err != nil || old.ArchivedRecords != 6 || old.Registered != 0 || old.Channels != 0 {
		t.Fatalf("archive before the Loop tables: %+v %v", old, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoopOverview(ctx); err == nil {
		t.Fatal("closed overview succeeded")
	}
}
