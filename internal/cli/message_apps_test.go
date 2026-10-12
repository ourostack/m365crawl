package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Synthetic application evidence; nothing here comes from a real cache.
const (
	appChat   = "19:app-evidence@thread.v2"
	appBot    = "28:synthetic-bot"
	appCitRaw = `{"properties":{"fromAppMetadata":{"id":"app-synthetic","name":"Synthetic Helper"},"botMetadata":{"replyToId":"native-reply-1"},` +
		`"botCitations":[{"id":9223372036854775807,"title":"First source","link":"https://example.invalid/a?x=1&y=2"},"refused",{"id":7,"title":"Second source","link":null,"contentType":""}]}}`
)

var appT0 = time.Date(2030, 1, 2, 9, 0, 0, 0, time.UTC)

// seedTeams writes synthetic Teams rows into the env's archive through the store.
func seedTeams(t *testing.T, e *env, convs []teamsdesktop.Conversation, ms ...teamsdesktop.Message) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.ApplyConversations(ctx, convs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyMessages(ctx, ms); err != nil {
		t.Fatal(err)
	}
}

func appMsg(conv, id, text, raw string, at time.Time) teamsdesktop.Message {
	return teamsdesktop.Message{TenantID: tenantA, UserID: userA, ConversationID: conv, ID: id, SenderID: appBot, SenderName: "Helper",
		SentAt: at, MessageType: "RichText/Html", ContentText: text, Version: 1, Raw: []byte(raw)}
}

func appChatConv() teamsdesktop.Conversation {
	return teamsdesktop.Conversation{TenantID: tenantA, UserID: userA, ID: appChat, Kind: "Chat", DisplayName: "Evidence chat",
		ReadHorizonAt: appT0.Add(-time.Hour), Raw: []byte(`{}`)}
}

// appEnv syncs the Teams fixture, then adds one chat holding an application answer with citations
// and an ordinary message.
func appEnv(t *testing.T) *env {
	t.Helper()
	e := textEnv(t)
	e.sync()
	seedTeams(t, e, []teamsdesktop.Conversation{appChatConv()},
		appMsg(appChat, "app-1", "evidence answer", appCitRaw, appT0),
		appMsg(appChat, "plain-1", "evidence plain", `{"id":"plain-1"}`, appT0.Add(time.Minute)))
	return e
}

