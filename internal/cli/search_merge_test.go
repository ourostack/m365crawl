package cli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

const searchMailAccount = "outlook/SearchTest"

const (
	searchFInbox    = 10
	searchFSent     = 11
	searchFProjects = 12
)

func searchFolders() []outlookmail.Folder {
	return []outlookmail.Folder{
		{Key: searchFInbox, Parent: 1, Name: "Inbox", Kind: "inbox"},
		{Key: searchFSent, Parent: 1, Name: "Sent Items", Kind: "sent"},
		{Key: searchFProjects, Parent: 1, Name: "Projects", Kind: "other"},
	}
}

func searchMsg(dk, folder uint32, subject string, received time.Time) outlookmail.Message {
	unread := true
	kind := "other"
	for _, f := range searchFolders() {
		if f.Key == folder {
			kind = f.Kind
		}
	}
	m := outlookmail.Message{Account: searchMailAccount, Folder: outlookmail.Folder{Key: folder, Kind: kind}, Copies: 1}
	m.DetailKey, m.FolderKey = dk, folder
	m.MessageID, m.Subject, m.Received, m.Sent = "<"+subject+"@x.test>", subject, received, received.Add(-time.Minute)
	m.SenderName, m.SenderAddress = "Ann Sender", "ann@example.test"
	m.Unread, m.ReadState, m.Flag, m.Importance = &unread, "unread", "none", "normal"
	m.Recipients = []outlookmail.Recipient{{Name: "Bob Reader", Address: "bob@example.test", KindRaw: 1}, {Address: "cy@example.test", KindRaw: 2}}
	m.Class = "IPM.Note"
	m.Preview = "preview of " + subject
	m.Body = outlookmail.Body{State: outlookmail.BodyInline, HTML: []byte("<p>body of " + subject + "</p>")}
	return m
}

func searchDay(day, hour, min int) time.Time {
	return time.Date(2023, 11, day, hour, min, 0, 0, time.UTC)
}

// The Teams fixture's "Hello" chats were sent at 2023-11-14T22:13:21Z.
func searchMessages() []outlookmail.Message {
	hello := searchMsg(201, searchFInbox, "Hello team", searchDay(14, 22, 15))
	helloAgain := searchMsg(202, searchFInbox, "Hello again", searchDay(10, 9, 0))
	helloProj := searchMsg(203, searchFProjects, "Hello from projects", searchDay(13, 8, 0))
	helloProj.Recipients = append(helloProj.Recipients, outlookmail.Recipient{Address: "di@example.test"}, outlookmail.Recipient{Address: "ed@example.test"})
	needle := searchMsg(204, searchFSent, "Mail only needle", searchDay(12, 8, 0))
	bare := searchMsg(205, searchFInbox, "Bare hello", searchDay(9, 8, 0))
	bare.SenderName, bare.Preview, bare.Recipients = "", "", nil
	return []outlookmail.Message{hello, helloAgain, helloProj, needle, bare}
}

// searchEnv names every source root: the Teams fixture and a copy of the Outlook fixture.
func searchEnv(t *testing.T) *env {
	t.Helper()
	e := textEnv(t)
	t.Setenv("M365CRAWL_TEAMS_ROOT", e.root)
	t.Setenv("M365CRAWL_OUTLOOK_ROOT", outlookProfiles(t, "Main"))
	oldPlatform := searchMailPlatform
	searchMailPlatform = "darwin"
	t.Cleanup(func() { searchMailPlatform = oldPlatform })
	return e
}

func searchSeed(t *testing.T, e *env, msgs []outlookmail.Message) {
	t.Helper()
	e.sync()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	res := outlookmail.Result{Messages: msgs, Folders: searchFolders()}
	cov := map[uint32]*outlookmail.Coverage{}
	for _, m := range msgs {
		c := cov[m.FolderKey]
		if c == nil {
			c = &outlookmail.Coverage{FolderKey: m.FolderKey, Oldest: m.Received, Newest: m.Received}
			cov[m.FolderKey] = c
		}
		c.Count++
	}
	for _, c := range cov {
		res.Coverage = append(res.Coverage, *c)
	}
	at := searchDay(20, 0, 0)
	if _, err := st.CommitMail(context.Background(), store.MailBatch{Account: searchMailAccount, ReadAt: at, FreshAt: at, Result: res, Trusted: true}); err != nil {
		t.Fatal(err)
	}
}

