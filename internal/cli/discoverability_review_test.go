package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/onenote"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// loopHitsEnv is the search fixture plus one Loop source whose three channels all say Hello. It
// syncs its own archive instead of copying the shared template: these tests open it for writing,
// and on Windows the store refuses an archive file it did not create.
func loopHitsEnv(t *testing.T) *env {
	t.Helper()
	e := searchEnv(t)
	searchSeed(t, e, searchMessages())
	seedSearchTranscripts(t, e)
	insertLoopCLI(t, loopRecords(t, e), "three", loopCLIValue(t, 5, [2]string{"one", "Hello one"}, [2]string{"two", "Hello two"}, [2]string{"three", "Hello three"}))
	runOK(t, e, "loop", "sync")
	return e
}

func textRun(t *testing.T, e *env, args ...string) string {
	t.Helper()
	return runOK(t, e, append([]string{"--max-age", "0", "--format", "text"}, args...)...)
}

// A hint never names the source already selected, and says to raise --limit when a source alone
// matched more than the limit.
func TestSearchHintsGiveAdviceThatHelps(t *testing.T) {
	e := loopHitsEnv(t)
	own := textRun(t, e, "search", "Hello", "--source", "loop", "--limit", "2")
	if strings.Contains(own, "use --source") || !strings.Contains(own, "1 Loop hit not shown; raise --limit\n") {
		t.Fatalf("--source loop --limit 2 gives circular advice:\n%s", own)
	}
	seedTeams(t, e, nil, manyHello(6)...)
	chats := textRun(t, e, "search", "Hello", "--source", "chats", "--limit", "5")
	if strings.Contains(chats, "use --source") || !strings.Contains(chats, " chat hits not shown; raise --limit\n") {
		t.Fatalf("--source chats --limit 5 gives circular advice:\n%s", chats)
	}
	mixed := textRun(t, e, "search", "Hello", "--limit", "5")
	for _, want := range []string{" chat hits not shown; use --source chats and raise --limit\n", "3 Loop hits not shown; use --source loop\n"} {
		if !strings.Contains(mixed, want) {
			t.Fatalf("mixed search lacks %q:\n%s", want, mixed)
		}
	}
	help := helpOf(t, "search")
	for _, want := range []string{"raise --limit", "never names the source already selected"} {
		if !strings.Contains(help, want) {
			t.Fatalf("search --help lacks %q", want)
		}
	}
}

func manyHello(n int) []teamsdesktop.Message {
	out := make([]teamsdesktop.Message, n)
	for i := range out {
		out[i] = appMsg(appChat, fmt.Sprintf("hello-%d", i), "Hello again", `{"id":"x"}`, appT0.Add(time.Duration(i)*time.Minute))
	}
	return out
}

// Loop matched is counted, not the rows read; a source that stops at the limit never claims an
// exact number.
func TestSearchMatchedIsExactOrALowerBound(t *testing.T) {
	e := loopHitsEnv(t)
	loop := searchSourceCount(t, decode(t, runOK(t, e, "--max-age", "0", "search", "Hello", "--source", "loop", "--limit", "2")), "loop")
	if loop["returned"] != float64(2) || loop["matched"] != float64(3) || loop["matched_at_least"] != nil || loop["truncated"] != true {
		t.Fatalf("Loop cut by the limit: %v", loop)
	}
	seedTwoNotes(t, e)
	seedTwoDocuments(t, e)
	for source, query := range map[string]string{"mail": "Hello", "transcripts": "Hello", "notes": "Quarterly", "documents": "Quarterly"} {
		cut := searchSourceCount(t, decode(t, runOK(t, e, "--max-age", "0", "search", query, "--source", source, "--limit", "1")), source)
		if _, exact := cut["matched"]; exact || cut["matched_at_least"] != float64(1) || cut["returned"] != float64(1) || cut["truncated"] != true {
			t.Fatalf("%s cut at the limit claims an exact count: %v", source, cut)
		}
		all := searchSourceCount(t, decode(t, runOK(t, e, "--max-age", "0", "search", query, "--source", source, "--limit", "500")), source)
		if _, bound := all["matched_at_least"]; bound || all["matched"] != all["returned"] || all["returned"].(float64) < 2 {
			t.Fatalf("%s read whole is not exact: %v", source, all)
		}
	}
}

