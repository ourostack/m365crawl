package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
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

// discover acquires every unregistered structural record with its value, in record order: the
// acquisition a caller performs one record at a time after LoopDiscoveryCandidates.
func discover(t testing.TB, s *Store) []LoopAcquisition {
	t.Helper()
	ctx := context.Background()
	ids, err := loopRecordIDs(ctx, s.db, `select r.rowid from records r where r.database=? and r.store=?
and not exists(select 1 from loop_locator_metadata m where m.source=r.source and m.key_json=r.key_json) order by r.rowid`, LoopDatabase, LoopStore)
	if err != nil {
		t.Fatal(err)
	}
	out := []LoopAcquisition{}
	for _, id := range ids {
		acquisition, err := readLoopAcquisition(ctx, s.db, id)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, acquisition)
	}
	return out
}

func candidates(t testing.TB, s *Store) LoopDiscovery {
	t.Helper()
	discovery, err := s.LoopDiscoveryCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return discovery
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

// Discovery loads no value, never blocks on more than one batch and never lets identity-invalid
// records consume the batch: each run takes the next identity-valid records in record order.
func TestLoopDiscoveryBatchesWithoutValuesOrBlocking(t *testing.T) {
	s := newStore(t)
	reads := countValueReads(t)
	invalid := []int64{}
	for i := range LoopDiagnosticLimit + 3 {
		invalid = append(invalid, insertLoopRecord(t, s, "bad\xff"+strings.Repeat("x", i), "invalid", loopText(t, "refused"), "", ""))
	}
	valid := []int64{}
	for i := range LoopDiscoveryBatch + 5 {
		valid = append(valid, insertLoopRecord(t, s, loopFixtureSource, "key-"+strings.Repeat("x", i), loopText(t, "valued"), "", ""))
	}
	insertLoopRecord(t, s, loopFixtureSource, "elsewhere", "{}", "", "")
	if _, err := s.db.Exec(`update records set database='otherDatabase' where key_json='elsewhere'`); err != nil {
		t.Fatal(err)
	}
	first := candidates(t, s)
	if *reads != 0 {
		t.Fatalf("discovery loaded %d values, want 0", *reads)
	}
	if len(first.Candidates) != LoopDiscoveryBatch || first.Remaining != 5 || first.Invalid != len(invalid) ||
		len(first.InvalidRecords) != LoopDiagnosticLimit || first.InvalidRecords[0] != invalid[0] {
		t.Fatalf("first pass: %d candidates, %d remaining, %d invalid, %d named", len(first.Candidates), first.Remaining,
			first.Invalid, len(first.InvalidRecords))
	}
	for i, candidate := range first.Candidates {
		if candidate.RecordID != valid[i] || candidate.Frame != nil || candidate.Registration == nil || candidate.RefusalCode != "" {
			t.Fatalf("candidate %d out of record order or carrying a value: %+v", i, candidate)
		}
		mustBeginLoop(t, s, *candidate.Registration, base)
	}
	second := candidates(t, s)
	if len(second.Candidates) != 5 || second.Remaining != 0 || second.Invalid != len(invalid) || second.Candidates[0].RecordID != valid[LoopDiscoveryBatch] {
		t.Fatalf("second pass did not continue: %d candidates, %d remaining", len(second.Candidates), second.Remaining)
	}
	for _, candidate := range second.Candidates {
		mustBeginLoop(t, s, *candidate.Registration, base)
	}
	if third := candidates(t, s); len(third.Candidates) != 0 || third.Remaining != 0 || third.Invalid != len(invalid) || *reads != 0 {
		t.Fatalf("drained discovery: %+v reads=%d", third, *reads)
	}
}

// A read of an unchanged registered source compares the stored fingerprint and loads no value; a
// changed record is loaded and hashed, and a selected read touches only its own source.
func TestLoopUnchangedReadLoadsNoValue(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := insertLoopRecord(t, s, loopFixtureSource, "a", loopText(t, "alpha words"), "", "")
	b := insertLoopRecord(t, s, loopFixtureSource, "b", loopText(t, "bravo words"), "", "")
	aID, bID := registerLoop(t, s, a, base), registerLoop(t, s, b, base)
	reads := countValueReads(t)
	snapshot, err := s.LoopSnapshot(ctx, LoopFilter{})
	if err != nil || len(snapshot.Rows) != 2 || !snapshot.Rows[0].MatchesRetainedValue || snapshot.Sources[1].SnapshotMatch != "matches" {
		t.Fatalf("unchanged snapshot: %+v %v", snapshot, err)
	}
	if _, _, err := s.LoopChannel(ctx, snapshot.Rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if sources, err := s.LoopSources(ctx, 0); err != nil || len(sources) != 2 {
		t.Fatalf("sources: %v %v", sources, err)
	}
	if *reads != 0 {
		t.Fatalf("unchanged reads loaded %d values, want 0", *reads)
	}
	// Same length and clock, different bytes and content hash: the fingerprint differs, so it is hashed.
	updateLoopRecord(t, s, b, "value_json=?,content_hash='changed'", loopText(t, "bravo wordz"))
	if sources, err := s.LoopSources(ctx, aID); err != nil || len(sources) != 1 || sources[0].SnapshotMatch != "matches" || *reads != 0 {
		t.Fatalf("selected unchanged source loaded a peer: %v %v reads=%d", sources, err, *reads)
	}
	if source := loopSource(t, s, bID); source.SnapshotMatch != "differs" || *reads != 1 {
		t.Fatalf("changed source: %s reads=%d", source.SnapshotMatch, *reads)
	}
	// A clock alone never establishes a match: with a matching clock and length but an unknown
	// content hash, the value is still hashed.
	updateLoopRecord(t, s, b, "content_hash=''")
	*reads = 0
	if source := loopSource(t, s, bID); source.SnapshotMatch != "differs" || *reads != 1 {
		t.Fatalf("fingerprint without a content hash was trusted: %s reads=%d", source.SnapshotMatch, *reads)
	}
	// A fingerprint stored without a content hash never matches, even when nothing changed.
	d := insertLoopRecord(t, s, loopFixtureSource, "d", loopText(t, "delta words"), "", "")
	updateLoopRecord(t, s, d, "content_hash=''")
	dID := registerLoop(t, s, d, base)
	*reads = 0
	if source := loopSource(t, s, dID); source.SnapshotMatch != "matches" || *reads != 1 {
		t.Fatalf("empty stored content hash was trusted: %s reads=%d", source.SnapshotMatch, *reads)
	}
	// The raw digest and the redaction state are part of the fingerprint.
	for _, assignment := range []string{"raw_digest=x'01'", "raw_digest=null,value_redacted=1"} {
		updateLoopRecord(t, s, a, assignment)
		*reads = 0
		if source := loopSource(t, s, aID); source.SnapshotMatch != "matches" || *reads != 1 {
			t.Fatalf("%s did not force a hash: %s reads=%d", assignment, source.SnapshotMatch, *reads)
		}
	}
	// A source that was never admitted has no snapshot to compare, so its value is never loaded.
	c := insertLoopRecord(t, s, loopFixtureSource, "c", loopText(t, "charlie words"), "", "")
	var never int64
	for _, candidate := range discover(t, s) {
		if candidate.RecordID == c {
			attempt := mustBeginLoop(t, s, *candidate.Registration, base)
			if err := s.RecordLoopFailure(ctx, attempt, "loop_cache_snapshot_unsupported", base); err != nil {
				t.Fatal(err)
			}
			never = attempt.LocatorID
		}
	}
	*reads = 0
	if source := loopSource(t, s, never); source.SnapshotMatch != "no_admission" || !source.RawValuePresent || *reads != 0 {
		t.Fatalf("never-admitted source: %s present=%v reads=%d", source.SnapshotMatch, source.RawValuePresent, *reads)
	}
	if _, err := s.LoopSources(ctx, 999); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown selected source: %v", err)
	}
	if _, err := s.LoopSources(ctx, -1); !errors.Is(err, ErrKnowledgeSourceUnknown) {
		t.Fatalf("negative selected source: %v", err)
	}
}

// Owner strings of a registered record leave the archive only after the same validation that
// registration applies.
func TestLoopInvalidCurrentOwnerIsNeverEchoed(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for name, assignment := range map[string]string{"invalid UTF-8": "tenant_id=cast(x'ff5345435245' as text)",
		"blob": "user_id=x'5345435245'", "oversized": "tenant_id='" + strings.Repeat("t", LoopOwnerLimit+1) + "'"} {
		record := insertLoopRecord(t, s, loopFixtureSource, name, loopText(t, "owned words"), "tenant-a", "user-a")
		id := registerLoop(t, s, record, base)
		updateLoopRecord(t, s, record, assignment)
		source := loopSource(t, s, id)
		if source.Registration.TenantID != "" || source.Registration.UserID != "" || source.Reason != "loop_cache_identity_unqualified" ||
			source.SnapshotMatch != "unavailable" || source.LastSnapshotTenantID != "tenant-a" {
			t.Fatalf("%s owner echoed or not refused: %+v", name, source)
		}
		acquisition, err := s.LoopRawFrame(ctx, id)
		if err != nil || acquisition.Frame != nil || acquisition.Registration.TenantID != "" || acquisition.Registration.UserID != "" {
			t.Fatalf("%s owner acquired: %+v %v", name, acquisition, err)
		}
	}
}

// Rows are ordered by ASCII-folded path, then registration, snapshot and numeric channel ID, before the limit.
func TestLoopOrderingBeforeLimit(t *testing.T) {
	s := newStore(t)
	// Registered so that channel IDs run against the required order.
	first := insertLoopRecord(t, s, loopFixtureSource, "first", loopEnvelope(t, "f", 1, 1,
		loopChannelSpec{name: "zulu", text: "ordered words", sequence: 1}, loopChannelSpec{name: "Bravo", text: "ordered words", sequence: 1}), "", "")
	second := insertLoopRecord(t, s, loopFixtureSource, "second", loopEnvelope(t, "f", 1, 1,
		loopChannelSpec{name: "bravo", text: "ordered words", sequence: 1}, loopChannelSpec{name: "alpha", text: "ordered words", sequence: 1}), "", "")
	firstID := registerLoop(t, s, first, base)
	secondID := registerLoop(t, s, second, base)
	want := []string{"channels/alpha", "channels/Bravo", "channels/bravo", "channels/zulu"}
	snapshot, err := s.LoopSnapshot(context.Background(), LoopFilter{Query: "ordered"})
	if err != nil || len(snapshot.Rows) != 4 {
		t.Fatalf("rows: %+v %v", snapshot.Rows, err)
	}
	for i, row := range snapshot.Rows {
		if row.Channel.Path != want[i] {
			t.Fatalf("row %d is %s, want %s", i, row.Channel.Path, want[i])
		}
	}
	if snapshot.Rows[0].ID < snapshot.Rows[1].ID || snapshot.Rows[1].RegistrationID != firstID || snapshot.Rows[2].RegistrationID != secondID {
		t.Fatalf("fixture or registration order wrong: %+v", snapshot.Rows)
	}
	clipped, err := s.LoopSnapshot(context.Background(), LoopFilter{Query: "ordered", Limit: 2})
	if err != nil || !clipped.Truncated || len(clipped.Rows) != 2 || clipped.Rows[0].Channel.Path != want[0] ||
		clipped.Rows[1].Channel.Path != want[1] || clipped.Rows[1].RegistrationID != firstID {
		t.Fatalf("limit applied before ordering: %+v %v", clipped.Rows, err)
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
	sources, err := s.LoopSources(ctx, 0)
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
	sources, err = s.LoopSources(ctx, 0)
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
	if sources, err := ro.LoopSources(ctx, 0); err != nil || len(sources) != 0 {
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

// The archive's own writer rewrites the content hash with the value, so a same-length change it
// makes is seen without a stale match.
func TestLoopSeesAChangeWrittenByTheArchive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	write := func(text string, at time.Time) {
		t.Helper()
		x, err := s.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x.UpsertRecords(loopFixtureSource, []teamsdesktop.GenericRecord{{Database: LoopDatabase, Store: LoopStore,
			KeyJSON: []byte(`"written"`), ValueJSON: []byte(loopText(t, text))}}, at); err != nil {
			t.Fatal(err)
		}
		if err := x.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha words", base)
	var record int64
	if err := s.db.QueryRow(`select rowid from records where key_json='"written"'`).Scan(&record); err != nil {
		t.Fatal(err)
	}
	id := registerLoop(t, s, record, base)
	reads := countValueReads(t)
	if source := loopSource(t, s, id); source.SnapshotMatch != "matches" || *reads != 0 {
		t.Fatalf("unchanged: %s reads=%d", source.SnapshotMatch, *reads)
	}
	write("alpha wordz", base) // same length, same clock
	snapshot, err := s.LoopSnapshot(ctx, LoopFilter{})
	if err != nil || len(snapshot.Rows) != 1 || snapshot.Rows[0].MatchesRetainedValue || snapshot.Sources[0].SnapshotMatch != "differs" {
		t.Fatalf("same-length change through the archive writer: %+v %v", snapshot, err)
	}
}
