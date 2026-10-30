package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/botcitations"
)

// These tests hold what a cold agent, with --help and command output only, must be able to read off
// application evidence and Loop results. Every fixture is synthetic.

const appCiteOnlyRaw = `{"properties":{"botCitations":[{"id":1,"title":"Only a citation"}]}}`

// citeEnv is appEnv plus one message whose claims were all read (mapped) and that names no app.
func citeEnv(t *testing.T) *env {
	t.Helper()
	e := appEnv(t)
	seedTeams(t, e, nil, appMsg(appChat, "cite-only", "evidence cited", appCiteOnlyRaw, appT0.Add(2*time.Minute)))
	return e
}

func helpOf(t *testing.T, args ...string) string {
	t.Helper()
	return strings.Join(strings.Fields(runOK(t, newEnv(t), append(args, "--help")...)), " ") // kong wraps help: compare words
}

// A result says how many returned messages name an app, so mapped is never read as "posted by a bot".
func TestApplicationCoverageCountsPostedByApp(t *testing.T) {
	e := citeEnv(t)
	m := jsonRun(t, e, "messages", "--conversation", appChat)
	c := coverageOf(t, m)
	if c["posted_by_app"] != float64(1) || states(c) != "mapped=1 partial=1 absent=1 failed=0 not_derived=0" ||
		!strings.Contains(c["note"].(string), "posted_by_app counts the returned messages whose served evidence names an app by app_id or app_name") {
		t.Fatalf("coverage: %v", c)
	}
	if none := coverageOf(t, jsonRun(t, e, "search", "zzzznomatch", "--source", "chats")); none["posted_by_app"] != float64(0) {
		t.Fatalf("zero hits: %v", none)
	}
	_, text, _ := e.run("--no-sync", "--format", "text", "messages", "--conversation", appChat)
	if !strings.Contains(text, "1 mapped, 1 partial, 1 absent\nposted by an app (names an app): 1 of 3\n") {
		t.Fatalf("text coverage line:\n%s", text)
	}
	for _, cmd := range [][]string{{"messages"}, {"unread"}, {"thread"}, {"search"}, {"calendar", "event"}} {
		help := helpOf(t, cmd...)
		for _, want := range []string{"posted_by_app", "mapped means the claims were read, not that an app or bot posted the message"} {
			if !strings.Contains(help, want) {
				t.Fatalf("%v --help lacks %q", cmd, want)
			}
		}
	}
}

func TestGlobalHelpExplainsLogFormat(t *testing.T) {
	help := helpOf(t)
	for _, want := range []string{"label={json}", "docs_list={...}"} {
		if !strings.Contains(help, want) {
			t.Fatalf("--help does not explain --format log (%q):\n%s", want, help)
		}
	}
	_, out, _ := appEnv(t).run("--no-sync", "--format", "log", "docs", "list")
	if !strings.HasPrefix(out, "docs_list={") || strings.Count(out, "\n") != 1 {
		t.Fatalf("log output is not the one line the help describes:\n%s", out)
	}
}

// liveTables are the tables of a fresh archive, without SQLite's own and the FTS5 shadow tables.
func liveTables(t *testing.T, e *env) []string {
	t.Helper()
	m := jsonRun(t, e, "sql", "select name from sqlite_master where type='table' and name not like 'sqlite_%' order by name", "--limit", "1000")
	var all, out []string
	for _, row := range m["rows"].([]any) {
		all = append(all, row.([]any)[0].(string))
	}
	for _, name := range all {
		shadow := false
		for _, fts := range all {
			shadow = shadow || (strings.HasSuffix(fts, "_fts") && strings.HasPrefix(name, fts+"_"))
		}
		if !shadow {
			out = append(out, name)
		}
	}
	return out
}