// searchTemplates holds, per kind, a synced archive that tests copy instead of syncing again: a
// sync of the fixtures is the slow part of these tests, most of all on Windows under -race.
// searchTempBase is the temp directory before any test points TMPDIR at its own.
var searchTempBase = os.TempDir()

var searchTemplates struct {
	sync.Mutex
	dirs map[bool]string
}

// removeSearchTemplates deletes the shared archives; TestMain calls it.
func removeSearchTemplates() {
	for _, d := range searchTemplates.dirs {
		_ = os.RemoveAll(d)
	}
}

func copyFlat(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o750); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(from, ent.Name())) //nolint:gosec // a test temp dir
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, ent.Name()), b, 0o600); err != nil { //nolint:gosec // test-only path under t.TempDir()
			t.Fatal(err)
		}
	}
}

// searchArchive puts a synced archive into e: the fixtures alone, or with the synthetic mailbox.
func searchArchive(t *testing.T, e *env, withMail bool) {
	t.Helper()
	searchTemplates.Lock()
	defer searchTemplates.Unlock()
	dir, ok := searchTemplates.dirs[withMail]
	if !ok {
		builder := searchEnv(t)
		if withMail {
			searchSeed(t, builder, searchMessages())
		} else {
			builder.sync()
		}
		var err error
		if dir, err = os.MkdirTemp(searchTempBase, "m365crawl-search-template-"); err != nil {
			t.Fatal(err)
		}
		copyFlat(t, filepath.Dir(builder.db), dir)
		if searchTemplates.dirs == nil {
			searchTemplates.dirs = map[bool]string{}
		}
		searchTemplates.dirs[withMail] = dir
	}
	copyFlat(t, dir, filepath.Dir(e.db))
}

func searchMailEnv(t *testing.T) *env {
	t.Helper()
	e := searchEnv(t)
	searchArchive(t, e, true)
	return e
}

func searchJSON(t *testing.T, e *env, args ...string) map[string]any {
	t.Helper()
	code, out, errOut := e.run(append([]string{"--max-age", "0", "--json", "search"}, args...)...)
	if code != 0 {
		t.Fatalf("search %v: exit %d: %s", args, code, errOut)
	}
	return decode(t, out)
}

func searchFails(t *testing.T, e *env, code int, args ...string) map[string]any {
	t.Helper()
	got, out, errOut := e.run(append([]string{"--max-age", "0", "--json", "search"}, args...)...)
	if got != code {
		t.Fatalf("search %v: exit %d, want %d: %s %s", args, got, code, out, errOut)
	}
	return errorOf(t, errOut)
}

func searchSourcesOf(its []map[string]any) []string {
	var out []string
	for _, it := range its {
		out = append(out, it["source"].(string))
	}
	return out
}

func itemAt(t *testing.T, it map[string]any) time.Time {
	t.Helper()
	key := "sent_at"
	if it["source"] == "mail" {
		key = "received_at"
	}
	at, err := time.Parse(time.RFC3339, it[key].(string))
	if err != nil {
		t.Fatalf("%v: %v", it, err)
	}
	return at
}

func TestSearchMergesChatsAndMailNewestFirst(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello")
	its := items(t, m)
	if got := strings.Join(searchSourcesOf(its), " "); got != "mail chats chats mail mail mail" {
		t.Fatalf("sources = %s", got)
	}
	for i := 1; i < len(its); i++ {
		if itemAt(t, its[i]).After(itemAt(t, its[i-1])) {
			t.Fatalf("item %d is newer than item %d", i, i-1)
		}
	}
	if its[0]["id"] != searchMailAccount+":201" || its[0]["subject"] != "Hello team" || its[1]["conversation_id"] == nil {
		t.Fatalf("shapes: %v / %v", its[0], its[1])
	}
	src := m["sources"].(map[string]any)
	chats, mail := src["chats"].(map[string]any), src["mail"].(map[string]any)
	if chats["count"] != float64(2) || chats["truncated"] != false || mail["count"] != float64(4) || mail["truncated"] != false {
		t.Fatalf("sources = %v", src)
	}
	if m["count"] != float64(6) || m["truncated"] != false {
		t.Fatalf("envelope = %v", m)
	}
	if _, has := m["note"]; has {
		t.Fatalf("a search of both sources needs no note: %v", m["note"])
	}
	if _, has := m["total"]; has {
		t.Fatal("no total without truncation")
	}
}

