package cli

import (
	"strings"
	"testing"
)

// A source that stopped reading at --limit reports a floor. Help says so, and text output names
// the sources whose count is a floor, once, even when every hit read is shown.
func TestMatchedAtLeastIsExplainedAsAFloor(t *testing.T) {
	e := loopHitsEnv(t)
	seedTwoDocuments(t, e)
	line := "documents stopped reading at --limit, so its match count (matched_at_least in JSON) is a floor, not a total; raise --limit to read further"
	text := runOK(t, e, "--max-age", "0", "--format", "text", "search", "Quarterly", "--source", "documents", "--limit", "1")
	if strings.Count(text, line) != 1 {
		t.Fatalf("text does not explain the floor once:\n%s", text)
	}
	if all := runOK(t, e, "--max-age", "0", "--format", "text", "search", "Quarterly", "--source", "documents"); strings.Contains(all, "a floor") {
		t.Fatalf("an exact count is called a floor:\n%s", all)
	}
	two := &searchSources{Mail: newSourceCount(1, 50, true, true, true), Notes: newSourceCount(5, 5, true, true, true),
		Loop: newSourceCount(2, 5, false, true, true), limit: 5}
	if got := two.floorNote(); got != "mail and notes stopped reading at --limit, so their match counts (matched_at_least in JSON) are floors, not totals; raise --limit to read further" {
		t.Fatalf("two floors: %q", got)
	}
	if (*searchSources)(nil).floorNote() != "" || (&searchSources{Loop: newSourceCount(2, 5, false, true, true)}).floorNote() != "" {
		t.Fatal("a result without a floor explains one")
	}
	if help := helpOf(t, "search"); !strings.Contains(help, "matched_at_least is a floor, not a total: that source stopped reading once it had --limit hits and may hold more") {
		t.Fatal("search --help does not explain matched_at_least as a floor")
	}
}

// loop list text says, once and only when it returns rows, that the rows are partial snapshots.
func TestLoopListTextSaysRowsArePartialSnapshots(t *testing.T) {
	e := loopRowsEnv(t)
	line := "partial snapshots of cached text, not full or current pages"
	if text := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "list"); strings.Count(text, line) != 1 {
		t.Fatalf("loop list text does not carry the line once:\n%s", text)
	}
	if text := runOK(t, e, "--max-age", "0", "--format", "text", "loop", "list", "--query", "nothingmatchesthis"); strings.Contains(text, line) {
		t.Fatalf("an empty loop list carries the line:\n%s", text)
	}
	if doc := decode(t, runOK(t, e, "--max-age", "0", "loop", "list")); strings.Contains(runOK(t, e, "--max-age", "0", "loop", "list"), line) || len(items(t, doc)) == 0 {
		t.Fatal("the line is text-only and the fixture returns rows")
	}
}

// app_summary.losses_total is the sum of the counts of app_observation.served.losses, and null
// like citations_total when nothing is served.
func TestAppSummaryLossesTotal(t *testing.T) {
	e := citeEnv(t)
	args := []string{"messages", "--conversation", appChat}
	full := byID(t, items(t, jsonRun(t, e, args...)), "app-1")["app_observation"].(map[string]any)["served"].(map[string]any)
	want := 0.0
	for _, loss := range full["losses"].([]any) {
		want += loss.(map[string]any)["count"].(float64)
	}
	rows := items(t, jsonRun(t, e, append([]string{"--fields", "id,app_summary"}, args...)...))
	summary := func(id string) map[string]any { return byID(t, rows, id)["app_summary"].(map[string]any) }
	if want == 0 || summary("app-1")["losses_total"] != want || summary("plain-1")["losses_total"] != float64(0) || summary("cite-only")["losses_total"] != float64(0) {
		t.Fatalf("losses_total: want %v, got %v %v %v", want, summary("app-1"), summary("plain-1"), summary("cite-only"))
	}
	unserved := (&applicationItem{State: "not_derived"}).summary()
	if unserved.LossesTotal != nil || unserved.CitationsTotal != nil {
		t.Fatalf("nothing served: %+v", unserved)
	}
	if cell := appSummaryCell([]byte(`{"state":"partial","app_name":null,"app_id":null,"reply_to_id":null,"citations_total":1,"losses_total":2}`)); cell != "partial; no app named; 1 citation; 2 native fields not read" {
		t.Fatalf("text cell: %q", cell)
	}
	if cell := appSummaryCell([]byte(`{"state":"mapped","app_name":null,"app_id":null,"reply_to_id":null,"citations_total":1,"losses_total":0}`)); cell != "mapped; no app named; 1 citation" {
		t.Fatalf("text cell without losses: %q", cell)
	}
	if help := helpOf(t, "messages"); !strings.Contains(help, "losses_total is the sum of the counts in served.losses") {
		t.Fatal("messages --help does not define losses_total")
	}
}