// sql --help names every table of the archive, so a table cannot be added without a line of help.
func TestSQLHelpListsEveryTable(t *testing.T) {
	e := appEnv(t)
	help := runOK(t, e, "sql", "--help")
	live := liveTables(t, e)
	var documented []string
	for _, table := range sqlTables {
		documented = append(documented, table.Name)
		if table.Holds == "" || !strings.Contains(help, "  "+table.Name+"  ") {
			t.Fatalf("sql --help does not describe table %s:\n%s", table.Name, help)
		}
	}
	slices.Sort(documented)
	if !slices.Equal(documented, live) {
		t.Fatalf("sqlTables must name exactly the archive's tables:\n documented %v\n live       %v", documented, live)
	}
	for _, want := range []string{"pragma_table_info('messages')", "sqlite_master", "SHA-256", "lowercase hex", "raw_digest", "read-only"} {
		if !strings.Contains(help, want) {
			t.Fatalf("sql --help lacks %q:\n%s", want, help)
		}
	}
	// The discovery queries the help prints run as printed.
	cols := jsonRun(t, e, "sql", "select name, type from pragma_table_info('messages')", "--limit", "200")
	found := false
	for _, row := range cols["rows"].([]any) {
		found = found || row.([]any)[0] == "raw_digest"
	}
	if !found {
		t.Fatalf("pragma_table_info('messages') lists no raw_digest: %v", cols["rows"])
	}
}

var appCodePattern = regexp.MustCompile(`"(teams_(?:bot|app)_[a-z0-9_]+)"`)

// Every application-evidence code the source can emit has a one-line explanation in the help.
func TestEveryApplicationCodeIsExplained(t *testing.T) {
	emitted := map[string]bool{}
	for _, glob := range []string{"../botcitations/*.go", "../teamsdesktop/app_observation.go", "../store/message_apps*.go"} {
		files, err := filepath.Glob(glob)
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v %v", glob, files, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			src, err := os.ReadFile(file) //nolint:gosec // G304: this repository's own source
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range appCodePattern.FindAllStringSubmatch(string(src), -1) {
				emitted[match[1]] = true
			}
		}
	}
	explained := map[string]bool{}
	help := helpOf(t, "messages")
	for code, meaning := range botcitations.LossMeanings {
		explained[code] = true
		if meaning == "" || !strings.Contains(help, code+": "+meaning) {
			t.Fatalf("messages --help does not explain loss code %s", code)
		}
	}
	for code, meaning := range appFailureMeanings {
		explained[code] = true
		if meaning == "" || !strings.Contains(help, code+": "+meaning) {
			t.Fatalf("messages --help does not explain failure code %s", code)
		}
	}
	for code := range emitted {
		if !explained[code] {
			t.Fatalf("code %s can be emitted and has no explanation: add it to botcitations.LossMeanings or appFailureMeanings", code)
		}
	}
	for code := range explained {
		if !emitted[code] {
			t.Fatalf("code %s is explained and never emitted: remove it", code)
		}
	}
	if len(botcitations.LossMeanings) < 6 || len(appFailureMeanings) < 6 {
		t.Fatalf("tables are incomplete: %d loss, %d failure", len(botcitations.LossMeanings), len(appFailureMeanings))
	}
}

func TestApplicationLossesAreExplainedPerMessage(t *testing.T) {
	e := appEnv(t)
	m := jsonRun(t, e, "messages", "--conversation", appChat)
	app := byID(t, items(t, m), "app-1")["app_observation"].(map[string]any)
	losses := app["served"].(map[string]any)["losses"].([]any)
	loss := losses[0].(map[string]any)
	want := botcitations.LossMeanings["teams_bot_citation_unmapped"]
	if len(losses) != 1 || loss["code"] != "teams_bot_citation_unmapped" || loss["count"] != float64(1) || loss["meaning"] != want ||
		!strings.Contains(app["meaning"].(string), "teams_bot_citation_unmapped: "+want) {
		t.Fatalf("loss not explained: %v / %v", losses, app["meaning"])
	}
	_, text, _ := e.run("--no-sync", "--format", "text", "messages", "--conversation", appChat)
	if !strings.Contains(text, "not read: teams_bot_citation_unmapped x1 ("+want+")") {
		t.Fatalf("text does not explain the loss:\n%s", text)
	}
	bad := appMsg(appChat, "bad-1", "evidence broken", `{"properties":{},"properties":{}}`, appT0.Add(3*time.Minute))
	seedTeams(t, e, nil, bad)
	failed := byID(t, items(t, jsonRun(t, e, "messages", "--conversation", appChat)), "bad-1")["app_observation"].(map[string]any)
	if !strings.Contains(failed["meaning"].(string), "teams_app_raw_invalid: "+appFailureMeanings["teams_app_raw_invalid"]) {
		t.Fatalf("failure not explained: %v", failed["meaning"])
	}
	// A code a newer version stored has no line here: nothing is invented for it.
	if unknown := lossMeaning("teams_bot_future_code"); unknown != "" || explained("text", "teams_bot_future_code", unknown) != "text" {
		t.Fatalf("unknown code invented a meaning: %q", unknown)
	}
}