func TestSearchOneSourceMatchingLeavesTheOtherEmptyWithoutANote(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "needle")
	if got := strings.Join(searchSourcesOf(items(t, m)), " "); got != "mail" {
		t.Fatalf("sources = %s", got)
	}
	src := m["sources"].(map[string]any)
	if src["chats"].(map[string]any)["count"] != float64(0) || src["mail"].(map[string]any)["count"] != float64(1) {
		t.Fatalf("sources = %v", src)
	}
	if _, has := m["note"]; has {
		t.Fatalf("no note when one source simply has no match: %v", m["note"])
	}
	m = searchJSON(t, e, "broadcast")
	if got := strings.Join(searchSourcesOf(items(t, m)), " "); !strings.HasPrefix(got, "chats") || strings.Contains(got, "mail") {
		t.Fatalf("sources = %s", got)
	}
	if _, has := m["note"]; has {
		t.Fatal("no note for a chats-only match")
	}
}

func TestSearchWithoutWordsMergesFilterMatchesNewestFirst(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "--since", "2023-11-14", "--until", "2023-11-15")
	its := items(t, m)
	kinds := strings.Join(searchSourcesOf(its), " ")
	if !strings.Contains(kinds, "mail") || !strings.Contains(kinds, "chats") {
		t.Fatalf("both sources expected: %s", kinds)
	}
	for i := 1; i < len(its); i++ {
		if itemAt(t, its[i]).After(itemAt(t, its[i-1])) {
			t.Fatalf("item %d is newer than item %d: %v", i, i-1, kinds)
		}
	}
	if its[0]["source"] != "mail" || its[0]["id"] != searchMailAccount+":201" {
		t.Fatalf("newest first: %v", its[0])
	}
	// --from applies to both sources: a mail sender and a chat sender.
	m = searchJSON(t, e, "--from", "ann")
	if got := searchSourcesOf(items(t, m)); len(got) == 0 || !strings.Contains(strings.Join(got, " "), "mail") {
		t.Fatalf("--from ann: %v", got)
	}
	// With no query and no filter at all the usage error of Teams search stands.
	er := searchFails(t, e, 2, "")
	if !strings.Contains(er["fix"].(string), "m365crawl messages") {
		t.Fatalf("fix = %v", er["fix"])
	}
}

func TestSearchLimitCapsTheMergedListAndSaysWhoWasCut(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--limit", "2")
	its := items(t, m)
	if got := strings.Join(searchSourcesOf(its), " "); got != "mail chats" || m["count"] != float64(2) || m["truncated"] != true {
		t.Fatalf("limit 2: %s %v", got, m)
	}
	src := m["sources"].(map[string]any)
	chats, mail := src["chats"].(map[string]any), src["mail"].(map[string]any)
	if chats["count"] != float64(1) || chats["truncated"] != true || mail["count"] != float64(1) || mail["truncated"] != true {
		t.Fatalf("sources = %v", src)
	}
	// Mail alone has more than the limit, and chats fit.
	m = searchJSON(t, e, "Hello", "--limit", "1", "--source", "mail")
	if m["truncated"] != true {
		t.Fatalf("mail truncated: %v", m)
	}
	// Exactly enough room: not truncated.
	m = searchJSON(t, e, "Hello", "--limit", "6")
	if m["truncated"] != false || m["count"] != float64(6) {
		t.Fatalf("limit 6: %v", m)
	}
}

