package cli

import (
	"strings"
	"testing"
)

func seedLoopSearch(t *testing.T, e *env) {
	t.Helper()
	e.sync()
	db := loopRecords(t, e)
	insertLoopCLI(t, db, "hello", loopCLIValue(t, 5, [2]string{"greeting", "Hello from a cached Loop channel"}))
	insertLoopCLI(t, db, "failed", nil)
	insertLoopCLI(t, db, "metadata", loopCLIValue(t, 6))
	runOK(t, e, "loop", "sync")
}

func searchSourceCount(t *testing.T, doc map[string]any, source string) map[string]any {
	t.Helper()
	sources, _ := doc["sources"].(map[string]any)
	count, _ := sources[source].(map[string]any)
	return count
}

func TestLoopGlobalLimitCoverageAndClipping(t *testing.T) {
	e := newEnv(t)
	seedLoopSearch(t, e)
	full := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--limit", "500"))
	var legacy, loop int
	for _, item := range items(t, full) {
		if item["source"] == "loop" {
			loop++
			if item["id"] == nil || !strings.HasPrefix(item["id"].(string), "loop-channel:") {
				t.Fatalf("loop hit without follow-up: %v", item)
			}
		} else {
			legacy++
		}
	}
	if loop != 1 || legacy == 0 || items(t, full)[len(items(t, full))-1]["source"] != "loop" {
		t.Fatalf("undated Loop hit not after dated hits: legacy=%d loop=%d", legacy, loop)
	}
	if full["total"] != nil {
		t.Fatal("legacy-only total presented for an all-source search")
	}
	clipped := decode(t, runOK(t, e, "--no-sync", "--fields", "source,text", "search", "Hello", "--limit", "1"))
	if len(items(t, clipped)) != 1 || items(t, clipped)[0]["source"] == "loop" || clipped["truncated"] != true || clipped["total"] != nil {
		t.Fatalf("global clip: %v", clipped)
	}
	if count := searchSourceCount(t, clipped, "loop"); count == nil || count["count"] != float64(0) || count["truncated"] != true {
		t.Fatalf("clipped Loop participation: %v", clipped["sources"])
	}
	sources := loopCoverageSources(t, clipped)
	states := map[string]bool{}
	for _, source := range sources {
		states[source["snapshot_match"].(string)+"/"+source["state"].(string)] = true
	}
	if len(sources) != 3 || !states["matches/complete"] || !states["no_admission/failed"] || !states["matches/metadata_only"] {
		t.Fatalf("participating coverage clipped with rows: %v", states)
	}
	zero := decode(t, runOK(t, e, "--no-sync", "search", "Meeting", "--limit", "5"))
	for _, item := range items(t, zero) {
		if item["source"] == "loop" {
			t.Fatal("Loop hit for a word it does not hold")
		}
	}
	if count := searchSourceCount(t, zero, "loop"); count == nil || count["count"] != float64(0) || count["truncated"] != false ||
		len(loopCoverageSources(t, zero)) != 3 {
		t.Fatalf("true zero Loop matches lost coverage: %v", zero)
	}
	only := decode(t, runOK(t, e, "--no-sync", "search", "nothingatall", "--source", "loop"))
	if len(items(t, only)) != 0 || len(loopCoverageSources(t, only)) != 3 || !strings.Contains(only["note"].(string), "loop") {
		t.Fatalf("Loop-only zero hits: %v", only)
	}
	text := runOK(t, e, "--no-sync", "--format", "text", "search", "Hello", "--source", "loop")
	if !strings.Contains(text, "loop show loop-channel:") || !strings.Contains(text, "loop coverage") {
		t.Fatalf("text search lacks the Loop follow-up or coverage:\n%s", text)
	}
}

func TestLoopScopeExclusions(t *testing.T) {
	e := newEnv(t)
	seedLoopSearch(t, e)
	for _, flags := range [][]string{{"--since", "2023-01-01"}, {"--until", "2030-01-01"}, {"--from", "Someone"},
		{"--conversation", "19:x"}, {"--team", "Team"}, {"--folder", "inbox"}, {"--mentions-me"}, {"--direct-mentions"},
		{"--include-deleted"}, {"--include-system"}, {"--html"}} {
		args := append([]string{"--no-sync", "search", "Hello", "--source", "loop"}, flags...)
		code, _, stderr := e.run(args...)
		if code != 2 || !strings.Contains(stderr, CodeFlagSourceConflict) {
			t.Fatalf("--source loop accepted %v: %d %s", flags, code, stderr)
		}
	}
	for _, global := range [][]string{{"--account", "tenant/user"}, {"--outlook-profile", "Main"}} {
		args := append(append([]string{"--no-sync"}, global...), "search", "Hello", "--source", "loop")
		if code, _, stderr := e.run(args...); code != 2 || !strings.Contains(stderr, CodeFlagSourceConflict) {
			t.Fatalf("--source loop accepted %v: %s", global, stderr)
		}
		args = append(append([]string{"--no-sync"}, global...), "loop", "list")
		if code, _, _ := e.run(args...); code == 0 {
			t.Fatalf("loop list accepted %v", global)
		}
	}
	if code, _, _ := e.run("--no-sync", "search", "--source", "loop"); code == 0 {
		t.Fatal("blank Loop search accepted")
	}
	if code, _, _ := e.run("--no-sync", "loop", "search", "  "); code == 0 {
		t.Fatal("blank loop search accepted")
	}
	plain := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--limit", "500"))
	bounded := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--limit", "500", "--since", "2023-01-01"))
	if searchSourceCount(t, bounded, "loop") != nil || bounded["loop_coverage"] != nil {
		t.Fatalf("bounded all searched Loop: %v", bounded["sources"])
	}
	notices, _ := bounded["notices"].([]any)
	found := false
	for _, notice := range notices {
		found = found || strings.Contains(notice.(string), "Loop channels were not searched")
	}
	if !found {
		t.Fatalf("bounded all lacks the Loop exclusion notice: %v", notices)
	}
	legacy := func(doc map[string]any) int {
		n := 0
		for _, item := range items(t, doc) {
			if item["source"] != "loop" {
				n++
			}
		}
		return n
	}
	if legacy(plain) != legacy(bounded) {
		t.Fatalf("Loop participation changed legacy results: %d vs %d", legacy(plain), legacy(bounded))
	}
}