// full_inspection prints the stored projection, a JSON string, and what the read serves is that
// projection verbatim: a citation link is null in both or the same string in both.
func TestServedCitationsAreTheStoredProjection(t *testing.T) {
	e := appEnv(t)
	m := jsonRun(t, e, "messages", "--conversation", appChat)
	item := byID(t, items(t, m), "app-1")
	served := item["app_observation"].(map[string]any)["served"].(map[string]any)
	id := strings.TrimPrefix(served["observation_id"].(string), "message-app-observation:")
	if !strings.HasSuffix(served["full_inspection"].(string), `where rowid=`+id+`" --limit 1`) {
		t.Fatalf("full_inspection does not name the served row: %v", served["full_inspection"])
	}
	row := jsonRun(t, e, "sql", "select projection_json from message_app_observations where rowid="+id, "--limit", "1")
	text, isString := row["rows"].([]any)[0].([]any)[0].(string)
	if !isString {
		t.Fatalf("projection_json is not a JSON string: %v", row["rows"])
	}
	var stored struct {
		Citations []map[string]any `json:"citations"`
	}
	if err := json.Unmarshal([]byte(text), &stored); err != nil { // the second decode the help describes
		t.Fatal(err)
	}
	cits := served["citations"].([]any)
	if len(cits) != 2 || len(stored.Citations) != 2 {
		t.Fatalf("citations: %v / %v", cits, stored.Citations)
	}
	for i, raw := range cits {
		got, want := raw.(map[string]any), stored.Citations[i]
		for _, key := range []string{"position", "id", "title", "link", "content_type", "nested_content_present"} {
			if got[key] != want[key] {
				t.Fatalf("citation %d %s: served %v, stored %v", i, key, got[key], want[key])
			}
		}
	}
	if cits[0].(map[string]any)["link"] == nil || cits[1].(map[string]any)["link"] != nil {
		t.Fatalf("fixture must hold a linked and an unlinked citation: %v", cits)
	}
	help := helpOf(t, "messages")
	for _, want := range []string{"JSON string", "decode it a second time", "never updated or deleted", "not the raw message", "messages.raw_json",
		"citation's link is the stored value, null when the message gave none", "the item's own link is the Teams link to the message"} {
		if !strings.Contains(help, want) {
			t.Fatalf("messages --help lacks %q", want)
		}
	}
	_, out, _ := e.run("--no-sync", "--format", "text", "messages", "--conversation", appChat)
	if !strings.Contains(out, "full record (the stored projection as a JSON string to decode again; not the raw message): m365crawl --db ") {
		t.Fatalf("text does not say what the full record is:\n%s", out)
	}
}