func jsonRun(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func byID(t *testing.T, list []map[string]any, id string) map[string]any {
	t.Helper()
	for _, it := range list {
		if it["id"] == id {
			return it
		}
	}
	t.Fatalf("no item %s in %v", id, list)
	return nil
}

func coverageOf(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	c, ok := m["application_evidence_coverage"].(map[string]any)
	if !ok {
		t.Fatalf("no application_evidence_coverage in %v", keysOf(m))
	}
	return c
}

func states(c map[string]any) string {
	s := c["states"].(map[string]any)
	return fmt.Sprintf("mapped=%v partial=%v absent=%v failed=%v not_derived=%v", s["mapped"], s["partial"], s["absent"], s["failed"], s["not_derived"])
}

func TestApplicationMessageRoutes(t *testing.T) {
	e := appEnv(t)
	routes := map[string][]string{
		"messages": {"messages", "--conversation", appChat},
		"unread":   {"unread", "--conversation", appChat},
		"thread":   {"thread", appChat, "app-1"},
		"search":   {"search", "evidence", "--source", "chats"},
	}
	var first map[string]any
	for name, args := range routes {
		t.Run(name, func(t *testing.T) {
			m := jsonRun(t, e, args...)
			app := byID(t, items(t, m), "app-1")["app_observation"].(map[string]any)
			served := app["served"].(map[string]any)
			cits := served["citations"].([]any)
			c0 := cits[0].(map[string]any)
			if app["state"] != "partial" || served["app_name"] != "Synthetic Helper" || served["reply_to_id"] != "native-reply-1" || len(cits) != 2 ||
				c0["id"] != "9223372036854775807" || c0["position"] != float64(0) || cits[1].(map[string]any)["position"] != float64(2) ||
				cits[1].(map[string]any)["link"] != nil || cits[1].(map[string]any)["content_type"] != "" || c0["link"] != "https://example.invalid/a?x=1&y=2" ||
				served["refused_citation_rows"] != float64(1) || served["total_admitted"] != float64(2) || served["source_slots"] != float64(3) {
				t.Fatalf("%s app_observation: %v", name, app)
			}
			if !strings.HasPrefix(served["observation_id"].(string), "message-app-observation:") ||
				!strings.Contains(served["full_inspection"].(string), "--max-age 0 sql \"select projection_json from message_app_observations where rowid=") {
				t.Fatalf("follow-up: %v", served)
			}
			if first == nil {
				first = app
			} else if fmt.Sprint(first["served"].(map[string]any)["observation_id"]) != fmt.Sprint(served["observation_id"]) {
				t.Fatalf("routes disagree on the observation: %v vs %v", first, app)
			}
			if name != "thread" {
				if plain := byID(t, items(t, m), "plain-1")["app_observation"].(map[string]any); plain["state"] != "absent" {
					t.Fatalf("ordinary message: %v", plain)
				}
			}
			c := coverageOf(t, m)
			if c["scope"] != "returned_teams_messages" || c["state"] != "observed" || c["mapper_version"] != float64(store.MessageApplicationVersion) {
				t.Fatalf("coverage: %v", c)
			}
			// --fields id drops the per-item envelope, not the outer coverage.
			slim := jsonRun(t, e, append(args, "--fields", "id")...)
			if _, has := items(t, slim)[0]["app_observation"]; has || states(coverageOf(t, slim)) != states(c) {
				t.Fatalf("--fields id: %v %v", items(t, slim)[0], slim["application_evidence_coverage"])
			}
		})
	}
	// One message stays one search hit: the application evidence adds no item and no total.
	m := jsonRun(t, e, "search", "evidence", "--source", "chats")
	if m["count"] != float64(2) || m["sources"].(map[string]any)["chats"].(map[string]any)["count"] != float64(2) {
		t.Fatalf("search hits: %v %v", m["count"], m["sources"])
	}
	// Text output names the claim, the citations, the refusal and the archive-only follow-up.
	code, out, _ := e.run("--max-age", "0", "--format", "text", "messages", "--conversation", appChat)
	for _, want := range []string{"[app: Synthetic", "message app-1 (thread: 19:app-evidence@thread.v2 app-1)", `app "Synthetic Helper", app id "app-synthetic", claimed by the message itself`,
		`native reply-to id "native-reply-1" (not a verified thread root)`, "citations: 2 of 2 admitted shown; 1 source rows refused",
		`[0] id 9223372036854775807 "First source"`, "full record: m365crawl --db ",
		"app evidence of 2 returned Teams messages (mapper v2): 1 partial, 1 absent"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("text lacks %q:\n%s", want, out)
		}
	}
}

func TestApplicationNotDerivedAndFailedText(t *testing.T) {
	e := appEnv(t)
	e.exec(`delete from message_app_current where message_id='plain-1'`)
	seedTeams(t, e, nil, appMsg(appChat, "bad-1", "evidence broken", `{"properties":{},"properties":{}}`, appT0.Add(2*time.Minute)))
	m := jsonRun(t, e, "messages", "--conversation", appChat)
	nd := byID(t, items(t, m), "plain-1")["app_observation"].(map[string]any)
	bad := byID(t, items(t, m), "bad-1")["app_observation"].(map[string]any)
	if nd["state"] != "not_derived" || nd["served"] != nil || !strings.Contains(nd["meaning"].(string), "m365crawl sync") ||
		bad["state"] != "failed" || bad["failure_code"] != "teams_app_raw_invalid" || bad["served"].(map[string]any)["citations"] != nil {
		t.Fatalf("not derived / failed: %v %v", nd, bad)
	}
	if c := coverageOf(t, m); !strings.Contains(c["note"].(string), "m365crawl sync") || states(c) != "mapped=0 partial=1 absent=0 failed=1 not_derived=1" || c["retained"] != float64(0) {
		t.Fatalf("coverage: %v", c)
	}
	// A later failed revision of the application answer serves the retained earlier one.
	failed := appMsg(appChat, "app-1", "evidence answer", `{"properties":[]`, appT0)
	failed.Version = 2
	seedTeams(t, e, nil, failed, appMsg(appChat, "id-only", "evidence id only", `{"properties":{"fromAppMetadata":{"id":"only-id"}}}`, appT0.Add(3*time.Minute)))
	m = jsonRun(t, e, "search", "evidence", "--source", "chats")
	retained := byID(t, items(t, m), "app-1")["app_observation"].(map[string]any)
	if retained["state"] != "failed" || retained["retained"] != true || retained["retained_reason"] != "current_failed" ||
		retained["served"].(map[string]any)["app_name"] != "Synthetic Helper" || coverageOf(t, m)["retained"] != float64(1) {
		t.Fatalf("retained: %v", retained)
	}
	_, out, _ := e.run("--max-age", "0", "--format", "text", "search", "evidence", "--source", "chats")
	if !strings.Contains(out, "served is the last good observation of this message, from mapper version 2") || !strings.Contains(out, "[app id: only-id]") ||
		!strings.Contains(out, `app id "only-id", claimed by the message itself`) || !strings.Contains(out, "1 retained") {
		t.Fatalf("search text:\n%s", out)
	}
	_, out, _ = e.run("--max-age", "0", "--format", "text", "search", "zzzznomatch", "--source", "chats")
	if !strings.Contains(out, "app evidence: unknown (no Teams message returned)") {
		t.Fatalf("empty search text:\n%s", out)
	}
	_, out, _ = e.run("--max-age", "0", "--format", "text", "messages", "--conversation", appChat)
	if !strings.Contains(out, "application evidence failed (teams_app_raw_invalid)") || !strings.Contains(out, "1 not derived") || !strings.Contains(out, "not derived yet: run m365crawl sync; reads with --max-age 0 never derive it") {
		t.Fatalf("text:\n%s", out)
	}
}