func TestSearchSourceChoosesWhatIsSearched(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--source", "mail")
	src := m["sources"].(map[string]any)
	if _, has := src["chats"]; has || m["count"] != float64(4) || strings.Contains(strings.Join(searchSourcesOf(items(t, m)), " "), "chats") {
		t.Fatalf("mail only: %v", m)
	}
	m = searchJSON(t, e, "Hello", "--source", "chats")
	src = m["sources"].(map[string]any)
	if _, has := src["mail"]; has || m["count"] != float64(2) {
		t.Fatalf("chats only: %v", m)
	}
	if _, has := m["note"]; has {
		t.Fatalf("an explicit --source needs no note: %v", m["note"])
	}
	if m["items"].([]any)[0].(map[string]any)["source"] != "chats" {
		t.Fatal("chat items carry source too")
	}
	if code, _, _ := e.run("--max-age", "0", "search", "Hello", "--source", "everything"); code != 2 {
		t.Fatalf("bad --source: exit %d", code)
	}
}

func TestSearchFlagSourceConflicts(t *testing.T) {
	e := searchMailEnv(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--mentions-me"}, "--mentions-me"},
		{[]string{"--direct-mentions"}, "--direct-mentions"},
		{[]string{"--conversation", "x"}, "--conversation"},
		{[]string{"--team", "x"}, "--team"},
		{[]string{"--include-system"}, "--include-system"},
		{[]string{"--include-deleted"}, "--include-deleted"},
		{[]string{"--html"}, "--html"},
		{[]string{"--mentions-me", "--html"}, "--mentions-me, --html"},
	} {
		er := searchFails(t, e, 2, append([]string{"Hello", "--source", "mail"}, c.args...)...)
		msg := er["message"].(string)
		if er["code"] != "flag_source_conflict" || !strings.Contains(msg, c.want) || !strings.Contains(msg, "Teams chats only") {
			t.Errorf("%v: %v", c.args, er)
		}
	}
	er := searchFails(t, e, 2, "Hello", "--source", "chats", "--folder", "inbox")
	if er["code"] != "flag_source_conflict" || !strings.Contains(er["message"].(string), "--folder") || !strings.Contains(er["message"].(string), "mail only") {
		t.Fatalf("--folder with chats: %v", er)
	}
	// No source can use both a chats-only flag and --folder.
	er = searchFails(t, e, 2, "Hello", "--folder", "inbox", "--mentions-me")
	msg := er["message"].(string)
	if er["code"] != "flag_source_conflict" || !strings.Contains(msg, "--folder") || !strings.Contains(msg, "--mentions-me") {
		t.Fatalf("both: %v", er)
	}
}

func TestSearchChatsOnlyFlagsNarrowToChatsAndSayWhy(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "review", "--mentions-me")
	if m["note"] != "--mentions-me applies to Teams chats only; mail was not searched" {
		t.Fatalf("note = %v", m["note"])
	}
	if got := strings.Join(searchSourcesOf(items(t, m)), " "); got == "" || strings.Contains(got, "mail") {
		t.Fatalf("sources = %s", got)
	}
	if _, has := m["sources"].(map[string]any)["mail"]; has {
		t.Fatal("mail was not searched, so it has no entry")
	}
	m = searchJSON(t, e, "review", "--mentions-me", "--include-deleted")
	if m["note"] != "--mentions-me and --include-deleted apply to Teams chats only; mail was not searched" {
		t.Fatalf("two flags: %v", m["note"])
	}
	m = searchJSON(t, e, "review", "--mentions-me", "--include-deleted", "--html")
	if m["note"] != "--mentions-me, --include-deleted and --html apply to Teams chats only; mail was not searched" {
		t.Fatalf("three flags: %v", m["note"])
	}
}

