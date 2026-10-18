package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

func overviewLoop(t *testing.T, e *env) map[string]any {
	t.Helper()
	doc := decode(t, runOK(t, e))
	for _, raw := range doc["sources"].([]any) {
		if source := raw.(map[string]any); source["source"] == "loop" {
			return source
		}
	}
	t.Fatalf("overview has no loop source: %v", doc["sources"])
	return nil
}

// The bare overview tells a cold agent what the archive holds of cached Loop channels and which
// command comes next, and tells nothing registered from registered failures.
func TestLoopOverviewRow(t *testing.T) {
	missing := newEnv(t)
	if row := overviewLoop(t, missing); row["state"] != "no_archive" {
		t.Fatalf("no archive: %v", row)
	}
	steps := runOK(t, missing, "--format", "text")
	if !strings.Contains(steps, "m365crawl loop search") || !strings.Contains(runOK(t, missing, "--help"), "m365crawl loop search") {
		t.Fatalf("Start here has no Loop line:\n%s", steps)
	}
	failed := newEnv(t)
	insertLoopCLI(t, loopRecords(t, failed), "failed", nil)
	runOK(t, failed, "loop", "sync")
	if row := overviewLoop(t, failed); row["state"] != "empty" || row["failed_sources"] != float64(1) || row["channels"] != float64(0) {
		t.Fatalf("registered without a channel: %v", row)
	}
	e := newEnv(t)
	db := loopRecords(t, e)
	if row := overviewLoop(t, e); row["state"] != "not_registered" || row["archived_records"] != float64(0) ||
		!strings.Contains(row["note"].(string), "m365crawl sync") {
		t.Fatalf("nothing archived: %v", row)
	}
	insertLoopCLI(t, db, "healthy", loopCLIValue(t, 1, [2]string{"a", "alpha words"}, [2]string{"b", "bravo words"}))
	insertLoopCLI(t, db, "failed", nil)
	insertLoopCLI(t, db, "metadata", loopCLIValue(t, 2))
	row := overviewLoop(t, e)
	if row["state"] != "not_registered" || row["archived_records"] != float64(3) || row["registered_sources"] != float64(0) ||
		!strings.Contains(row["note"].(string), "m365crawl loop sync") {
		t.Fatalf("archived but not registered: %v", row)
	}
	runOK(t, e, "loop", "sync")
	row = overviewLoop(t, e)
	note, _ := row["note"].(string)
	if row["state"] != "ok" || row["registered_sources"] != float64(3) || row["failed_sources"] != float64(1) ||
		row["metadata_only_sources"] != float64(1) || row["channels"] != float64(2) || row["last_sync_at"] == nil ||
		!strings.Contains(note, "m365crawl loop sources") || !strings.Contains(note, "1 source failed") || !strings.Contains(note, "metadata") {
		t.Fatalf("registered with a failure: %v", row)
	}
	text := runOK(t, e, "--format", "text")
	if !strings.Contains(text, "2 Loop channels (partial snapshots) from 3 sources; 1 failed") || !strings.Contains(text, "loop: ") {
		t.Fatalf("text overview:\n%s", text)
	}
	insertLoopCLI(t, db, "later", loopCLIValue(t, 3, [2]string{"c", "charlie words"}))
	if row := overviewLoop(t, e); !strings.Contains(row["note"].(string), "1 archived record is not registered: m365crawl loop sync registers new records") {
		t.Fatalf("new unregistered record not named: %v", row)
	}
	// Records that can never register leave no instruction that cannot be satisfied.
	insertLoopCLI(t, db, "bad\xff", nil)
	runOK(t, e, "loop", "sync")
	if note := overviewLoop(t, e)["note"].(string); strings.Contains(note, "not registered: run m365crawl loop sync") ||
		!strings.Contains(note, "1 archived record is not registered: m365crawl loop sync registers new records and names any it cannot register") {
		t.Fatalf("unregistrable record note: %s", note)
	}
	scoped := decode(t, runOK(t, e, "--account", "00000000-0000-0000-0000-000000000001/00000000-0000-0000-0000-000000000002"))
	for _, raw := range scoped["sources"].([]any) {
		if source := raw.(map[string]any); source["source"] == "loop" && !strings.Contains(source["note"].(string), "not narrowed by --account") {
			t.Fatalf("scoped overview: %v", source)
		}
	}
}

