package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"reflect"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/engagecontent"
	"github.com/ourostack/m365crawl/internal/store"
)

type maxTextArchiveState struct {
	mode os.FileMode
	size int64
	time time.Time
	hash [32]byte
}

func maxTextArchiveSnapshot(t *testing.T, e *env) maxTextArchiveState {
	t.Helper()
	info, err := os.Stat(e.db)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(e.db)
	if err != nil {
		t.Fatal(err)
	}
	return maxTextArchiveState{info.Mode().Perm(), info.Size(), info.ModTime(), sha256.Sum256(data)}
}

func TestMaxTextOfflineReadsPreservePOSIX0400AndFullEvidence(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("0400 is a POSIX mode condition, not a Windows ACL claim")
	}
	e := contentFollowupEnv(t)
	ctx := context.Background()
	acquired := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	r := engageCLIResult("synthetic-readonly")
	title := "😀😁😂😃😄😅😆😇😈"
	r.Threads[0].Title = &title
	r.Threads[0].ID = "9007199254740993"
	raw := "0000-01-01T00:30:00+01:00"
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatal(err)
	}
	r.Threads[0].StarterUpdatedRaw, r.Threads[0].StarterUpdatedAt = &raw, &at
	r.Losses[0].Count = 3
	r.Threads[0].Blocks = make([]string, 29)
	for i := range r.Threads[0].Blocks {
		r.Threads[0].Blocks[i] = "syntheticword abcdefghijk"
	}
	r.Threads[0].Blocks[1] = ""
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := st.BeginEngageAttempt(ctx, store.KnowledgeInput{Kind: store.EngageKind, Platform: "browser", Locator: store.EngageHomeURL, At: acquired})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CommitEngageCapture(ctx, attempt, store.EngageCapture{Result: r, AcquiredAt: acquired, AdmittedAt: acquired.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	detail, err := st.EngageDetail(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.db, 0o400); err != nil {
		t.Fatal(err)
	}
	baseline := maxTextArchiveSnapshot(t, e)
	if baseline.mode != 0o400 {
		t.Fatal("fixture baseline is not0400")
	}
	check := func(label string) {
		t.Helper()
		got := maxTextArchiveSnapshot(t, e)
		if got != baseline {
			t.Fatalf("attributable offline metadata mutation after %s: before=%+v after=%+v", label, baseline, got)
		}
		t.Logf("%s: mode=%04o bytes=%d mtime=%s sha256=%x", label, got.mode, got.size, got.time.Format(time.RFC3339Nano), got.hash)
	}
	var command string
	for _, args := range [][]string{
		{"content", "list"}, {"content", "sources"},
		{"content", "show", "engage-observation:1"},
		{"content", "search", "syntheticword"}, {"content", "search", "absentword"},
		{"--max-text", "8", "content", "show", "engage-observation:1"},
		{"--max-text", "1", "content", "show", "engage-observation:1"},
		{"--fields", "id,blocks,blocks_total,title,text_truncated", "--max-text", "8", "content", "show", "engage-observation:1"},
		{"--fields", "id", "content", "list"},
		{"--fields", "id", "search", "syntheticword", "--source", "content"},
		{"search", "absentword", "--source", "content"},
		{"--format", "text", "--max-text", "8", "content", "show", "engage-observation:1"},
	} {
		full := append([]string{"--max-age", "1ns"}, args...)
		code, out, stderr := e.run(full...)
		check(strings.Join(full, " "))
		if code != 0 {
			t.Fatal(stderr)
		}
		if slices.Contains(args, "text") {
			continue
		}
		doc := decode(t, out)
		if doc["sync_error"] != nil || doc["synced"] != nil {
			t.Fatal("ordinary content read entered default freshness")
		}
		if len(args) == 3 && args[1] == "show" {
			item := items(t, doc)[0]
			command = item["full_text_command"].(string)
			if item["native_thread_id"] != "9007199254740993" || item["representation_digest"] != detail.Observation.RepresentationDigest ||
				item["starter_updated_raw"] != raw || !reflect.DeepEqual(item["blocks"], stringsAsAny(r.Threads[0].Blocks)) {
				t.Fatal("unbounded detail altered original evidence")
			}
		}
		if slices.Contains(args, "--max-text") && slices.Contains(args, "show") {
			maximum := 8
			if slices.Contains(args, "1") {
				maximum = 1
			}
			item := items(t, doc)[0]
			blocks := item["blocks"].([]any)
			if len(blocks) != 29 || item["blocks_total"] != float64(29) || item["text_truncated"] != true {
				t.Fatal("bounded detail dropped occurrences or flags")
			}
			for _, block := range blocks {
				if utf8.RuneCountInString(block.(string)) > maximum {
					t.Errorf("decoded emitted block exceeded inclusive%d", maximum)
				}
			}
			if utf8.RuneCountInString(item["title"].(string)) > maximum {
				t.Errorf("decoded emitted title exceeded inclusive%d", maximum)
			}
		}
	}
	words := requireContentFollowupArchive(t, e, command)
	if !slices.Contains(words, "--max-age") || words[slices.Index(words, "--max-age")+1] != "0" {
		t.Fatal("emitted full command is not selected archive/max-age0")
	}
	code, out, stderr := replayContentFollowup(t, e, command)
	check("exact emitted selected-DB max-age0 SQL")
	if code != 0 {
		t.Fatal(stderr)
	}
	doc := decode(t, out)
	columns := stringsFromAny(doc["columns"].([]any))
	row := doc["rows"].([]any)[0].([]any)
	var fullResult engagecontent.Result
	if err := json.Unmarshal([]byte(row[slices.Index(columns, "result_json")].(string)), &fullResult); err != nil || !reflect.DeepEqual(fullResult, r) {
		t.Fatal("full SQL did not recover exact original native Result", err)
	}
	sqlAcquired, err := time.Parse(time.RFC3339Nano, row[slices.Index(columns, "acquired_at")].(string))
	if err != nil || row[slices.Index(columns, "representation_digest")] != detail.Observation.RepresentationDigest ||
		!sqlAcquired.Equal(detail.Observation.AcquiredAt) {
		t.Fatal("full SQL lost original digest/acquisition clock")
	}
}

func stringsAsAny(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func stringsFromAny(values []any) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = value.(string)
	}
	return out
}
