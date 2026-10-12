package syncer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

var loopBase = time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)

// syntheticLoop is a synthetic cached Loop snapshot with one channel per text, each under
// channels/c<i>; an empty list is metadata-only.
func syntheticLoop(t testing.TB, sequence, latest int64, texts ...string) string {
	t.Helper()
	entries, trees := []any{}, map[string]any{}
	for i, text := range texts {
		length := len([]rune(text))
		header, err := json.Marshal(map[string]any{
			"chunkStartSegmentIndex": 0, "chunkSegmentCount": 1, "chunkLengthChars": length,
			"totalLengthChars": length, "totalSegmentCount": 1, "chunkSequenceNumber": sequence,
			"segmentTexts": []any{map[string]any{"text": text}},
			"headerMetadata": map[string]any{"sequenceNumber": sequence, "totalLength": length, "totalSegmentCount": 1,
				"orderedChunkMetadata": []any{map[string]any{"id": "header"}}}})
		if err != nil {
			t.Fatal(err)
		}
		id := string(rune('a' + i))
		entries = append(entries, []any{id, map[string]any{"$bytes": base64.StdEncoding.EncodeToString(header)}})
		trees["c"+id] = map[string]any{"blobs": map[string]any{"header": id}, "trees": map[string]any{}}
	}
	raw, err := json.Marshal(map[string]any{"fileId": "synthetic-file", "cachedObject": map[string]any{"value": map[string]any{
		"sequenceNumber": sequence, "latestSequenceNumber": latest, "ops": []any{}, "blobContents": map[string]any{"$map": entries},
		"snapshotTree": map[string]any{"blobs": map[string]any{}, "trees": map[string]any{
			"channels": map[string]any{"blobs": map[string]any{}, "trees": trees}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// loopArchive is a writable synthetic archive whose generic records a test edits through SQL.
type loopArchive struct {
	path string
	db   *sql.DB
}

func newLoopArchive(t *testing.T) *loopArchive {
	t.Helper()
	path := newDB(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &loopArchive{path: path, db: db}
}

func (a *loopArchive) insert(t *testing.T, source, key string, value any) int64 {
	t.Helper()
	result, err := a.db.Exec(`insert into records(source,database,store,key_json,value_json,content_hash,first_seen_at,updated_at)
values(?,?,?,?,?,'synthetic',?,?)`, source, store.LoopDatabase, store.LoopStore, key, value,
		loopBase.Format(time.RFC3339), loopBase.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func (a *loopArchive) set(t *testing.T, record int64, value any) {
	t.Helper()
	// As a sync does: a changed value changes the record's content hash.
	if _, err := a.db.Exec(`update records set value_json=?,content_hash=? where rowid=?`, value,
		fmt.Sprintf("%x", sha256.Sum256(fmt.Append(nil, value))), record); err != nil {
		t.Fatal(err)
	}
}

func (a *loopArchive) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := a.db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sourceByKey(t *testing.T, sources []store.LoopSource, key string) store.LoopSource {
	t.Helper()
	for _, source := range sources {
		if source.Registration.Tuple.KeyJSON == key {
			return source
		}
	}
	t.Fatalf("no source %s in %d", key, len(sources))
	return store.LoopSource{}
}

func explicitLoop(t *testing.T, path string, sourceID int64) LoopReport {
	t.Helper()
	report, err := SyncLoop(context.Background(), LoopOptions{DBPath: path, SourceID: sourceID, Explicit: true, Discover: sourceID == 0})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestLoopDiscoveryIncludesRefusedValues(t *testing.T) {
	a := newLoopArchive(t)
	a.insert(t, "bad\xff", "invalid", "SECRET-INVALID-VALUE")
	a.insert(t, "https://synthetic.invalid", "unsupported", `{"not":"loop"}`)
	a.insert(t, "https://synthetic.invalid", "oversized", strings.Repeat(" ", store.LoopValueLimit+1))
	a.insert(t, "https://synthetic.invalid", "null", nil)
	a.insert(t, "https://synthetic.invalid", "metadata", syntheticLoop(t, 4, 4))
	a.insert(t, "https://synthetic.invalid", "healthy", syntheticLoop(t, 4, 4, "healthy channel words"))
	report := explicitLoop(t, a.path, 0)
	if report.Projected != 2 || report.Failed != 3 || len(report.Sources) != 5 || len(report.Diagnostics) != 1 ||
		report.InvalidRecords != 1 || report.DiscoveryRemaining {
		t.Fatalf("mixed batch: %+v", report)
	}
	if strings.Contains(report.Diagnostics[0], "SECRET") || strings.Contains(report.Diagnostics[0], "invalid") ||
		!strings.HasPrefix(report.Diagnostics[0], "loop_cache_identity_unqualified: records rowid ") {
		t.Fatalf("invalid identity diagnostic: %q", report.Diagnostics[0])
	}
	for key, reason := range map[string]string{"unsupported": "loop_cache_snapshot_unsupported",
		"oversized": "loop_cache_value_too_large", "null": "loop_cache_value_missing"} {
		source := sourceByKey(t, report.Sources, key)
		if source.Registration.ID <= 0 || source.Health.Status != "failed" || source.Health.Reason != reason {
			t.Fatalf("%s not a selectable failed registration: %+v", key, source)
		}
	}
	if metadata := sourceByKey(t, report.Sources, "metadata"); metadata.Health.Coverage != "metadata_only" || metadata.SnapshotMatch != "matches" {
		t.Fatalf("metadata-only: %+v", metadata)
	}
	if healthy := sourceByKey(t, report.Sources, "healthy"); healthy.Health.Coverage != "channels" || healthy.LastSnapshotID <= 0 {
		t.Fatalf("healthy peer: %+v", healthy)
	}
	if n := a.count(t, `select count(*) from loop_locator_metadata`); n != 5 {
		t.Fatalf("registrations %d", n)
	}
	oversized := sourceByKey(t, report.Sources, "oversized")
	selected := explicitLoop(t, a.path, oversized.Registration.ID)
	if selected.Failed != 1 || len(selected.Sources) != 1 || selected.Sources[0].Registration.ID != oversized.Registration.ID {
		t.Fatalf("oversized not selectable: %+v", selected)
	}
}

func TestLoopSelectedFailedRetry(t *testing.T) {
	a := newLoopArchive(t)
	failed := a.insert(t, "https://synthetic.invalid", "failed", nil)
	a.insert(t, "https://synthetic.invalid", "peer", syntheticLoop(t, 1, 1, "peer words"))
	report := explicitLoop(t, a.path, 0)
	source := sourceByKey(t, report.Sources, "failed")
	a.set(t, failed, syntheticLoop(t, 2, 2, "recovered words"))
	attempts := a.count(t, `select count(*) from knowledge_attempts`)
	retry := explicitLoop(t, a.path, source.Registration.ID)
	if retry.Projected != 1 || len(retry.Sources) != 1 || retry.Sources[0].Registration.ID != source.Registration.ID ||
		retry.Sources[0].Health.Status != "complete" {
		t.Fatalf("selected retry: %+v", retry)
	}
	if n := a.count(t, `select count(*) from knowledge_attempts`); n != attempts+1 {
		t.Fatalf("selected retry touched other sources: %d attempts", n-attempts)
	}
	if _, err := SyncLoop(context.Background(), LoopOptions{DBPath: a.path, SourceID: 999, Explicit: true}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatalf("unknown selected source: %v", err)
	}
}

func TestLoopAgeZeroAndRootlessReads(t *testing.T) {
	ctx := context.Background()
	missing := newDB(t)
	report, err := SyncLoop(ctx, LoopOptions{DBPath: missing, MaxAge: time.Hour})
	if err != nil || len(report.Sources) != 0 {
		t.Fatalf("unregistered ordinary read: %+v %v", report, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ordinary read created an archive")
	}
	if _, err := SyncLoop(ctx, LoopOptions{DBPath: missing, SourceID: 1}); !errors.Is(err, store.ErrKnowledgeSourceUnknown) {
		t.Fatalf("selected source in a missing archive: %v", err)
	}
	a := newLoopArchive(t)
	record := a.insert(t, "https://synthetic.invalid", "aged", syntheticLoop(t, 1, 1, "first words"))
	explicitLoop(t, a.path, 0)
	a.set(t, record, syntheticLoop(t, 2, 2, "second words"))
	attempts := a.count(t, `select count(*) from knowledge_attempts`)
	for _, options := range []LoopOptions{{DBPath: a.path, MaxAge: 0}, {DBPath: a.path, MaxAge: time.Hour}} {
		report, err := SyncLoop(ctx, options)
		if err != nil || len(report.Sources) != 1 || report.Projected != 0 || report.Sources[0].SnapshotMatch != "differs" {
			t.Fatalf("archive-only or recent read projected: %+v %v", report, err)
		}
	}
	if n := a.count(t, `select count(*) from knowledge_attempts`); n != attempts {
		t.Fatal("archive-only read began an attempt")
	}
	now := loopNow
	loopNow = func() time.Time { return now().Add(2 * time.Hour) }
	t.Cleanup(func() { loopNow = now })
	report, err = SyncLoop(ctx, LoopOptions{DBPath: a.path, MaxAge: time.Hour})
	if err != nil || report.Projected != 1 || report.Sources[0].SnapshotMatch != "matches" {
		t.Fatalf("age-permitted read did not refresh: %+v %v", report, err)
	}
	// An archive at schema 12 with --max-age 0 is not migrated.
	if _, err := a.db.Exec(`update schema_migrations set version=12`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"loop_fts", "loop_channels", "loop_snapshot_losses", "loop_snapshots", "loop_attempt_evidence", "loop_locator_metadata"} {
		if _, err := a.db.Exec(`drop table ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if report, err := SyncLoop(ctx, LoopOptions{DBPath: a.path, MaxAge: 0}); err != nil || len(report.Sources) != 0 {
		t.Fatalf("old archive read: %+v %v", report, err)
	}
	if v := a.count(t, `select max(version) from schema_migrations`); v != 12 {
		t.Fatalf("archive-only read migrated to %d", v)
	}
	for _, invalid := range []LoopOptions{{SourceID: -1}, {MaxAge: -1}, {Discover: true}, {Explicit: true, Discover: true, SourceID: 1}} {
		invalid.DBPath = a.path
		if _, err := SyncLoop(ctx, invalid); err == nil {
			t.Fatalf("invalid options accepted: %+v", invalid)
		}
	}
}