func TestSearchFolderNarrowsToMailAndSaysWhy(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--folder", "inbox")
	if m["note"] != "--folder applies to mail only; Teams chats were not searched" {
		t.Fatalf("note = %v", m["note"])
	}
	if got := strings.Join(searchSourcesOf(items(t, m)), " "); got != "mail mail mail" {
		t.Fatalf("sources = %s", got)
	}
	if _, has := m["sources"].(map[string]any)["chats"]; has {
		t.Fatal("chats were not searched, so they have no entry")
	}
	// --folder alone is a filter, so no words are needed.
	m = searchJSON(t, e, "--folder", "sent")
	if m["count"] != float64(1) {
		t.Fatalf("folder only: %v", m)
	}
	er := searchFails(t, e, 2, "Hello", "--folder", "nowhere")
	if er["code"] != "unknown_folder" || !strings.Contains(er["fix"].(string), "m365crawl mail folders") {
		t.Fatalf("unknown folder: %v", er)
	}
	// Mail alone with no words and no filter has no query to run.
	er = searchFails(t, e, 2, "", "--source", "mail")
	if !strings.Contains(er["message"].(string), "at least one filter") {
		t.Fatalf("mail without words: %v", er)
	}
}

func TestSearchWithoutMailInTheArchiveReturnsChatsWithANote(t *testing.T) {
	e := searchEnv(t)
	searchArchive(t, e, false)
	m := searchJSON(t, e, "Hello")
	if m["note"] != "mail is not in the archive yet; run m365crawl sync" || m["count"] != float64(2) {
		t.Fatalf("note/count = %v / %v", m["note"], m["count"])
	}
	if _, has := m["sources"].(map[string]any)["mail"]; has {
		t.Fatal("mail was not searched")
	}
	m = searchJSON(t, e, "Hello", "--source", "mail")
	if m["note"] != "mail is not in the archive yet; run m365crawl sync" || m["count"] != float64(0) {
		t.Fatalf("mail only: %v", m)
	}
	// No archive at all: nothing from either source, and the same note.
	fresh := searchEnv(t)
	m = searchJSON(t, fresh, "Hello", "--source", "mail")
	if m["note"] != "mail is not in the archive yet; run m365crawl sync" || m["count"] != float64(0) {
		t.Fatalf("no archive: %v", m)
	}
}

func TestSearchOnWindowsReturnsChatsWithANote(t *testing.T) {
	e := searchMailEnv(t)
	searchMailPlatform = "windows"
	m := searchJSON(t, e, "Hello")
	if m["note"] != "mail is not yet read on Windows; searched Teams chats only" || m["count"] != float64(2) {
		t.Fatalf("windows: %v", m)
	}
	// A chats-only flag already explains why mail is out; Windows adds nothing.
	m = searchJSON(t, e, "review", "--mentions-me")
	if m["note"] != "--mentions-me applies to Teams chats only; mail was not searched" {
		t.Fatalf("note = %v", m["note"])
	}
	er := searchFails(t, e, 3, "Hello", "--source", "mail")
	if er["code"] != "mail_unsupported_platform" || !strings.Contains(er["message"].(string), "not yet read on Windows") {
		t.Fatalf("windows mail only: %v", er)
	}
}

func TestSearchQuerySyntaxErrorsAreOneUsageError(t *testing.T) {
	e := searchMailEnv(t)
	for _, q := range []string{`"`, "*"} {
		code, out, errOut := e.run("--max-age", "0", "--json", "search", q)
		if code != 2 || out != "" || strings.Count(errOut, `"error"`) != 1 {
			t.Fatalf("query %q: exit %d, out %q, err %q", q, code, out, errOut)
		}
		fix := errorOf(t, errOut)["fix"]
		code, _, errOut = e.run("--max-age", "0", "--json", "search", q, "--source", "mail")
		if code != 2 || errorOf(t, errOut)["fix"] != fix {
			t.Fatalf("mail fix differs for %q: %s", q, errOut)
		}
	}
	// An unbalanced quote is a phrase, for both sources alike.
	m := searchJSON(t, e, `"Hello`)
	if m["count"] != float64(6) {
		t.Fatalf("unbalanced: %v", m["count"])
	}
}