func TestApplicationCoverageAfterMergedLimit(t *testing.T) {
	e := appEnv(t)
	// Newer non-chat hits are not available offline here, so the displacement is by newer chats in
	// another conversation: only surviving hits count.
	var newer []teamsdesktop.Message
	for i := 0; i < 3; i++ {
		newer = append(newer, appMsg("19:other@thread.v2", fmt.Sprintf("n-%d", i), "evidence newer", `{"id":"n"}`, appT0.Add(time.Duration(10+i)*time.Hour)))
	}
	seedTeams(t, e, []teamsdesktop.Conversation{{TenantID: tenantA, UserID: userA, ID: "19:other@thread.v2", Kind: "Chat", DisplayName: "Other", Raw: []byte(`{}`)}}, newer...)
	for _, fields := range [][]string{nil, {"--fields", "id"}} {
		m := jsonRun(t, e, append([]string{"search", "evidence", "--limit", "3"}, fields...)...)
		c := coverageOf(t, m)
		if c["returned_messages"] != float64(3) || states(c) != "mapped=0 partial=0 absent=3 failed=0 not_derived=0" {
			t.Fatalf("displaced application hits were counted: %v", c)
		}
		m = jsonRun(t, e, append([]string{"search", "evidence", "--limit", "5"}, fields...)...)
		if c := coverageOf(t, m); c["returned_messages"] != float64(5) || states(c) != "mapped=0 partial=1 absent=4 failed=0 not_derived=0" {
			t.Fatalf("surviving subset: %v", c)
		}
	}
	// A search that consulted chats and returned none says unknown, not absent.
	m := jsonRun(t, e, "search", "zzzznomatch", "--source", "chats")
	if c := coverageOf(t, m); c["state"] != "unknown" || c["returned_messages"] != float64(0) || !strings.Contains(c["note"].(string), "unknown") {
		t.Fatalf("zero hits: %v", c)
	}
	// A route that never consults messages has no coverage.
	if m := jsonRun(t, e, "conversations"); m["application_evidence_coverage"] != nil {
		t.Fatalf("conversations invented coverage: %v", m["application_evidence_coverage"])
	}
}