// loop sync never fails on a large backlog: it registers one batch, says more remain and how to
// continue, and exits 0.
func TestLoopSyncReportsRemainingDiscovery(t *testing.T) {
	e := newEnv(t)
	db := loopRecords(t, e)
	insertLoopCLI(t, db, "bad\xff", nil)
	for i := range 2*store.LoopDiscoveryBatch + 2 {
		insertLoopCLI(t, db, fmt.Sprintf("k%03d", i), nil)
	}
	doc := decode(t, runOK(t, e, "loop", "sync"))
	if doc["failed"] != float64(store.LoopDiscoveryBatch) || doc["discovery_remaining"] != true ||
		doc["discovery_remaining_records"] != float64(store.LoopDiscoveryBatch+2) ||
		doc["invalid_records"] != float64(1) {
		t.Fatalf("first batch: %v", doc)
	}
	assertRecoveryEnvCommand(t, e, doc["notice"], "this run registered 128 new Loop sources, the most one run takes; 130 archived records are still unregistered: run ", " again", "json", "loop", "sync")
	second := runOK(t, e, "--format", "text", "loop", "sync")
	var notice string
	for _, line := range strings.Split(second, "\n") {
		if strings.HasPrefix(line, "notice: ") {
			if notice != "" {
				t.Fatalf("repeated second-batch notice:\n%s", second)
			}
			notice = line
		}
	}
	assertRecoveryEnvCommand(t, e, notice, "notice: this run registered 128 new Loop sources, the most one run takes; 2 archived records are still unregistered: run ", " again", "text", "loop", "sync")
	text := runOK(t, e, "--format", "text", "loop", "sync")
	if strings.Contains(text, "notice: ") || !strings.Contains(text, "loop diagnostic: loop_cache_identity_unqualified: records rowid") {
		t.Fatalf("drained run:\n%s", text)
	}
	done := decode(t, runOK(t, e, "loop", "sync"))
	if done["discovery_remaining"] != false || done["notice"] != nil {
		t.Fatalf("drained run: %v", done)
	}
	help := runOK(t, e, "loop", "sync", "--help")
	for _, want := range []string{"128", "again", "records rowid"} {
		if !strings.Contains(help, want) {
			t.Fatalf("loop sync --help does not state %q:\n%s", want, help)
		}
	}
}

