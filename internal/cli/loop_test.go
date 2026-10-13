package cli

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
)

// loopCLIValue is a synthetic cached Loop snapshot with one channel per text under channels/<name>.
func loopCLIValue(t testing.TB, sequence int64, channels ...[2]string) string {
	t.Helper()
	entries, trees := []any{}, map[string]any{}
	for i, channel := range channels {
		length := len([]rune(channel[1]))
		header, err := json.Marshal(map[string]any{
			"chunkStartSegmentIndex": 0, "chunkSegmentCount": 1, "chunkLengthChars": length,
			"totalLengthChars": length, "totalSegmentCount": 1, "chunkSequenceNumber": sequence,
			"segmentTexts": []any{map[string]any{"text": channel[1]}},
			"headerMetadata": map[string]any{"sequenceNumber": sequence, "totalLength": length, "totalSegmentCount": 1,
				"orderedChunkMetadata": []any{map[string]any{"id": "header"}}}})
		if err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("b%d", i)
		entries = append(entries, []any{id, map[string]any{"$bytes": base64.StdEncoding.EncodeToString(header)}})
		trees[channel[0]] = map[string]any{"blobs": map[string]any{"header": id}, "trees": map[string]any{}}
	}
	raw, err := json.Marshal(map[string]any{"fileId": "synthetic-file", "cachedObject": map[string]any{"value": map[string]any{
		"sequenceNumber": sequence, "latestSequenceNumber": sequence, "ops": []any{}, "blobContents": map[string]any{"$map": entries},
		"snapshotTree": map[string]any{"blobs": map[string]any{}, "trees": map[string]any{
			"channels": map[string]any{"blobs": map[string]any{}, "trees": trees}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// loopRecords edits the generic records of the env's archive directly, creating it first.
func loopRecords(t *testing.T, e *env) *sql.DB {
	t.Helper()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", e.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertLoopCLI(t *testing.T, db *sql.DB, key string, value any) int64 {
	t.Helper()
	result, err := db.Exec(`insert into records(source,database,store,key_json,value_json,content_hash,first_seen_at,updated_at)
values('https://synthetic.invalid',?,?,?,?,'synthetic','2026-10-12T01:00:00.000Z','2026-10-12T01:00:00.000Z')`,
		store.LoopDatabase, store.LoopStore, key, value)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func exactDecode(t *testing.T, s string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return m
}

func runOK(t *testing.T, e *env, args ...string) string {
	t.Helper()
	code, stdout, stderr := e.run(args...)
	if code != 0 {
		t.Fatalf("%v exit %d: %s", args, code, stderr)
	}
	return stdout
}

func loopCoverageSources(t *testing.T, doc map[string]any) []map[string]any {
	t.Helper()
	coverage, ok := doc["loop_coverage"].(map[string]any)
	if !ok {
		t.Fatalf("no loop_coverage in %v", doc)
	}
	raw, _ := coverage["sources"].([]any)
	out := make([]map[string]any, len(raw))
	for i, r := range raw {
		out[i] = r.(map[string]any)
	}
	return out
}

func TestLoopHelpAndFailedSourceFollowup(t *testing.T) {
	e := newEnv(t)
	help := runOK(t, e, "loop", "--help")
	for _, want := range []string{"registration", "source_id", "snapshot_id", "partial_snapshot", "modified_at", "matches_retained_value",
		"loop sync --source-id", "not a full or current Loop page"} {
		if !strings.Contains(help, want) {
			t.Fatalf("loop --help does not explain %q:\n%s", want, help)
		}
	}
	db := loopRecords(t, e)
	insertLoopCLI(t, db, "failed", nil)
	insertLoopCLI(t, db, "metadata", loopCLIValue(t, 2))
	doc := decode(t, runOK(t, e, "loop", "sync"))
	if doc["failed"] != float64(1) || doc["projected"] != float64(1) {
		t.Fatalf("loop sync counts: %v", doc)
	}
	doc = decode(t, runOK(t, e, "--max-age", "0", "loop", "sources"))
	sources := items(t, doc)
	if len(sources) != 2 {
		t.Fatalf("sources: %v", sources)
	}
	for _, source := range sources {
		id := source["source_id"].(float64)
		retry := fmt.Sprintf("m365crawl loop sync --source-id %d", int64(id))
		switch source["tuple"].(map[string]any)["key_json"] {
		case "failed":
			if source["status"] != "failed" || source["reason"] != "loop_cache_value_missing" || source["retry"] != retry ||
				!strings.Contains(source["fix"].(string), retry) || source["snapshot_match"] != "no_admission" || source["last_snapshot_id"] != float64(0) {
				t.Fatalf("failed source follow-up: %v", source)
			}
		case "metadata":
			if source["coverage"] != "metadata_only" || source["retry"] != retry || !strings.Contains(source["effect"].(string), "no channel") {
				t.Fatalf("metadata-only follow-up: %v", source)
			}
		}
	}
	text := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "sources")
	if !strings.Contains(text, "loop sync --source-id") || !strings.Contains(text, "loop_cache_value_missing") {
		t.Fatalf("text sources lack the retry route:\n%s", text)
	}
	list := decode(t, runOK(t, e, "--max-age", "0", "loop", "list"))
	if len(items(t, list)) != 0 || !strings.Contains(list["note"].(string), "loop sources") || len(loopCoverageSources(t, list)) != 2 {
		t.Fatalf("registered sources without channels: %v", list)
	}
	empty := newEnv(t)
	none := decode(t, runOK(t, empty, "--max-age", "0", "loop", "list"))
	if !strings.Contains(none["note"].(string), "loop sync") || len(loopCoverageSources(t, none)) != 0 {
		t.Fatalf("no registration note: %v", none)
	}
	if sources := decode(t, runOK(t, empty, "loop", "sources")); !strings.Contains(sources["note"].(string), "loop sync") {
		t.Fatalf("no registration sources note: %v", sources)
	}
	if _, err := os.Stat(empty.db); err == nil {
		t.Fatal("archive-only loop list created an archive")
	}
}

func archiveDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temporary archive
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

func TestLoopArchiveOnlyRawMatch(t *testing.T) {
	e := newEnv(t)
	db := loopRecords(t, e)
	record := insertLoopCLI(t, db, "page", loopCLIValue(t, 3, [2]string{"native", "xray retained words"}))
	runOK(t, e, "loop", "sync")
	if _, err := db.Exec(`update records set value_json=?,updated_at='2026-10-12T05:00:00.000Z' where rowid=?`,
		loopCLIValue(t, 4, [2]string{"native", "yankee replacement"}), record); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := archiveDigest(t, e.db)
	list := decode(t, runOK(t, e, "--max-age", "0", "loop", "list"))
	rows := items(t, list)
	if len(rows) != 1 || rows[0]["matches_retained_value"] != false || rows[0]["retained"] != false ||
		rows[0]["raw_updated_at"] != "2026-10-12T05:00:00Z" || rows[0]["text"] != "xray retained words" || rows[0]["modified_at"] != nil ||
		rows[0]["text_state"] != "partial_snapshot" {
		t.Fatalf("archive-only list: %v", rows)
	}
	if source := loopCoverageSources(t, list)[0]; source["snapshot_match"] != "differs" || source["raw_present"] != true {
		t.Fatalf("archive-only coverage: %v", source)
	}
	show := decode(t, runOK(t, e, "--max-age", "0", "loop", "show", rows[0]["id"].(string)))
	detail := items(t, show)
	if len(detail) != 1 || detail[0]["id"] != rows[0]["id"] || detail[0]["matches_retained_value"] != false ||
		detail[0]["raw_updated_at"] != "2026-10-12T05:00:00Z" || loopCoverageSources(t, show)[0]["snapshot_match"] != "differs" {
		t.Fatalf("archive-only detail: %v", show)
	}
	zero := decode(t, runOK(t, e, "--max-age", "0", "loop", "search", "nothingmatchesthis"))
	if len(items(t, zero)) != 0 || loopCoverageSources(t, zero)[0]["snapshot_match"] != "differs" ||
		loopCoverageSources(t, zero)[0]["raw_updated_at"] != "2026-10-12T05:00:00Z" {
		t.Fatalf("zero-hit coverage: %v", zero)
	}
	if after := archiveDigest(t, e.db); after != before {
		t.Fatal("archive-only reads changed archive bytes")
	}
	// An age-permitted read then projects Y; X stays inspectable by its stable ID as retained.
	runOK(t, e, "loop", "sync")
	retained := items(t, decode(t, runOK(t, e, "--max-age", "0", "loop", "show", rows[0]["id"].(string))))
	if retained[0]["retained"] != true || retained[0]["text"] != "xray retained words" {
		t.Fatalf("retained X: %v", retained)
	}
}

func TestLoopExactSequenceTransport(t *testing.T) {
	e := newEnv(t)
	db := loopRecords(t, e)
	insertLoopCLI(t, db, "big", loopCLIValue(t, 9007199254740993, [2]string{"big", "sequence alpha"}))
	insertLoopCLI(t, db, "max", loopCLIValue(t, math.MaxInt64, [2]string{"max", "sequence bravo"}))
	runOK(t, e, "loop", "sync")
	want := map[string]string{"channels/big": "9007199254740993", "channels/max": "9223372036854775807"}
	check := func(label string, rows []any) {
		t.Helper()
		if len(rows) != 2 {
			t.Fatalf("%s rows: %v", label, rows)
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			path, _ := row["path"].(string)
			if path == "" {
				path = "channels/" + map[string]string{"sequence alpha": "big", "sequence bravo": "max"}[row["text"].(string)]
			}
			if fmt.Sprint(row["sequence"]) != want[path] {
				t.Fatalf("%s sequence of %s: %v", label, path, row["sequence"])
			}
			if snapshot, ok := row["snapshot_sequence"]; ok && fmt.Sprint(snapshot) != want[path] {
				t.Fatalf("%s snapshot sequence: %v", label, snapshot)
			}
		}
	}
	check("plain", exactDecode(t, runOK(t, e, "--max-age", "0", "loop", "list"))["items"].([]any))
	check("fields", exactDecode(t, runOK(t, e, "--max-age", "0", "--fields", "sequence,text", "loop", "search", "sequence"))["items"].([]any))
	check("shared", exactDecode(t, runOK(t, e, "--max-age", "0", "search", "sequence", "--source", "loop"))["items"].([]any))
	check("shared fields", exactDecode(t, runOK(t, e, "--max-age", "0", "--fields", "source,path,sequence,snapshot_sequence", "search", "sequence", "--source", "loop"))["items"].([]any))
	text := runOK(t, e, "--max-age", "0", "--format", "text", "--fields", "path,sequence", "loop", "list")
	if !strings.Contains(text, "9007199254740993") || !strings.Contains(text, "9223372036854775807") {
		t.Fatalf("text projection lost exact sequences:\n%s", text)
	}
}

func TestLoopEveryFailureReasonHasARemedy(t *testing.T) {
	commands, err := os.ReadFile("../../docs/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	reasons := append(append([]string{}, store.LoopFailureReasons...), "stale_attempt")
	for _, reason := range reasons {
		if loopFailureFix[reason] == "" || !strings.Contains(string(commands), "| `"+reason+"` |") {
			t.Fatalf("Loop failure reason %s lacks a remedy or a docs/commands.md entry", reason)
		}
	}
	if len(loopFailureFix) != len(reasons) {
		t.Fatal("a remedy exists for an undocumented Loop reason")
	}
}
