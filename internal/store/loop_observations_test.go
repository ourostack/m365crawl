package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/loopcache"
)

// loopChannelSpec is one synthetic channel of a synthetic Loop cache snapshot.
type loopChannelSpec struct {
	name, text string
	sequence   int64
}

// loopEnvelope builds a synthetic cached Loop snapshot that loopcache.ReadSnapshot accepts: one
// legacy single-chunk channel per spec under channels/<name>.
func loopEnvelope(t testing.TB, fileID string, sequence, latest int64, channels ...loopChannelSpec) string {
	t.Helper()
	entries := []any{}
	trees := map[string]any{}
	for i, channel := range channels {
		length := int64(len([]rune(channel.text)))
		header := map[string]any{
			"chunkStartSegmentIndex": 0, "chunkSegmentCount": 1, "chunkLengthChars": length,
			"totalLengthChars": length, "totalSegmentCount": 1, "chunkSequenceNumber": channel.sequence,
			"segmentTexts": []any{map[string]any{"text": channel.text}},
			"headerMetadata": map[string]any{"sequenceNumber": channel.sequence, "totalLength": length,
				"totalSegmentCount": 1, "orderedChunkMetadata": []any{map[string]any{"id": "header"}}},
		}
		raw, err := json.Marshal(header)
		if err != nil {
			t.Fatal(err)
		}
		id := "h" + string(rune('a'+i))
		entries = append(entries, []any{id, map[string]any{"$bytes": base64.StdEncoding.EncodeToString(raw)}})
		trees[channel.name] = map[string]any{"blobs": map[string]any{"header": id}, "trees": map[string]any{}}
	}
	value := map[string]any{"sequenceNumber": sequence, "latestSequenceNumber": latest, "ops": []any{},
		"blobContents": map[string]any{"$map": entries},
		"snapshotTree": map[string]any{"blobs": map[string]any{}, "trees": map[string]any{
			"channels": map[string]any{"blobs": map[string]any{}, "trees": trees}}}}
	raw, err := json.Marshal(map[string]any{"fileId": fileID, "cachedObject": map[string]any{"value": value}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func loopText(t testing.TB, text string) string {
	t.Helper()
	return loopEnvelope(t, "synthetic-file", 10, 10, loopChannelSpec{name: "native", text: text, sequence: 10})
}

const loopFixtureSource = "https://synthetic.invalid"

// insertLoopRecord archives one generic record under the exact Loop tuple database/store.
func insertLoopRecord(t testing.TB, s *Store, source, key string, value any, tenant, user string) int64 {
	t.Helper()
	result, err := s.db.Exec(`insert into records(source,tenant_id,user_id,database,store,key_json,value_json,
content_hash,first_seen_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, source, tenant, user, LoopDatabase, LoopStore,
		key, value, "synthetic", fmtTime(base), fmtTime(base))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func updateLoopRecord(t testing.TB, s *Store, recordID int64, assignment string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(`update records set `+assignment+` where rowid=?`, append(args, recordID)...); err != nil { //nolint:gosec // G202: fixed assignments from this test file
		t.Fatal(err)
	}
}

func discover(t testing.TB, s *Store) []LoopAcquisition {
	t.Helper()
	discovery, err := s.LoopDiscoveryFrames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return discovery.Candidates
}

func countValueReads(t *testing.T) *int {
	t.Helper()
	reads := 0
	previous := loopReadValue
	loopReadValue = func(ctx context.Context, reader knowledgeReader, recordID int64) ([]byte, error) {
		reads++
		return previous(ctx, reader, recordID)
	}
	t.Cleanup(func() { loopReadValue = previous })
	return &reads
}

func loopTableCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	return rowCount(t, s, `select count(*) from `+table)
}

func TestLoopRawBoundsAndExactTuple(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	valid := map[string]int64{
		"source-at-bound": insertLoopRecord(t, s, strings.Repeat("s", LoopSourceLimit), "1", "", "", ""),
		"key-one":         insertLoopRecord(t, s, loopFixtureSource, "1", "", "", ""),
		"key-one-decimal": insertLoopRecord(t, s, loopFixtureSource, "1.0", nil, strings.Repeat("t", LoopOwnerLimit), strings.Repeat("u", LoopOwnerLimit)),
		"key-at-bound":    insertLoopRecord(t, s, loopFixtureSource, strings.Repeat("k", LoopKeyLimit), strings.Repeat(" ", LoopValueLimit), "", ""),
		"value-over":      insertLoopRecord(t, s, loopFixtureSource, "over", strings.Repeat(" ", LoopValueLimit+1), "", ""),
		"value-blob":      insertLoopRecord(t, s, loopFixtureSource, "blob", []byte("{}"), "", ""),
	}
	invalid := []int64{
		insertLoopRecord(t, s, strings.Repeat("s", LoopSourceLimit+1), "1", "{}", "", ""),
		insertLoopRecord(t, s, loopFixtureSource, strings.Repeat("k", LoopKeyLimit+1), "{}", "", ""),
		insertLoopRecord(t, s, loopFixtureSource, "owner", "{}", strings.Repeat("t", LoopOwnerLimit+1), ""),
		insertLoopRecord(t, s, loopFixtureSource, "user", "{}", "", strings.Repeat("u", LoopOwnerLimit+1)),
		insertLoopRecord(t, s, loopFixtureSource, "bad\xff", "{}", "", ""),
	}
	byID := map[int64]LoopAcquisition{}
	for _, candidate := range discover(t, s) {
		byID[candidate.RecordID] = candidate
	}
	if len(byID) != len(valid)+len(invalid) {
		t.Fatalf("structural candidates dropped from discovery: %d", len(byID))
	}
	for _, id := range invalid {
		candidate := byID[id]
		if candidate.Registration != nil || candidate.Frame != nil || candidate.RefusalCode != "loop_cache_identity_unqualified" {
			t.Fatalf("invalid identity %d was qualified: %+v", id, candidate)
		}
	}
	empty := byID[valid["key-one"]]
	if empty.Frame == nil || !empty.Frame.ValuePresent || len(empty.Frame.Value) != 0 || empty.Frame.ValueDigest == "" ||
		empty.Registration == nil || empty.Registration.Tuple.KeyJSON != "1" || empty.Registration.ID != 0 {
		t.Fatalf("empty value was not a distinct present frame: %+v", empty)
	}
	null := byID[valid["key-one-decimal"]]
	if null.Frame != nil || null.RefusalCode != "loop_cache_value_missing" || null.Registration == nil ||
		null.Registration.Tuple.KeyJSON != "1.0" || len(null.Registration.TenantID) != LoopOwnerLimit {
		t.Fatalf("NULL value was not a refused registration: %+v", null)
	}
	if bound := byID[valid["key-at-bound"]]; bound.Frame == nil || len(bound.Frame.Value) != LoopValueLimit {
		t.Fatalf("inclusive value/key bounds refused: %+v", bound.RefusalCode)
	}
	if over := byID[valid["value-over"]]; over.Frame != nil || over.RefusalCode != "loop_cache_value_too_large" || over.Registration == nil {
		t.Fatalf("oversized value acquired: %+v", over.RefusalCode)
	}
	if blob := byID[valid["value-blob"]]; blob.Frame != nil || blob.RefusalCode != "loop_cache_value_unqualified" {
		t.Fatalf("non-text value acquired: %+v", blob.RefusalCode)
	}
	if source := byID[valid["source-at-bound"]]; source.Registration == nil || len(source.Registration.Tuple.Source) != LoopSourceLimit {
		t.Fatalf("inclusive source bound refused: %+v", source)
	}
	one := mustBeginLoop(t, s, *empty.Registration, base)
	decimal := mustBeginLoop(t, s, *null.Registration, base)
	if one.LocatorID <= 0 || decimal.LocatorID <= 0 || one.LocatorID == decimal.LocatorID {
		t.Fatalf("exact key strings 1 and 1.0 were normalized: %d %d", one.LocatorID, decimal.LocatorID)
	}
	limit := loopLocatorLimit
	loopLocatorLimit = 16
	t.Cleanup(func() { loopLocatorLimit = limit })
	if _, err := s.BeginLoopAttempt(ctx, *empty.Registration, base); err == nil {
		t.Fatal("encoded locator over its bound was registered")
	}
	for _, candidate := range discover(t, s) {
		if candidate.RefusalCode != "loop_cache_identity_unqualified" || candidate.Registration != nil {
			t.Fatalf("over-bound encoded locator qualified: %+v", candidate)
		}
	}
}

func mustBeginLoop(t testing.TB, s *Store, registration LoopRegistration, at time.Time) KnowledgeAttempt {
	t.Helper()
	attempt, err := s.BeginLoopAttempt(context.Background(), registration, at)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestLoopDiscoveryOverflowWritesNothing(t *testing.T) {
	s := newStore(t)
	reads := countValueReads(t)
	for i := range LoopDiscoveryBatch {
		insertLoopRecord(t, s, loopFixtureSource, "key-"+strings.Repeat("x", i), nil, "", "")
	}
	insertLoopRecord(t, s, loopFixtureSource, "elsewhere", "{}", "", "")
	if _, err := s.db.Exec(`update records set database='otherDatabase' where key_json='elsewhere'`); err != nil {
		t.Fatal(err)
	}
	if candidates := discover(t, s); len(candidates) != LoopDiscoveryBatch {
		t.Fatalf("batch at the limit lost candidates: %d", len(candidates))
	}
	insertLoopRecord(t, s, loopFixtureSource, "overflow", "{}", "", "")
	*reads = 0
	if _, err := s.LoopDiscoveryFrames(context.Background()); !errors.Is(err, ErrLoopDiscoveryLimit) {
		t.Fatalf("129 structural rows did not overflow: %v", err)
	}
	if *reads != 0 || loopTableCount(t, s, "loop_locator_metadata") != 0 || loopTableCount(t, s, "knowledge_locators") != 0 {
		t.Fatalf("overflow loaded values or registered sources: reads=%d", *reads)
	}
}

func TestLoopRegistrationBeforeAdmission(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	record := insertLoopRecord(t, s, loopFixtureSource, `"metadata"`, nil, "tenant-a", "user-a")
	candidates := discover(t, s)
	if len(candidates) != 1 || candidates[0].Registration == nil || candidates[0].RefusalCode != "loop_cache_value_missing" {
		t.Fatalf("missing value was not a registrable refusal: %+v", candidates)
	}
	attempt := mustBeginLoop(t, s, *candidates[0].Registration, base)
	if err := s.RecordLoopFailure(ctx, attempt, candidates[0].RefusalCode, base); err != nil {
		t.Fatal(err)
	}
	sources, err := s.LoopSources(ctx)
	if err != nil || len(sources) != 1 || sources[0].Registration.ID != attempt.LocatorID || attempt.LocatorID <= 0 ||
		sources[0].Health.Status != "failed" || sources[0].Health.Reason != "loop_cache_value_missing" ||
		sources[0].SnapshotMatch != "no_admission" || sources[0].LastSnapshotID != 0 || sources[0].Health.ID != 0 {
		t.Fatalf("failed initial source lacks a stable selected registration: %+v %v", sources, err)
	}
	if len(discover(t, s)) != 0 {
		t.Fatal("registered tuple rediscovered")
	}
	updateLoopRecord(t, s, record, "value_json=?", loopEnvelope(t, "file", 1, 1))
	acquisition, err := s.LoopRawFrame(ctx, attempt.LocatorID)
	if err != nil || acquisition.Frame == nil || acquisition.Registration == nil || acquisition.Registration.ID != attempt.LocatorID {
		t.Fatalf("selected retry by registration ID failed: %+v %v", acquisition, err)
	}
	retry := mustBeginLoop(t, s, *acquisition.Registration, base.Add(time.Minute))
	if retry.LocatorID != attempt.LocatorID {
		t.Fatalf("retry allocated another registration: %d", retry.LocatorID)
	}
	result := mustRead(t, acquisition.Frame.Value)
	if err := s.CommitLoopObservations(ctx, retry, *acquisition.Frame, result, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	sources, err = s.LoopSources(ctx)
	if err != nil || sources[0].Registration.ID != attempt.LocatorID || sources[0].LastSnapshotID <= 0 ||
		sources[0].Health.Coverage != "metadata_only" || sources[0].SnapshotMatch != "matches" || sources[0].Health.ID <= 0 {
		t.Fatalf("metadata-only admission lost registration identity: %+v %v", sources, err)
	}
	if _, err := s.LoopRawFrame(ctx, 999); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown registration accepted: %v", err)
	}
}

func mustRead(t testing.TB, value []byte) loopcache.Result {
	t.Helper()
	result, err := loopcache.ReadSnapshot(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLoopAcquisitionRefusalsAreIndependent(t *testing.T) {
	s := newStore(t)
	reads := countValueReads(t)
	a := insertLoopRecord(t, s, "bad\xff", "a", "{}", "", "")
	b := insertLoopRecord(t, s, loopFixtureSource, "b", strings.Repeat(" ", LoopValueLimit+1), "", "")
	c := insertLoopRecord(t, s, loopFixtureSource, "c", loopText(t, "healthy"), "", "")
	candidates := discover(t, s)
	if len(candidates) != 3 || *reads != 1 {
		t.Fatalf("candidates/value reads: %d/%d", len(candidates), *reads)
	}
	if got := candidates[0]; got.RecordID != a || got.Registration != nil || got.Frame != nil ||
		got.RefusalCode != "loop_cache_identity_unqualified" {
		t.Fatalf("invalid identity A: %+v", got)
	}
	if got := candidates[1]; got.RecordID != b || got.Registration == nil || got.Frame != nil ||
		got.RefusalCode != "loop_cache_value_too_large" || got.Registration.Tuple.KeyJSON != "b" {
		t.Fatalf("oversized B: %+v", got)
	}
	if got := candidates[2]; got.RecordID != c || got.Registration == nil || got.Frame == nil || got.RefusalCode != "" ||
		got.Frame.RecordID != c || got.Frame.Tuple != got.Registration.Tuple || !got.Frame.ValuePresent {
		t.Fatalf("healthy C: %+v", got)
	}
}

func TestMigrationSchema13AddsLoopTablesWithoutTouchingRecords(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "private", "archive.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	record := insertLoopRecord(t, s, loopFixtureSource, "kept", loopText(t, "kept"), "", "")
	for _, table := range []string{"loop_fts", "loop_channels", "loop_snapshot_losses", "loop_snapshots", "loop_attempt_evidence", "loop_locator_metadata"} {
		if _, err := s.db.Exec(`drop table ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`update schema_migrations set version=12`); err != nil {
		t.Fatal(err)
	}
	recordsState := func(s *Store) string {
		var state string
		if err := s.db.QueryRow(`select group_concat(rowid||source||key_json||value_json||updated_at,'|') from records`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := recordsState(s)
	must0(s.Close())
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if sources, err := ro.LoopSources(ctx); err != nil || len(sources) != 0 {
		t.Fatalf("schema 12 read: %v %v", sources, err)
	}
	if need, err := ro.NeedsUpgrade(ctx); err != nil || need {
		t.Fatalf("optional Loop tables contaminated legacy freshness: %t %v", need, err)
	}
	must0(ro.Close())
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if n := rowCount(t, s, `select max(version) from schema_migrations`); n != SchemaVersion || SchemaVersion != 13 {
		t.Fatalf("schema %d, want 13", n)
	}
	if after := recordsState(s); after != before {
		t.Fatal("migration changed existing rows")
	}
	if got := discover(t, s); len(got) != 1 || got[0].RecordID != record {
		t.Fatalf("migrated archive discovery: %+v", got)
	}
}