// A run that fails after it wrote prints what it committed and then the error.
func TestLoopSyncFailureAfterWritesReportsThem(t *testing.T) {
	e, _ := seedLoopFault(t)
	stubLoopSync(t, func(_ int, opts syncer.LoopOptions) (syncer.LoopReport, error) {
		report, err := syncer.SyncLoop(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		return report, errors.New("synthetic failure after a commit")
	})
	code, stdout, stderr := e.run("loop", "sync")
	if code != 1 || !strings.Contains(stderr, "db_error") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	doc := decode(t, stdout)
	if doc["unchanged"] != float64(1) || len(loopCoverageSources(t, doc)) != 1 {
		t.Fatalf("committed work not reported: %s", stdout)
	}
	stubLoopSync(t, func(int, syncer.LoopOptions) (syncer.LoopReport, error) {
		return syncer.LoopReport{}, errors.New("synthetic failure before any write")
	})
	if code, stdout, _ := e.run("loop", "sync"); code != 1 || stdout != "" {
		t.Fatalf("failure without writes printed a report: %d %s", code, stdout)
	}
}

// In shared search the Loop refresh runs after the implicit Teams sync, so it projects the records
// that sync just archived.
func TestLoopSearchRefreshesAfterImplicitSync(t *testing.T) {
	e := newEnv(t)
	synced := -1
	stubLoopSync(t, func(_ int, opts syncer.LoopOptions) (syncer.LoopReport, error) {
		if !opts.Probe {
			t.Error("the shared-search refresh read the sources a second time")
		}
		if opts.MaxAge > 0 && synced < 0 {
			synced = 0
			if db, err := sql.Open("sqlite", e.db); err == nil {
				_ = db.QueryRow(`select count(*) from sync_runs`).Scan(&synced)
				_ = db.Close()
			}
		}
		return syncer.LoopReport{}, nil
	})
	runOK(t, e, "search", "Hello")
	if synced <= 0 {
		t.Fatalf("Loop refresh ran before the implicit sync (sync runs seen: %d)", synced)
	}
}

// loop show in text carries the partial-snapshot evidence, with zone-suffixed clocks.
func TestLoopShowTextCarriesSnapshotEvidence(t *testing.T) {
	e := newEnv(t)
	db := loopRecords(t, e)
	partial := strings.Replace(loopCLIValue(t, 7, [2]string{"native", "evidence words"}),
		`"blobs":{},"trees":{"channels"`, `"blobs":{"orphan":"absent"},"trees":{"channels"`, 1)
	insertLoopCLI(t, db, "page", partial)
	runOK(t, e, "loop", "sync")
	id := items(t, decode(t, runOK(t, e, "--max-age", "0", "loop", "list")))[0]["id"].(string)
	text := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "show", id)
	zone := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC).In(displayZone).Format("2006-01-02 15:04 MST")
	for _, want := range []string{id + "  channels/native", "text state: partial_snapshot", "not a full or current Loop page",
		"sequence 7 (snapshot 7, latest 7)", "pending operations 0 (snapshot 0)", "markers 0", "missing blobs 1", "partial: yes",
		"losses: loop_cache_blob_missing 1", "latest cached snapshot of this source; archived record matches this snapshot: yes; archived record still in the app's cache: yes",
		"record updated at admission: " + zone, "projected: ", "raw updated: " + zone, "raw removed: -",
		"modified: unknown (the cache records no native modification time)", "association: unresolved", "\nevidence words\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text loop show lacks %q:\n%s", want, text)
		}
	}
	if search := runOK(t, e, "--max-age", "0", "--format", "text", "search", "evidence"); !strings.Contains(search, "partial_snapshot") || !strings.Contains(search, "  evidence words") || strings.Contains(search, "[partial_snapshot]") {
		t.Fatalf("shared search does not mark the Loop row as a partial snapshot:\n%s", search)
	}
	if _, err := db.Exec(`update records set value_json=?,content_hash='next',updated_at='2026-10-12T05:00:00.000Z' where key_json='page'`,
		loopCLIValue(t, 8, [2]string{"native", "later words"})); err != nil {
		t.Fatal(err)
	}
	runOK(t, e, "loop", "sync")
	old := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "show", id)
	for _, want := range []string{"older cached snapshot (a newer snapshot of this source exists); archived record matches this snapshot: no; archived record still in the app's cache: yes", "\nevidence words\n"} {
		if !strings.Contains(old, want) {
			t.Fatalf("retained text loop show lacks %q:\n%s", want, old)
		}
	}
	if list := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "list"); strings.Contains(list, "text state:") {
		t.Fatalf("loop list text is not compact:\n%s", list)
	}
	if fields := runOK(t, e, "--max-age", "0", "--format", "text", "--fields", "id,text", "loop", "show", id); strings.Contains(fields, "text state:") {
		t.Fatalf("--fields did not bound the detail:\n%s", fields)
	}
}