func TestSearchFieldsValidateAgainstBothSources(t *testing.T) {
	e := searchMailEnv(t)
	code, out, errOut := e.run("--max-age", "0", "--json", "--fields", "source,id,subject,sent_at,received_at,folder", "search", "Hello", "--limit", "3")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	its := items(t, decode(t, out))
	if its[0]["source"] != "mail" || its[0]["folder"] != "Inbox" || its[0]["received_at"] == nil {
		t.Fatalf("mail item: %v", its[0])
	}
	if its[1]["source"] != "chats" || its[1]["sent_at"] == nil || its[1]["folder"] != nil {
		t.Fatalf("chat item: %v", its[1])
	}
	code, _, errOut = e.run("--max-age", "0", "--json", "--fields", "source,nope", "search", "Hello")
	er := errorOf(t, errOut)
	msg, _ := er["message"].(string)
	if code != 2 || !strings.Contains(msg, `unknown --fields key "nope"`) || !strings.Contains(msg, "source") || !strings.Contains(msg, "folder") || !strings.Contains(msg, "conversation_id") {
		t.Fatalf("unknown key: %d %v", code, er)
	}
}

func TestSearchAccountFlagNamesTeamsAccountsOnly(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--account", tenantA+"/"+userA)
	if m["note"] != "--account names a Teams account; mail was searched across all Outlook accounts" {
		t.Fatalf("note = %v", m["note"])
	}
	if m["sources"].(map[string]any)["mail"].(map[string]any)["count"] != float64(4) {
		t.Fatalf("mail was not narrowed: %v", m["sources"])
	}
	m = searchJSON(t, e, "review", "--mentions-me", "--account", tenantA+"/"+userA)
	if m["note"] != "--mentions-me applies to Teams chats only; mail was not searched" {
		t.Fatalf("no second note when mail is out: %v", m["note"])
	}
}

func TestSearchTotalSurvivesWhenOnlyChatsAreSearched(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Fixture", "--limit", "1", "--source", "chats")
	if m["truncated"] != true || m["total"] == nil || m["total"].(float64) < 2 {
		t.Fatalf("total: %v", m)
	}
	m = searchJSON(t, e, "Fixture", "--limit", "1")
	if _, has := m["total"]; has {
		t.Fatal("a merged list has no exact total")
	}
}

func TestSearchHTMLAndDeletedAreChatOnly(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--source", "chats", "--html")
	if m["items"].([]any)[0].(map[string]any)["html"] == nil {
		t.Fatalf("html: %v", m["items"])
	}
	m = searchJSON(t, e, "deleted", "--include-deleted")
	if m["count"] == float64(0) {
		t.Fatal("deleted chat expected")
	}
}

func TestSearchTextOutputs(t *testing.T) {
	e := searchMailEnv(t)
	for _, c := range []struct {
		name string
		args []string
	}{
		{"search_merge", []string{"search", "Hello"}},
		{"search_merge_note", []string{"search", "review", "--mentions-me"}},
		{"search_merge_mail", []string{"search", "--folder", "inbox"}},
		{"search_merge_deleted", []string{"search", "deleted", "--include-deleted", "--source", "chats"}},
	} {
		for _, color := range []bool{false, true} {
			suffix := "plain"
			t.Setenv("CLICOLOR_FORCE", "")
			if color {
				suffix = "color"
				t.Setenv("CLICOLOR_FORCE", "1")
			}
			code, out, errOut := e.run(append([]string{"--format", "text", "--max-age", "0"}, c.args...)...)
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", c.name, code, errOut)
			}
			if !color && strings.Contains(out, "\x1b") {
				t.Errorf("%s: escape in plain output", c.name)
			}
			checkGolden(t, c.name+"."+suffix, e.scrub(out))
		}
	}
}

func TestSearchMailFailuresAreReported(t *testing.T) {
	e := searchMailEnv(t)
	e.exec("drop table mail_fts")
	code, _, errOut := e.run("--max-age", "0", "--json", "search", "Hello", "--source", "mail")
	if code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("a broken mail index must fail the search: %d %s", code, errOut)
	}
}