func TestApplicationTextIsPlainAndKeepsTheMarker(t *testing.T) {
	e := appEnv(t)
	t.Setenv("COLUMNS", "60")
	_, out, _ := e.run("--no-sync", "--format", "text", "messages", "--conversation", appChat)
	for _, want := range []string{"[app: Synthetic Helper] ", "citations: 2 read from the message, 2 shown; 1 more could not be read"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
	for _, jargon := range []string{"admitted shown", "source rows refused"} {
		if strings.Contains(out, jargon) {
			t.Fatalf("text still says %q:\n%s", jargon, out)
		}
	}
	long := strings.Repeat("N", 80)
	name := &long
	it := applicationItem{Served: &applicationServed{applicationPayloadItem: &applicationPayloadItem{AppName: name}}}
	if marker := appMarker(&it); marker != "[app: "+strings.Repeat("N", appMarkerName-3)+"...] " {
		t.Fatalf("a long app name is not bounded inside the marker: %q", marker)
	}
	help := helpOf(t, "messages")
	for _, want := range []string{"total_admitted", "refused_citation_rows counts the citation rows of the message that could not be read"} {
		if !strings.Contains(help, want) {
			t.Fatalf("messages --help lacks %q", want)
		}
	}
}

func TestFieldsAppSummary(t *testing.T) {
	e := citeEnv(t)
	routes := [][]string{{"messages", "--conversation", appChat}, {"unread", "--conversation", appChat}, {"thread", appChat, "app-1"}, {"search", "evidence", "--source", "chats"}}
	for _, args := range routes {
		m := jsonRun(t, e, append([]string{"--fields", "id,app_summary"}, args...)...)
		got := byID(t, items(t, m), "app-1")
		summary := got["app_summary"].(map[string]any)
		if len(got) != 2 || len(summary) != 6 || summary["state"] != "partial" || summary["app_name"] != "Synthetic Helper" || summary["app_id"] != "app-synthetic" ||
			summary["reply_to_id"] != "native-reply-1" || summary["citations_total"] != float64(2) {
			t.Fatalf("%v app_summary: %v", args, got)
		}
		if plain := jsonRun(t, e, args...); byID(t, items(t, plain), "app-1")["app_summary"] != nil {
			t.Fatalf("%v carries app_summary without --fields", args)
		}
	}
	m := jsonRun(t, e, "--fields", "id,app_summary", "--max-text", "5", "messages", "--conversation", appChat)
	clipped := byID(t, items(t, m), "app-1")["app_summary"].(map[string]any)
	absent := byID(t, items(t, m), "plain-1")["app_summary"].(map[string]any)
	cited := byID(t, items(t, m), "cite-only")["app_summary"].(map[string]any)
	if clipped["app_name"] != "Synt…" || absent["state"] != "absent" || absent["app_id"] != nil || absent["app_name"] != nil || absent["reply_to_id"] != nil ||
		absent["citations_total"] != float64(0) || cited["state"] != "mapped" || cited["app_id"] != nil || cited["citations_total"] != float64(1) {
		t.Fatalf("summaries: %v %v %v", clipped, absent, cited)
	}
	for _, cmd := range []string{"messages", "unread", "thread", "search"} {
		if help := helpOf(t, cmd); !strings.Contains(help, "app_observation, app_summary") || !strings.Contains(help, "--fields app_summary") {
			t.Fatalf("%s --help does not list app_summary", cmd)
		}
	}
	if slices.Contains(watchKeys(), "app_summary") {
		t.Fatal("watch carries no application evidence and must not accept app_summary")
	}
}

// loopRowsEnv registers one healthy two-channel source, one failed and one metadata-only source.
func loopRowsEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	seedLoopSearch(t, e)
	return e
}