// Shared search carries compact Loop coverage: totals and a short row per source, never the tuple.
func TestLoopSearchCoverageIsCompact(t *testing.T) {
	e := newEnv(t)
	seedLoopSearch(t, e)
	for _, args := range [][]string{{"search", "Hello"}, {"search", "Hello", "--source", "loop"}, {"--fields", "source", "search", "Hello", "--limit", "1"}} {
		doc := decode(t, runOK(t, e, append([]string{"--max-age", "0"}, args...)...))
		coverage := doc["loop_coverage"].(map[string]any)
		totals, _ := coverage["totals"].(map[string]any)
		if totals == nil || totals["registered"] != float64(3) || totals["failed"] != float64(1) || totals["metadata_only"] != float64(1) ||
			totals["matches"] != float64(2) || totals["no_admission"] != float64(1) {
			t.Fatalf("%v totals: %v", args, coverage)
		}
		for _, source := range loopCoverageSources(t, doc) {
			keys := []string{}
			for key := range source {
				keys = append(keys, key)
			}
			words := attendeeCommandWords(t, source["inspect"].(string))
			if len(source) != 6 || recoveryValue(words, "--source-id") != fmt.Sprint(int64(source["source_id"].(float64))) ||
				recoveryValue(words, "--db") != e.db || recoveryValue(words, "--format") != "json" ||
				source["state"] == nil || source["snapshot_match"] == nil || source["raw_present"] == nil || source["reason"] == nil {
				t.Fatalf("%v compact source has keys %v: %v", args, keys, source)
			}
		}
		if strings.Contains(runOK(t, e, append([]string{"--max-age", "0"}, args...)...), "key_json") {
			t.Fatalf("%v search coverage carries the tuple", args)
		}
	}
	// loop commands keep the full detail.
	list := decode(t, runOK(t, e, "--max-age", "0", "loop", "list"))
	if source := loopCoverageSources(t, list)[0]; source["tuple"] == nil || source["retry"] == nil {
		t.Fatalf("loop list lost full coverage: %v", source)
	}
}

func TestLoopCoverageWording(t *testing.T) {
	empty := newEnv(t)
	if doc := decode(t, runOK(t, empty, "--max-age", "0", "loop", "list")); doc["loop_coverage"].(map[string]any)["state"] != "not_registered" {
		t.Fatalf("nothing registered: %v", doc["loop_coverage"])
	}
	for reason, fix := range loopFailureFix {
		rendered := (&runtime{}).loopCoverageOf([]store.LoopSource{{Registration: store.LoopRegistration{ID: 3},
			Health: store.KnowledgeSource{Status: "failed", Reason: reason}}}, nil).items[0].Fix
		if strings.Count(rendered, "retry") > 1 || strings.Contains(rendered, "{retry}") || fix == "" {
			t.Fatalf("%s fix repeats itself: %q", reason, rendered)
		}
	}
}

// An invalid current owner on a registered record is never printed; the fixed reason is.
func TestLoopInvalidOwnerNeverPrinted(t *testing.T) {
	e := newEnv(t)
	db := loopRecords(t, e)
	record := insertLoopCLI(t, db, "owned", loopCLIValue(t, 1, [2]string{"native", "owned words"}))
	runOK(t, e, "loop", "sync")
	if _, err := db.Exec(`update records set tenant_id=cast(x'ff5345435245' as text) where rowid=?`, record); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"loop", "sources"}, {"loop", "list"}, {"search", "owned"}, {"--format", "text", "loop", "sources"}} {
		out := runOK(t, e, append([]string{"--max-age", "0"}, args...)...)
		if strings.Contains(out, "SECRE") || !strings.Contains(out, "loop_cache_identity_unqualified") {
			t.Fatalf("%v: invalid owner printed or reason missing:\n%s", args, out)
		}
	}
	doc := decode(t, runOK(t, e, "loop", "sync"))
	if source := loopCoverageSources(t, doc)[0]; source["current_tenant_id"] != "" || source["reason"] != "loop_cache_identity_unqualified" ||
		source["status"] != "failed" {
		t.Fatalf("refresh of an invalid owner: %v", source)
	}
}