func citationsRaw(n int) string {
	var b strings.Builder
	b.WriteString(`{"properties":{"botCitations":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		if i%10 == 3 {
			b.WriteString(`{"id":"refused","title":"x"}`) // a refused hole keeps native positions
			continue
		}
		fmt.Fprintf(&b, `{"id":%d,"title":"T%d"}`, i%5, i) // repeated ids stay separate citations
	}
	b.WriteString(`]}}`)
	return b.String()
}

func TestApplicationCitationPreviewBounds(t *testing.T) {
	e := textEnv(t)
	e.sync()
	admitted := map[int]int{0: 0, 100: 0, 101: 0}
	var ms []teamsdesktop.Message
	slots := map[int]int{0: 0, 100: 111, 101: 112} // with every tenth slot refused
	for n := range admitted {
		ms = append(ms, appMsg(appChat, fmt.Sprintf("c-%d", n), "evidence", citationsRaw(slots[n]), appT0.Add(time.Duration(n)*time.Second)))
	}
	seedTeams(t, e, []teamsdesktop.Conversation{appChatConv()}, ms...)
	m := jsonRun(t, e, "messages", "--conversation", appChat)
	for n := range admitted {
		served := byID(t, items(t, m), fmt.Sprintf("c-%d", n))["app_observation"].(map[string]any)["served"].(map[string]any)
		if n == 0 {
			if served["citations"] != nil {
				t.Fatalf("empty citations: %v", served)
			}
			continue
		}
		cits := served["citations"].([]any)
		last := cits[len(cits)-1].(map[string]any)
		wantTrunc := n == 101
		if served["total_admitted"] != float64(n) || len(cits) != 100 || served["citations_count"] != float64(100) || served["citations_truncated"] != wantTrunc ||
			cits[3].(map[string]any)["position"] != float64(4) || cits[0].(map[string]any)["id"] != cits[4].(map[string]any)["id"] || last["title"] != "T110" && n == 100 {
			t.Fatalf("%d admitted: total=%v count=%v trunc=%v last=%v", n, served["total_admitted"], served["citations_count"], served["citations_truncated"], last)
		}
	}
}

func TestCalendarAppEvidenceBounds(t *testing.T) {
	e := calEnv(t)
	review := itemBySubject(t, agenda(t, e, "--from", "2023-11-20", "--days", "1", "--account", tenantA+"/"+userA), "Fixture planning review")
	id := review["event_id"].(string)
	acct := []string{"--account", tenantA + "/" + userA}
	chat := eventDoc(t, e, id, acct...)["chat"].(map[string]any)["conversation_id"].(string)
	long := strings.Repeat("N", 40)
	raw := `{"properties":{"fromAppMetadata":{"id":"app-long","name":"` + long + `"},"botCitations":[{"id":1,"title":"` + long + `"}]}}`
	seedTeams(t, e, nil, appMsg(chat, "cal-app", "evidence in the meeting chat", raw, appT0))
	d := eventDoc(t, e, id, acct...)
	recent := d["chat"].(map[string]any)["recent_messages"].([]any)[0].(map[string]any)
	app := recent["app_observation"].(map[string]any)["served"].(map[string]any)
	if app["app_name"] != long || coverageOf(t, d)["returned_messages"] != float64(9+1) {
		t.Fatalf("calendar recent app evidence: %v %v", app, d["application_evidence_coverage"])
	}
	cut := eventDoc(t, e, id, append(acct, "--max-text", "5")...)
	recent = cut["chat"].(map[string]any)["recent_messages"].([]any)[0].(map[string]any)
	obs := recent["app_observation"].(map[string]any)
	app = obs["served"].(map[string]any)
	cit := app["citations"].([]any)[0].(map[string]any)
	if cut["text_truncated"] != true || app["app_name"] != "NNNNN…" || app["app_name_truncated"] != true || cit["title"] != "NNNNN…" || cit["title_truncated"] != true ||
		obs["text_truncated"] != true || app["app_id"] != "app-long" {
		t.Fatalf("calendar clipping bypassed application bounds: %v", obs)
	}
	if f := eventDoc(t, e, id, append(acct, "--fields", "subject")...); f["application_evidence_coverage"] == nil || f["chat"] != nil {
		t.Fatalf("--fields subject dropped coverage or kept chat: %v", keysOf(f))
	}
	_, out, _ := e.run("--max-age", "0", "--format", "text", "--account", tenantA+"/"+userA, "calendar", "event", id)
	if !strings.Contains(out, "app evidence of 10 returned Teams messages (mapper v2): 1 mapped, 9 absent") || !strings.Contains(out, "application evidence mapped") {
		t.Fatalf("calendar text:\n%s", out)
	}
}

func TestApplicationItemHelpers(t *testing.T) {
	if fullInspection(0, "/a.db") != "" || fullInspection(-1, "/a.db") != "" {
		t.Fatal("a non-positive id produced a follow-up command")
	}
	name, title := "ABCDEFGH", "abcdefgh"
	env := store.MessageApplicationEnvelope{State: store.AppStateMapped, ActiveMapperVersion: 1, Served: &store.ApplicationRepresentation{ID: 4, State: store.AppStateMapped,
		Payload: store.ApplicationPayload{AppName: &name, Citations: []store.ApplicationCitation{{ID: "1", Title: title}}}}}
	it := applicationItemOf(env, 0, "/a.db")
	if it.TextTruncated || it.truncate(0) || !it.truncate(3) || *it.Served.AppName != "ABC…" || it.Served.Citations[0].Title != "abc…" {
		t.Fatalf("clip: %+v", it.Served.applicationPayloadItem)
	}
	if !it.truncate(3) || *it.Served.AppName != "ABC…" || it.Served.Citations[0].Title != "abc…" || name != "ABCDEFGH" {
		t.Fatal("clipping is not idempotent or changed the store's strings")
	}
	if appMarker(nil) != "" || appDetailLines("x", nil) != nil || appText("a\x1b[31mb\n c") != "a?[31mb c" {
		t.Fatal("helpers")
	}
	retained := store.MessageApplicationEnvelope{State: store.AppStateFailed, FailureCode: "teams_app_raw_invalid", Retained: true,
		Served: &store.ApplicationRepresentation{ID: 2, State: store.AppStateMapped, Revision: store.ApplicationRevision{MapperVersion: 1}}}
	if it := applicationItemOf(retained, 0, ""); appMarker(&it) != "[app evidence, retained] " || !strings.Contains(it.Meaning, "last good") {
		t.Fatalf("retained: %q %q", appMarker(&it), it.Meaning)
	}
	corrupt := store.MessageApplicationEnvelope{State: store.AppStateFailed, FailureCode: store.AppProjectionCorrupt}
	if it := applicationItemOf(corrupt, 0, ""); !strings.Contains(it.Meaning, "failed validation") || len(appDetailLines("x", &it)) != 1 {
		t.Fatalf("corrupt: %+v", it)
	}
}

// posixWords splits a POSIX shell command line with single and double quotes, enough for the
// follow-up commands this package prints.
func posixWords(s string) []string {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			cur.WriteString(s[i+1 : i+1+j])
			i, inWord = i+1+j, true
		case '"':
			j := strings.IndexByte(s[i+1:], '"')
			cur.WriteString(s[i+1 : i+1+j])
			i, inWord = i+1+j, true
		case ' ':
			if inWord {
				out, inWord = append(out, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out
}

// The full_inspection follow-up names the archive it came from, so running it as printed reads
// the same representation even when --db is not the default.
func TestApplicationFullInspectionKeepsArchive(t *testing.T) {
	if got, want := fullInspection(7, "/tmp/a b/it's.db"), `m365crawl --db '/tmp/a b/it'\''s.db' --max-age 0 sql "select projection_json from message_app_observations where rowid=7" --limit 1`; got != want {
		t.Fatalf("template:\n got %s\nwant %s", got, want)
	}
	e := appEnv(t)
	served := byID(t, items(t, jsonRun(t, e, "messages", "--conversation", appChat)), "app-1")["app_observation"].(map[string]any)["served"].(map[string]any)
	cmd := served["full_inspection"].(string)
	words := posixWords(cmd)
	if words[0] != "m365crawl" || !strings.Contains(cmd, "--db ") {
		t.Fatalf("follow-up does not name the archive: %s", cmd)
	}
	t.Setenv("HOME", t.TempDir()) // the default archive is elsewhere and empty
	t.Setenv("M365CRAWL_DB", "")
	var out, errb strings.Builder
	if code := Main(append(words[1:], "--json"), &out, &errb); code != 0 || !strings.Contains(out.String(), "Synthetic Helper") {
		t.Fatalf("running %s: exit %d %s %s", cmd, code, out.String(), errb.String())
	}
}