func TestLoopRowsSayLatestCachedSnapshot(t *testing.T) {
	e := loopRowsEnv(t)
	row := items(t, decode(t, runOK(t, e, "--no-sync", "loop", "list")))[0]
	if row["latest_cached_snapshot"] != true || row["retained"] != false || row["text_state"] != "partial_snapshot" {
		t.Fatalf("row: %v", row)
	}
	text := runOK(t, e, "--no-sync", "--format", "text", "loop", "list")
	for _, want := range []string{"text_state", "partial_snapshot", "latest cached snapshot"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text loop list lacks %q:\n%s", want, text)
		}
	}
	// The one line that says the rows are not current pages is the only place the word appears.
	if strings.Contains(strings.Replace(text, loopRowsHint, "", 1), "current") {
		t.Fatalf("text loop list still says current:\n%s", text)
	}
	if search := runOK(t, e, "--no-sync", "--format", "text", "search", "Hello", "--source", "loop"); !strings.Contains(search, "partial_snapshot") {
		t.Fatalf("text search does not mark the Loop row partial_snapshot:\n%s", search)
	}
	id := row["id"].(string)
	if show := runOK(t, e, "--no-sync", "--format", "text", "loop", "show", id); !strings.Contains(show, "latest cached snapshot of this source; archived record matches this snapshot: yes; archived record still in the app's cache: yes") {
		t.Fatalf("text loop show:\n%s", show)
	}
	e.exec(`update records set removed_at='2026-10-12T06:00:00.000Z' where key_json='hello'`)
	gone := runOK(t, e, "--no-sync", "--format", "text", "loop", "list")
	if !strings.Contains(gone, "archived record gone from the app's cache") {
		t.Fatalf("a removed record is not named plainly:\n%s", gone)
	}
	if show := runOK(t, e, "--no-sync", "--format", "text", "loop", "show", id); !strings.Contains(show, "archived record gone from the app's cache") {
		t.Fatalf("text loop show of a removed record:\n%s", show)
	}
	e.exec(`update records set removed_at=null, value_json='` + loopCLIValue(t, 9, [2]string{"greeting", "Hello again"}) + `', content_hash='next', updated_at='2026-10-12T07:00:00.000Z' where key_json='hello'`)
	runOK(t, e, "loop", "sync")
	older := runOK(t, e, "--no-sync", "--format", "text", "loop", "list")
	if !strings.Contains(older, "older cached snapshot, archived record differs") {
		t.Fatalf("an older snapshot is not named plainly:\n%s", older)
	}
	if show := runOK(t, e, "--no-sync", "--format", "text", "loop", "show", id); !strings.Contains(show, "older cached snapshot (a newer snapshot of this source exists)") {
		t.Fatalf("text loop show of an older snapshot:\n%s", show)
	}
	help := helpOf(t, "loop")
	for _, want := range []string{"latest_cached_snapshot", "latest cached snapshot", "raw_present=false means the archived record is gone from the app's cache"} {
		if !strings.Contains(help, want) {
			t.Fatalf("loop --help lacks %q", want)
		}
	}
}

// Shared search says, per source, how many hits matched before the limit, so zero returned Loop
// hits are told apart from no Loop hits.
func TestSearchReportsMatchedPerSource(t *testing.T) {
	e := loopRowsEnv(t)
	clipped := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--limit", "1"))
	loop := searchSourceCount(t, clipped, "loop")
	if loop["count"] != float64(0) || loop["returned"] != float64(0) || loop["matched"] != float64(1) || loop["truncated"] != true || loop["matched_at_least"] != nil {
		t.Fatalf("crowded-out Loop hit: %v", loop)
	}
	full := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--limit", "500"))
	chatsAll := searchSourceCount(t, full, "chats")
	chats := searchSourceCount(t, clipped, "chats")
	if chats["returned"] != float64(1) || chats["matched"] != chatsAll["returned"] || chatsAll["matched"] != chatsAll["returned"] || chatsAll["returned"].(float64) < 2 {
		t.Fatalf("chats matched is not the exact count: clipped %v, full %v", chats, chatsAll)
	}
	if all := searchSourceCount(t, full, "loop"); all["returned"] != float64(1) || all["matched"] != float64(1) || all["truncated"] != false {
		t.Fatalf("returned Loop hit: %v", all)
	}
	text := runOK(t, e, "--no-sync", "--format", "text", "search", "Hello", "--limit", "1")
	if !strings.Contains(text, "1 Loop hit not shown; use --source loop") || !strings.Contains(text, "1 chat hit not shown; use --source chats") {
		t.Fatalf("text search has no crowded-out hint:\n%s", text)
	}
	none := decode(t, runOK(t, e, "--no-sync", "search", "Meeting", "--limit", "1"))
	if zero := searchSourceCount(t, none, "loop"); zero["matched"] != float64(0) || zero["returned"] != float64(0) || zero["truncated"] != false {
		t.Fatalf("no Loop hit: %v", zero)
	}
	if quiet := runOK(t, e, "--no-sync", "--format", "text", "search", "Meeting", "--limit", "1"); strings.Contains(quiet, "Loop hit") {
		t.Fatalf("a hint for Loop hits that do not exist:\n%s", quiet)
	}
	only := decode(t, runOK(t, e, "--no-sync", "search", "Hello", "--source", "loop"))
	if own := searchSourceCount(t, only, "loop"); own["matched"] != float64(1) || own["returned"] != float64(1) {
		t.Fatalf("--source loop: %v", own)
	}
	help := helpOf(t, "search")
	for _, want := range []string{"matched_at_least", "returned", "not shown; use --source"} {
		if !strings.Contains(help, want) {
			t.Fatalf("search --help lacks %q", want)
		}
	}
}