func seedTwoNotes(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	attempt, err := st.BeginKnowledgeAttempt(ctx, store.KnowledgeInput{Kind: "notes", Platform: "synthetic", Locator: "/fictional/two.db", AcquisitionSignature: "metadata", At: at})
	if err != nil {
		t.Fatal(err)
	}
	err = st.CommitNotes(ctx, attempt, store.KnowledgeOutcome{Status: "complete", AcquisitionSignature: "metadata", ContentDigest: "bytes",
		Qualification: store.NotesQualification, At: at, SourceObservedAt: at}, onenote.Result{Entities: []onenote.Entity{
		{RowID: 4, Type: 4, GOID: "notebook", GUID: "guid-notebook", Title: "Synthetic notebook"},
		{RowID: 1, Type: 1, GOID: "page-1", GUID: "guid-page-1", ParentGOID: "notebook", Title: "Quarterly plan one", ModifiedAt: at},
		{RowID: 2, Type: 1, GOID: "page-2", GUID: "guid-page-2", ParentGOID: "notebook", Title: "Quarterly plan two", ModifiedAt: at.Add(time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func seedTwoDocuments(t *testing.T, e *env) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	at := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	a, err := st.BeginDocumentAttempt(ctx, store.KnowledgeInput{Kind: "office_shared", Platform: "synthetic", Locator: "/fictional/two.json", At: at}, officedocuments.Shared, "supplied")
	if err != nil {
		t.Fatal(err)
	}
	doc := func(i int, title string) officedocuments.Document {
		return officedocuments.Document{Ordinal: i, Surface: officedocuments.Shared, Title: title, URL: fmt.Sprintf("https://example.invalid/plan-%d", i),
			Modified: officedocuments.Timestamp{Raw: "2026-10-11T01:00:00Z", Value: at.Add(time.Duration(i) * time.Hour)}}
	}
	err = st.CommitDocumentObservations(ctx, a, store.KnowledgeOutcome{Status: "complete", ContentDigest: "synthetic", Qualification: store.DocumentQualification, At: at},
		officedocuments.Result{Documents: []officedocuments.Document{doc(0, "Quarterly plan one"), doc(1, "Quarterly plan two")}},
		store.DocumentCaptureMetadata{Surface: officedocuments.Shared, Application: "supplied", AcquiredAt: &at, SourceObservedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
}

// With no archive nothing was searched, so no source reports a matched count.
func TestSearchWithoutAnArchiveReportsNoMatchedCount(t *testing.T) {
	e := newEnv(t)
	doc := decode(t, runOK(t, e, "--max-age", "0", "search", "Hello"))
	sources := doc["sources"].(map[string]any)
	if len(sources) == 0 || doc["needs_sync"] != true {
		t.Fatalf("fixture: %v", doc)
	}
	for name, raw := range sources {
		count := raw.(map[string]any)
		_, exact := count["matched"]
		_, bound := count["matched_at_least"]
		if exact || bound || count["returned"] != float64(0) || count["count"] != float64(0) {
			t.Fatalf("%s was never searched and reports a matched count: %v", name, count)
		}
	}
	if help := helpOf(t, "search"); !strings.Contains(help, "With no archive nothing is searched, and neither is present") {
		t.Fatal("search --help does not say when matched is absent")
	}
	if (&searchSources{Chats: newSourceCount(0, 0, false, false, false)}).hints() != nil {
		t.Fatal("a source that was not searched has no hint")
	}
}

// Help that offers --fields app_summary is help of a command that accepts it.
func TestHelpMentionsAppSummaryOnlyWhereItIsAccepted(t *testing.T) {
	mentioned := 0
	for cmd := range commandFields {
		help := helpOf(t, strings.Fields(cmd)...)
		accepts := contains(fieldsOf(cmd, ""), appSummaryField)
		if strings.Contains(help, "app_summary") != accepts {
			t.Fatalf("%s --help mentions app_summary: %v; accepts it: %v", cmd, !accepts, accepts)
		}
		if accepts {
			mentioned++
			if code, _, stderr := appEnv(t).run(append([]string{"--max-age", "0", "--fields", "app_summary"}, append(strings.Fields(cmd), summaryArgs[cmd]...)...)...); code != 0 {
				t.Fatalf("%s refuses the key its help offers: %s", cmd, stderr)
			}
		}
	}
	if mentioned != 4 {
		t.Fatalf("%d commands accept app_summary", mentioned)
	}
	if code, _, stderr := appEnv(t).run("--max-age", "0", "--fields", "app_summary", "calendar", "event", "x"); code != 2 || !strings.Contains(stderr, "app_summary") {
		t.Fatalf("calendar event: %d %s", code, stderr)
	}
}

var summaryArgs = map[string][]string{"messages": {"--conversation", appChat}, "unread": nil, "thread": {appChat, "app-1"}, "search": {"evidence"}}

// citations_total is every citation read from the message, not the at most 100 an item shows, and
// the served claims are null, not zero, when nothing is served.
func TestAppSummaryCitationsTotalAndNulls(t *testing.T) {
	e := appEnv(t)
	var cits []string
	for i := 1; i <= appCitationPreview+1; i++ {
		cits = append(cits, fmt.Sprintf(`{"id":%d,"title":"Source %d"}`, i, i))
	}
	seedTeams(t, e, nil, appMsg(appChat, "many", "evidence many", `{"properties":{"botCitations":[`+strings.Join(cits, ",")+`]}}`, appT0.Add(4*time.Minute)),
		appMsg(appChat, "bad-1", "evidence broken", `{"properties":{},"properties":{}}`, appT0.Add(5*time.Minute)))
	e.exec(`delete from message_app_current where message_rowid=(select rowid from messages where id='plain-1')`)
	full := jsonRun(t, e, "messages", "--conversation", appChat)
	served := byID(t, items(t, full), "many")["app_observation"].(map[string]any)["served"].(map[string]any)
	if served["citations_count"] != float64(appCitationPreview) || served["total_admitted"] != float64(appCitationPreview+1) {
		t.Fatalf("fixture must show fewer citations than it read: %v %v", served["citations_count"], served["total_admitted"])
	}
	m := jsonRun(t, e, "--fields", "id,app_summary", "messages", "--conversation", appChat)
	summary := func(id string) map[string]any { return byID(t, items(t, m), id)["app_summary"].(map[string]any) }
	many := summary("many")
	if _, old := many["citations_count"]; old || many["citations_total"] != float64(appCitationPreview+1) {
		t.Fatalf("citations_total is not every citation read: %v", many)
	}
	for id, state := range map[string]string{"plain-1": "not_derived", "bad-1": "failed"} {
		got := summary(id)
		total, has := got["citations_total"]
		if got["state"] != state || !has || total != nil || got["app_id"] != nil || got["app_name"] != nil || got["reply_to_id"] != nil || len(got) != 5 {
			t.Fatalf("%s serves nothing, so its claims are unknown (null): %v", id, got)
		}
	}
	if help := helpOf(t, "messages"); !strings.Contains(help, "citations_total is every citation read from the message (served.total_admitted), not the at most 100 an item shows") ||
		!strings.Contains(help, "null when nothing is served") {
		t.Fatal("messages --help does not define citations_total")
	}
}

func TestAppSummaryTextCellIsReadable(t *testing.T) {
	e := citeEnv(t)
	out := textRun(t, e, "--fields", "id,app_summary", "messages", "--conversation", appChat)
	if strings.Contains(out, "map[") || !strings.Contains(out, `partial; app "Synthetic Helper" (id "app-synthetic"); reply to "native-reply-1"; 2 citations`) ||
		!strings.Contains(out, "mapped; no app named; 1 citation") || !strings.Contains(out, "absent; no app named; 0 citations") {
		t.Fatalf("text app_summary:\n%s", out)
	}
	if got := appSummaryCell([]byte(`{"state":"not_derived","app_name":null,"app_id":null,"reply_to_id":null,"citations_total":null}`)); got != "not_derived; nothing served" {
		t.Fatalf("nothing served: %q", got)
	}
	if got := appSummaryCell([]byte(`{"state":"mapped","app_name":null,"app_id":"only-id","reply_to_id":null,"citations_total":0}`)); got != `mapped; app id "only-id"; 0 citations` {
		t.Fatalf("id only: %q", got)
	}
	if got := appSummaryCell([]byte(`{"state":"mapped","app_name":"Named Only","app_id":null,"reply_to_id":null,"citations_total":1}`)); got != `mapped; app "Named Only"; 1 citation` {
		t.Fatalf("name only: %q", got)
	}
	if got := appSummaryCell([]byte(`[`)); got != "" {
		t.Fatalf("unreadable: %q", got)
	}
}

// posted_by_app counts what the text marker marks: a message that names an app by id or by name.
func TestPostedByAppCountsNamedAppsAndSaysWhenItIsALowerBound(t *testing.T) {
	e := appEnv(t)
	seedTeams(t, e, nil,
		appMsg(appChat, "name-only", "evidence name", `{"properties":{"fromAppMetadata":{"name":"Named Only"}}}`, appT0.Add(2*time.Minute)),
		appMsg(appChat, "empty-id", "evidence empty", `{"properties":{"fromAppMetadata":{"id":""},"botCitations":[{"id":1,"title":"A"}]}}`, appT0.Add(3*time.Minute)))
	c := coverageOf(t, jsonRun(t, e, "messages", "--conversation", appChat))
	if c["posted_by_app"] != float64(2) || strings.Contains(c["note"].(string), "lower bound") ||
		!strings.Contains(c["note"].(string), "posted_by_app counts the returned messages whose served evidence names an app by app_id or app_name") {
		t.Fatalf("coverage: %v", c)
	}
	text := textRun(t, e, "messages", "--conversation", appChat)
	if strings.Count(text, "[app: ") != 2 || !strings.Contains(text, "\nposted by an app (names an app): 2 of 4\n") {
		t.Fatalf("the count and the markers disagree:\n%s", text)
	}
	e.exec(`delete from message_app_current where message_rowid=(select rowid from messages where id='plain-1')`)
	seedTeams(t, e, nil, appMsg(appChat, "bad-1", "evidence broken", `{"properties":{},"properties":{}}`, appT0.Add(5*time.Minute)))
	c = coverageOf(t, jsonRun(t, e, "messages", "--conversation", appChat))
	if c["posted_by_app"] != float64(2) || !strings.Contains(c["note"].(string), "posted_by_app is a lower bound: 2 returned messages are not_derived or failed with nothing served") {
		t.Fatalf("lower bound not said: %v", c)
	}
	text = textRun(t, e, "messages", "--conversation", appChat)
	if !strings.Contains(text, "\nposted by an app (names an app): at least 2 of 5\n2 not derived or failed, so whether they name an app is unknown\n") {
		t.Fatalf("text does not say the count is a lower bound:\n%s", text)
	}
	if help := helpOf(t, "messages"); !strings.Contains(help, "names an app by app_id or app_name") || !strings.Contains(help, "lower bound") {
		t.Fatal("messages --help does not define posted_by_app")
	}
}

// A retained message says why its current revision failed.
func TestRetainedFailureMeaningNamesItsCode(t *testing.T) {
	e := appEnv(t)
	failed := appMsg(appChat, "app-1", "evidence answer", `{"properties":{},"properties":{}}`, appT0)
	failed.Version = 2
	seedTeams(t, e, nil, failed)
	app := byID(t, items(t, jsonRun(t, e, "messages", "--conversation", appChat)), "app-1")["app_observation"].(map[string]any)
	meaning := app["meaning"].(string)
	if app["retained"] != true || app["failure_code"] != "teams_app_raw_invalid" || !strings.Contains(meaning, "served is the last good observation") ||
		!strings.HasSuffix(meaning, "; teams_app_raw_invalid: "+appFailureMeanings["teams_app_raw_invalid"]) {
		t.Fatalf("retained meaning: %v", app)
	}
	if !strings.Contains(appFailureMeanings["teams_app_raw_invalid"], "repeats a key") {
		t.Fatalf("the meaning does not name a repeated key, which this message has: %q", appFailureMeanings["teams_app_raw_invalid"])
	}
}

func TestAppCodePatternReadsCodesWithDigits(t *testing.T) {
	if m := appCodePattern.FindStringSubmatch(`return "teams_app_raw_v2_odd"`); m == nil || m[1] != "teams_app_raw_v2_odd" {
		t.Fatalf("a code with a digit is not read whole: %v", m)
	}
}

// The marker is bounded by display width and a cell uses one ellipsis.
func TestAppMarkerIsBoundedByDisplayWidth(t *testing.T) {
	wide := strings.Repeat("語", 60)
	it := applicationItem{Served: &applicationServed{applicationPayloadItem: &applicationPayloadItem{AppName: &wide}}}
	marker := appMarker(&it)
	if w := runewidth.StringWidth(marker); w > len("[app: ] ")+appMarkerName || !strings.HasSuffix(marker, "...] ") {
		t.Fatalf("marker is %d columns: %q", w, marker)
	}
	clipped := "Synth…" // an app name --max-text already clipped
	it.Served.AppName, it.Served.AppNameTruncated = &clipped, true
	if marker := appMarker(&it); marker != "[app: Synth...] " {
		t.Fatalf("two ellipsis styles in one cell: %q", marker)
	}
	e := appEnv(t)
	seedTeams(t, e, nil, appMsg(appChat, "wide", "evidence wide "+strings.Repeat("word ", 40), `{"properties":{"fromAppMetadata":{"id":"w","name":"`+wide+`"}}}`, appT0.Add(2*time.Minute)))
	t.Setenv("COLUMNS", "80")
	for _, line := range strings.Split(textRun(t, e, "messages", "--conversation", appChat), "\n") {
		if strings.Contains(line, "[app: 語") && (strings.Contains(line, "…") || !strings.HasSuffix(line, "...")) {
			t.Fatalf("row mixes ellipsis styles: %q", line)
		}
	}
}

func TestRetryNeededOnlyWhereARetryCanHelp(t *testing.T) {
	e := loopRowsEnv(t)
	for _, source := range items(t, decode(t, runOK(t, e, "--max-age", "0", "loop", "sources"))) {
		key := source["tuple"].(map[string]any)["key_json"].(string)
		fix, _ := source["fix"].(string)
		switch key {
		case "failed":
			if source["retry_needed"] != true {
				t.Fatalf("a failed source needs a retry: %v", source)
			}
		case "metadata":
			if source["retry_needed"] != false || !strings.Contains(fix, "a retry now changes nothing") {
				t.Fatalf("a metadata-only source cannot be helped by a retry now: %v / %q", source["retry_needed"], fix)
			}
		default:
			if source["retry_needed"] != false {
				t.Fatalf("a healthy source needs no retry: %v", source)
			}
		}
	}
	text := textRun(t, e, "loop", "sources")
	if strings.Count(text, "m365crawl loop sync --source-id") != 1+2 { // one table cell and two fix lines
		t.Fatalf("text shows a retry where it cannot help:\n%s", text)
	}
	for _, cmd := range [][]string{{"loop"}, {"loop", "sync"}} {
		if help := helpOf(t, cmd...); !strings.Contains(help, "retry_needed is true on a failed or cancelled source") || !strings.Contains(help, "metadata-only source is false") {
			t.Fatalf("%v --help does not say when retry_needed is true", cmd)
		}
	}
}

func TestSQLHelpIsCorrectAndFitsTheHelpWidth(t *testing.T) {
	e := appEnv(t)
	help := runOK(t, e, "sql", "--help")
	if !strings.Contains(help, fmt.Sprintf("Tables (%d", len(sqlTables))) || !strings.Contains(strings.Join(strings.Fields(help), " "), "sender_id, sender_name, sent_at, content_text") {
		t.Fatalf("sql --help table list:\n%s", help)
	}
	_, tables, _ := strings.Cut(help, "\nTables (")
	tables, _, _ = strings.Cut(tables, "\nColumns of a table")
	for _, line := range strings.Split(tables, "\n") {
		if len(line) > helpWidth {
			t.Fatalf("table line is %d columns: %q", len(line), line)
		}
	}
	// Every column the messages line names exists, and the printed column query shows every column of the widest table.
	const query = `select name, type from pragma_table_info('TABLE')`
	if !strings.Contains(help, `m365crawl sql "`+strings.Replace(query, "TABLE", "messages", 1)+`" --limit 200`) {
		t.Fatalf("sql --help prints no column query with a limit that fits:\n%s", help)
	}
	have := map[string]bool{}
	for _, row := range jsonRun(t, e, "sql", strings.Replace(query, "TABLE", "messages", 1), "--limit", "200")["rows"].([]any) {
		have[row.([]any)[0].(string)] = true
	}
	for _, col := range []string{"sender_id", "sender_name", "sent_at", "content_text", "raw_json", "raw_digest"} {
		if !have[col] {
			t.Fatalf("messages has no column %s", col)
		}
	}
	for _, table := range sqlTables {
		cols := jsonRun(t, e, "sql", strings.Replace(query, "TABLE", table.Name, 1), "--limit", "200")
		if cols["truncated"] != false {
			t.Fatalf("the printed column query cuts %s at --limit 200", table.Name)
		}
	}
}

func TestOverviewTextNoteUsesPlainWords(t *testing.T) {
	e := loopRowsEnv(t)
	text := runOK(t, e, "--format", "text")
	if strings.Contains(text, "last_sync_at") || strings.Contains(text, "last_projected_at") ||
		!strings.Contains(text, "the time shown is the last Loop projection, not an edit time") {
		t.Fatalf("text overview note names JSON fields:\n%s", text)
	}
	if note, _ := overviewLoop(t, e)["note"].(string); !strings.Contains(note, "last_sync_at is the last Loop projection (last_projected_at)") {
		t.Fatalf("JSON note lost its field names: %s", note)
	}
}