func TestApplicationArchiveRef(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := (&runtime{dbPath: "rel/a.db"}).archiveRef(); got != filepath.Join(wd, "rel/a.db") {
		t.Fatalf("relative: %s", got)
	}
	if got := (&runtime{dbPath: "/abs/a.db"}).archiveRef(); got != "/abs/a.db" {
		t.Fatalf("absolute: %s", got)
	}
	old := getwd
	t.Cleanup(func() { getwd = old })
	getwd = func() (string, error) { return "", errors.New("no working directory") }
	if got := (&runtime{dbPath: "rel/a.db"}).archiveRef(); got != "rel/a.db" {
		t.Fatalf("without a working directory: %s", got)
	}
	if shellQuote("/plain/a.db") != "/plain/a.db" || shellQuote("") != "''" {
		t.Fatal("shellQuote")
	}
}

// Coverage reports the mapper version the store read under, not a build constant.
func TestApplicationCoverageMapperVersion(t *testing.T) {
	envs := []store.MessageApplicationEnvelope{{State: store.AppStateAbsent, ActiveMapperVersion: 5}}
	if c := applicationCoverageOf(envs); c.MapperVersion != 5 {
		t.Fatalf("observed coverage mapper version = %d", c.MapperVersion)
	}
	if c := applicationCoverageOf(nil); c.MapperVersion != store.ActiveApplicationVersion() {
		t.Fatalf("unknown coverage mapper version = %d", c.MapperVersion)
	}
}