func TestSourceCountOf(t *testing.T) {
	exact := newSourceCount(2, 5, false, true, true)
	if *exact.Matched != 5 || exact.MatchedAtLeast != nil || exact.Returned != 2 || exact.Count != 2 || !exact.Truncated {
		t.Fatalf("exact: %+v", exact)
	}
	atLeast := newSourceCount(1, 50, true, true, true)
	if atLeast.Matched != nil || *atLeast.MatchedAtLeast != 50 {
		t.Fatalf("at least: %+v", atLeast)
	}
	sources := &searchSources{Mail: atLeast, Loop: exact, Notes: newSourceCount(3, 3, false, false, true), limit: 5}
	hints := sources.hints()
	if len(hints) != 2 || hints[0] != "at least 49 mail hits not shown; use --source mail and raise --limit" || hints[1] != "3 Loop hits not shown; use --source loop" {
		t.Fatalf("hints: %v", hints)
	}
	if (*searchSources)(nil).hints() != nil {
		t.Fatal("no sources, no hints")
	}
}

func TestOverviewLoopRowSaysWhatItsTimeAndCountsMean(t *testing.T) {
	e := loopRowsEnv(t)
	row := overviewLoop(t, e)
	note, _ := row["note"].(string)
	if row["last_projected_at"] == nil || row["last_projected_at"] != row["last_sync_at"] {
		t.Fatalf("last_projected_at: %v", row)
	}
	for _, want := range []string{"last_sync_at is the last Loop projection (last_projected_at), not an edit time", "channels are partial snapshots of cached text",
		"an archived record is one cached Loop record in the archive, registered as one source; a source holds any number of channels, or none"} {
		if !strings.Contains(note, want) {
			t.Fatalf("overview loop note lacks %q: %s", want, note)
		}
	}
	text := runOK(t, e, "--format", "text")
	if !strings.Contains(text, "Loop channel (partial snapshot) from 3 sources; 1 failed") || !strings.Contains(text, "(last projection)") {
		t.Fatalf("text overview:\n%s", text)
	}
	if chats := decode(t, runOK(t, e))["sources"].([]any)[0].(map[string]any); chats["last_projected_at"] != nil {
		t.Fatalf("chats carry a Loop key: %v", chats)
	}
}

func TestSearchIsDescribedByEverySourceItSearches(t *testing.T) {
	for _, text := range []string{helpOf(t), runOK(t, newEnv(t), "--format", "text")} {
		line := ""
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, `m365crawl search "words"`) {
				line = l
			}
		}
		for _, want := range []string{"chats", "mail", "transcripts", "notes", "document", "Loop"} {
			if !strings.Contains(line, want) {
				t.Fatalf("Start here search line does not name %s: %q", want, line)
			}
		}
	}
	help := strings.Join(strings.Fields(helpOf(t)), " ")
	if !strings.Contains(help, "Full-text search over Teams chats, Outlook mail, fetched meeting transcripts, indexed notes, document metadata, cached Loop channels and offline cloud content") {
		t.Fatalf("top-level help does not say what search covers:\n%s", help)
	}
}