func TestSearchMailRowsWithoutOptionalFields(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Bare")
	it := items(t, m)[0]
	if it["from_name"] != nil || it["preview"] != nil || it["recipient_count"] != float64(0) || it["recipients_preview"] == nil {
		t.Fatalf("bare mail: %v", it)
	}
	m = searchJSON(t, e, "Hello", "--source", "mail", "--limit", "1")
	it = items(t, m)[0]
	if it["recipient_count"] != float64(2) || len(it["recipients_preview"].([]any)) != 2 || it["recipients_preview"].([]any)[1] != "cy@example.test" {
		t.Fatalf("recipients: %v", it)
	}
	m = searchJSON(t, e, "Hello", "--folder", "projects")
	it = items(t, m)[0]
	if it["recipient_count"] != float64(4) || len(it["recipients_preview"].([]any)) != 3 {
		t.Fatalf("preview is cut at three: %v", it)
	}
}

func TestSearchMaxTextCutsMailPreviews(t *testing.T) {
	e := searchMailEnv(t)
	code, out, errOut := e.run("--max-age", "0", "--json", "--max-text", "5", "search", "Hello", "--source", "mail", "--limit", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	it := items(t, decode(t, out))[0]
	if it["text_truncated"] != true || !strings.HasPrefix(it["preview"].(string), "previ") {
		t.Fatalf("preview: %v", it)
	}
}

func TestSearchReportsArchiveFailuresFromEitherSource(t *testing.T) {
	e := searchMailEnv(t)
	e.exec("alter table messages drop column content_html")
	code, _, errOut := e.run("--max-age", "0", "--json", "search", "Hello", "--source", "chats", "--html")
	if code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("a broken chat body column must fail the search: %d %s", code, errOut)
	}
	e.exec("drop table mail_folders")
	code, _, errOut = e.run("--max-age", "0", "--json", "search", "Hello")
	if code == 0 || errorOf(t, errOut)["code"] == nil {
		t.Fatalf("a broken mail table must fail the search: %d %s", code, errOut)
	}
}

func TestSearchAccountNamingAMailAccountNarrowsMailAndSkipsChats(t *testing.T) {
	e := searchMailEnv(t)
	m := searchJSON(t, e, "Hello", "--account", searchMailAccount)
	if m["note"] != "--account names a mail account; Teams chats were not searched" || m["count"] != float64(4) {
		t.Fatalf("mail account: %v", m)
	}
	src := m["sources"].(map[string]any)
	if _, has := src["chats"]; has {
		t.Fatalf("chats were skipped: %v", src)
	}
	// The account narrows: another profile holds none of this mail.
	m = searchJSON(t, e, "Hello", "--account", "outlook/Other")
	if m["count"] != float64(0) {
		t.Fatalf("other account: %v", m)
	}
	// An explicit --source mail needs no note.
	m = searchJSON(t, e, "Hello", "--source", "mail", "--account", searchMailAccount)
	if _, has := m["note"]; has || m["count"] != float64(4) {
		t.Fatalf("explicit source: %v", m)
	}
}

func TestSearchAccountConflictsWithTheOtherSource(t *testing.T) {
	e := searchMailEnv(t)
	teams := tenantA + "/" + userA
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"mail account with a chats-only flag", []string{"Hello", "--account", searchMailAccount, "--mentions-me"}, "--mentions-me"},
		{"mail account with --source chats", []string{"Hello", "--account", searchMailAccount, "--source", "chats"}, "--source chats"},
		{"Teams account with --folder", []string{"Hello", "--account", teams, "--folder", "inbox"}, "--folder"},
		{"Teams account with --source mail", []string{"Hello", "--account", teams, "--source", "mail"}, "--source mail"},
	} {
		er := searchFails(t, e, 2, c.args...)
		if er["code"] != "flag_source_conflict" || !strings.Contains(er["message"].(string), c.want) {
			t.Errorf("%s: %v", c.name, er)
		}
	}
}

// A search mail item has the keys of a mail list item plus source, so the two cannot drift apart.
func TestSearchMailItemHasTheKeysOfAMailListItem(t *testing.T) {
	mailListKeys := jsonKeys(reflect.TypeFor[mailListItem]())
	got := jsonKeys(reflect.TypeFor[searchMailItem]())
	if got[0] != "source" || strings.Join(got[1:], ",") != strings.Join(mailListKeys, ",") {
		t.Fatalf("search mail item keys:\n%v\nmail list item keys:\n%v", got, mailListKeys)
	}
}