func TestLoopRetryOnlyWhereItHelps(t *testing.T) {
	e := loopRowsEnv(t)
	doc := decode(t, runOK(t, e, "--no-sync", "loop", "sources"))
	needed := map[string]bool{}
	for _, source := range items(t, doc) {
		key := source["tuple"].(map[string]any)["key_json"].(string)
		flag, has := source["retry_needed"].(bool)
		if needed[key] = flag; !has || source["retry"] == nil {
			t.Fatalf("no retry_needed, or retry removed: %v", source)
		}
	}
	if needed["hello"] || !needed["failed"] || needed["metadata"] {
		t.Fatalf("retry_needed: %v", needed)
	}
	text := runOK(t, e, "--no-sync", "--format", "text", "loop", "sources")
	assertCurrentLoopRetryTable(t, text)
	for _, cmd := range [][]string{{"loop"}, {"loop", "sync"}} {
		help := strings.Join(strings.Fields(helpOf(t, cmd...)), " ")
		for _, want := range []string{"retry_needed", "cannot recover", "missing_blobs"} {
			if !strings.Contains(help, want) {
				t.Fatalf("%v --help lacks %q:\n%s", cmd, want, help)
			}
		}
	}
}

func TestLoopCommandsReportArchiveAge(t *testing.T) {
	e := loopRowsEnv(t)
	id := items(t, decode(t, runOK(t, e, "--no-sync", "loop", "list")))[0]["id"].(string)
	for _, args := range [][]string{{"loop", "list"}, {"loop", "search", "Hello"}, {"loop", "show", id}, {"loop", "sources"}, {"search", "Hello", "--source", "loop"}} {
		doc := decode(t, runOK(t, e, append([]string{"--no-sync"}, args...)...))
		if age, ok := doc["archive_age_seconds"].(float64); !ok || age < 0 {
			t.Fatalf("%v archive_age_seconds = %v", args, doc["archive_age_seconds"])
		}
	}
	if text := runOK(t, e, "--no-sync", "--format", "text", "loop", "list"); !strings.Contains(text, "archive age: ") || strings.Contains(text, "never synced") {
		t.Fatalf("text loop list has no archive age:\n%s", text)
	}
	never := newEnv(t)
	insertLoopCLI(t, loopRecords(t, never), "hello", loopCLIValue(t, 5, [2]string{"greeting", "Hello"}))
	runOK(t, never, "loop", "sync")
	doc := decode(t, runOK(t, never, "--no-sync", "loop", "list"))
	if _, has := doc["archive_age_seconds"]; !has || doc["archive_age_seconds"] != nil {
		t.Fatalf("an archive that never synced has an age: %v", doc["archive_age_seconds"])
	}
	if text := runOK(t, never, "--no-sync", "--format", "text", "loop", "list"); !strings.Contains(text, "archive age: never synced") {
		t.Fatalf("text loop list of a never-synced archive:\n%s", text)
	}
	if help := helpOf(t, "loop"); !strings.Contains(help, "archive_age_seconds") {
		t.Fatal("loop --help does not say what archive_age_seconds is")
	}
}

func TestLoopShowHelpMatchesItsOutput(t *testing.T) {
	for _, cmd := range [][]string{{}, {"loop"}, {"loop", "show"}} {
		if help := helpOf(t, cmd...); strings.Contains(help, "snapshot versions") {
			t.Fatalf("%v --help still promises snapshot versions", cmd)
		}
	}
	help := strings.Join(strings.Fields(helpOf(t, "loop", "show")), " ")
	for _, want := range []string{"one channel of one snapshot", "returns exactly one item", "loop list --source-id"} {
		if !strings.Contains(help, want) {
			t.Fatalf("loop show --help lacks %q:\n%s", want, help)
		}
	}
}
