package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/store"
)

func TestSyncJSON(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("sync")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["status"] != "ok" && m["status"] != "ok_with_omissions" {
		t.Fatalf("status = %v", m["status"])
	}
	if _, ok := m["messages"].(map[string]any); !ok {
		t.Fatalf("report has no messages counts: %v", m)
	}
	// An immediate second run is unchanged and still exit 0.
	code, stdout, _ = e.run("sync")
	if code != 0 || decode(t, stdout)["status"] != "unchanged" {
		t.Fatalf("second sync: exit %d, %s", code, stdout)
	}
}

func TestSearchJSONShape(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("search", "Fixture")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	its := items(t, m)
	if len(its) == 0 {
		t.Fatalf("no hits: %s", stdout)
	}
	if int(m["count"].(float64)) != len(its) {
		t.Fatalf("count %v != %d items", m["count"], len(its))
	}
	if _, ok := m["truncated"].(bool); !ok {
		t.Fatalf("truncated missing: %v", m)
	}
	age, ok := m["archive_age_seconds"].(float64)
	if !ok || age < 0 || age > 60 {
		t.Fatalf("archive_age_seconds = %v", m["archive_age_seconds"])
	}
	if _, has := m["sync_error"]; has {
		t.Fatalf("unexpected sync_error: %v", m["sync_error"])
	}
	it := its[0]
	for _, k := range []string{"tenant_id", "user_id", "conversation_id", "conversation_display_name", "id", "sender_id", "sender_name", "sent_at", "message_type", "text", "mentions_me", "pinned"} {
		if _, ok := it[k]; !ok {
			t.Errorf("item lacks %q: %v", k, it)
		}
	}
	if _, has := it["html"]; has {
		t.Error("html must only appear with --html")
	}
	for k := range it {
		if k != strings.ToLower(k) {
			t.Errorf("field %q is not snake_case", k)
		}
	}
}

func TestSearchDefaultLimitAndTruncation(t *testing.T) {
	e := newEnv(t)
	code, stdout, _ := e.run("search", "Fixture", "--limit", "1")
	m := decode(t, stdout)
	if code != 0 || len(items(t, m)) != 1 || m["truncated"] != true {
		t.Fatalf("limit 1: exit %d, %s", code, stdout)
	}
}

func TestUsageErrorExit2(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"search"},
		{"nonesuch"},
		{"messages", "--since", "yesterday-ish"},
		{"messages", "--limit", "0"},
		{"messages", "--fields", "id,bogus"},
		{"messages", "--account", "not-an-account"},
		{"--max-age", "soon", "messages"},
		{"--format", "xml", "messages"},
		{"thread", "only-conversation"},
		{"sql", "delete from messages"},
	} {
		code, stdout, stderr := e.run(append(args, "--json")...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2 (stderr %q)", args, code, stderr)
			continue
		}
		if stdout != "" {
			t.Errorf("%v: stdout must be empty on usage errors: %q", args, stdout)
		}
		er := errorOf(t, stderr)
		if er["code"] != "usage" || er["fix"] == "" || er["message"] == "" {
			t.Errorf("%v: error = %v", args, er)
		}
	}
}

func TestUsageErrorListsValidFields(t *testing.T) {
	e := newEnv(t)
	_, _, stderr := e.run("conversations", "--fields", "nope", "--json")
	msg, _ := errorOf(t, stderr)["message"].(string)
	if !strings.Contains(msg, "nope") || !strings.Contains(msg, "display_name") {
		t.Fatalf("message should name the bad key and list valid keys: %q", msg)
	}
}

func TestEmptyArchiveSearch(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("--max-age", "0", "search", "anything")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["count"].(float64) != 0 || len(items(t, m)) != 0 || m["truncated"] != false {
		t.Fatalf("empty result expected: %s", stdout)
	}
	if v, ok := m["archive_age_seconds"]; !ok || v != nil {
		t.Fatalf("archive_age_seconds must be null with no sync: %v (present %v)", v, ok)
	}
	if !strings.Contains(stderr, "run teamscrawl sync") {
		t.Fatalf("stderr hint missing: %q", stderr)
	}
	if _, err := os.Stat(e.db); err == nil {
		t.Fatal("a read must not create the archive when auto-sync is off")
	}
}

func TestNoFDAJSONError(t *testing.T) {
	skipIfRoot(t)
	e := newEnv(t)
	locked := t.TempDir()
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // G302: restoring a directory so TempDir cleanup works
	e.root = locked
	code, stdout, stderr := e.run("sync", "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout must be empty: %q", stdout)
	}
	er := errorOf(t, stderr)
	if er["code"] != "no_full_disk_access" || !strings.Contains(er["fix"].(string), "Full Disk Access") {
		t.Fatalf("error = %v", er)
	}
	for _, bad := range []string{"goroutine ", "panic:", ".go:"} {
		if strings.Contains(stderr, bad) {
			t.Fatalf("stderr looks like a stack trace (%q): %s", bad, stderr)
		}
	}
}

func TestTeamsMissingExit3(t *testing.T) {
	e := newEnv(t)
	e.root = filepath.Join(t.TempDir(), "absent")
	code, _, stderr := e.run("sync", "--json")
	if code != 3 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if er := errorOf(t, stderr); er["code"] != "teams_not_installed" || er["fix"] == "" {
		t.Fatalf("error = %v", er)
	}
}

func TestTextErrorHasFixLine(t *testing.T) {
	e := newEnv(t)
	e.root = filepath.Join(t.TempDir(), "absent")
	code, _, stderr := e.run("--format", "text", "sync")
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if code != 3 || len(lines) != 2 || !strings.HasPrefix(lines[1], "fix: ") || strings.HasPrefix(stderr, "{") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestLockedExit4(t *testing.T) {
	e := newEnv(t)
	e.sync()
	release, err := store.AcquireLock(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, _, stderr := e.run("sync", "--json")
	if code != 4 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if er := errorOf(t, stderr); er["code"] != "locked" {
		t.Fatalf("error = %v", er)
	}
}

func TestImplicitSyncFailureStillReads(t *testing.T) {
	e := newEnv(t)
	e.sync()
	release, err := store.AcquireLock(e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, stdout, stderr := e.run("--max-age", "1ns", "search", "Fixture", "--json")
	if code != 0 {
		t.Fatalf("read must succeed on the existing archive: exit %d, %s", code, stderr)
	}
	m := decode(t, stdout)
	se, ok := m["sync_error"].(map[string]any)
	if !ok || se["code"] != "locked" || se["message"] == "" {
		t.Fatalf("sync_error = %v", m["sync_error"])
	}
	if len(items(t, m)) == 0 {
		t.Fatal("expected hits from the existing archive")
	}
	if !strings.Contains(stderr, `"locked"`) {
		t.Fatalf("warning missing on stderr: %q", stderr)
	}
}

func TestImplicitSyncNoTeamsEmptyArchive(t *testing.T) {
	e := newEnv(t)
	e.root = filepath.Join(t.TempDir(), "absent")
	code, stdout, stderr := e.run("messages", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["count"].(float64) != 0 || m["sync_error"].(map[string]any)["code"] != "teams_not_installed" {
		t.Fatalf("result = %v", m)
	}
	if !strings.Contains(stderr, "run teamscrawl sync") {
		t.Fatalf("hint missing: %q", stderr)
	}
}

func syncRunCount(t *testing.T, e *env) int {
	t.Helper()
	_, stdout, _ := e.run("--max-age", "0", "sql", "select count(*) as n from sync_runs")
	rows := decode(t, stdout)["rows"].([]any)
	return int(rows[0].([]any)[0].(float64))
}

func TestMaxAgeControlsImplicitSync(t *testing.T) {
	e := newEnv(t)
	e.run("messages") // first read syncs (default 15m, no successful sync yet)
	if n := syncRunCount(t, e); n != 1 {
		t.Fatalf("runs after first read = %d", n)
	}
	e.run("messages") // fresh: no new sync
	if n := syncRunCount(t, e); n != 1 {
		t.Fatalf("a fresh archive must not sync again, runs = %d", n)
	}
	e.run("--max-age", "1ns", "messages") // stale: syncs (unchanged)
	if n := syncRunCount(t, e); n != 2 {
		t.Fatalf("a stale archive must sync, runs = %d", n)
	}
	e.run("--max-age", "0", "messages")
	if n := syncRunCount(t, e); n != 2 {
		t.Fatalf("--max-age 0 must not sync, runs = %d", n)
	}
	t.Setenv("TEAMSCRAWL_MAX_AGE", "1ns")
	e.run("messages")
	if n := syncRunCount(t, e); n != 3 {
		t.Fatalf("TEAMSCRAWL_MAX_AGE must set the default, runs = %d", n)
	}
}

func TestImplicitSyncProgressNotOnStdout(t *testing.T) {
	e := newEnv(t)
	_, stdout, stderr := e.run("messages")
	decode(t, stdout) // exactly one JSON document
	if strings.Contains(stderr, "progress") || strings.Contains(stderr, "|") {
		t.Fatalf("progress must stay off a non-TTY stderr: %q", stderr)
	}
}

func TestFormatDefaultNonTTY(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "conversations")
	decode(t, stdout)
	code, stdout, _ := e.run("--max-age", "0", "--format", "text", "conversations")
	if code != 0 || strings.HasPrefix(strings.TrimSpace(stdout), "{") || !strings.Contains(stdout, "Fixture") {
		t.Fatalf("text format: exit %d, %q", code, stdout)
	}
	_, stdout, _ = e.run("--max-age", "0", "--format", "log", "conversations")
	if !strings.HasPrefix(stdout, "conversations={") {
		t.Fatalf("log format: %q", stdout)
	}
}

func TestSQLReadOnly(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "sql", "select count(*) as n from messages")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	if m["columns"].([]any)[0] != "n" || m["rows"].([]any)[0].([]any)[0].(float64) < 100 {
		t.Fatalf("sql result = %s", stdout)
	}
	for _, q := range []string{"delete from messages", "insert into people(tenant_id,id) values('a','b')", "drop table messages", "attach database 'x.db' as x", "select 1; delete from messages"} {
		code, _, stderr := e.run("--max-age", "0", "sql", q, "--json")
		if code == 0 {
			t.Errorf("%q must fail", q)
			continue
		}
		if code != 2 {
			t.Errorf("%q: exit %d, stderr %s", q, code, stderr)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "sql", "select count(*) as n from messages")
	if decode(t, stdout)["rows"].([]any)[0].([]any)[0].(float64) < 100 {
		t.Fatal("archive was modified")
	}
}

func TestMessagesByConversationTitle(t *testing.T) {
	e := newEnv(t)
	e.sync()
	code, stdout, stderr := e.run("--max-age", "0", "messages", "--conversation", "Fixture chat 1", "--account", tenantA+"/"+userA)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	its := items(t, decode(t, stdout))
	if len(its) == 0 {
		t.Fatal("no messages for the title")
	}
	prev := ""
	for _, it := range its {
		if it["conversation_display_name"] != "Fixture chat 1" || it["user_id"] != userA {
			t.Fatalf("wrong item %v", it)
		}
		at := it["sent_at"].(string)
		if at < prev {
			t.Fatal("messages must be chronological")
		}
		prev = at
	}
}

func TestIncludeDeleted(t *testing.T) {
	e := newEnv(t)
	e.sync()
	has := func(args ...string) (bool, map[string]any) {
		t.Helper()
		_, stdout, _ := e.run(append([]string{"--max-age", "0"}, args...)...)
		for _, it := range items(t, decode(t, stdout)) {
			if it["id"] == "1700000012000" && it["user_id"] == userA {
				return true, it
			}
		}
		return false, nil
	}
	if ok, _ := has("messages", "--conversation", "19:space1@thread.v2"); ok {
		t.Fatal("deleted message listed by default")
	}
	if ok, _ := has("search", "deleted"); ok {
		t.Fatal("deleted message searchable by default")
	}
	ok, it := has("messages", "--conversation", "19:space1@thread.v2", "--include-deleted")
	if !ok || it["deleted_at"] == nil || it["deleted_at"] == "" {
		t.Fatalf("--include-deleted must list it with deleted_at: %v", it)
	}
	if ok, _ := has("search", "deleted", "--include-deleted"); !ok {
		t.Fatal("--include-deleted search must find it")
	}
}

func TestMentionsMeAndUnread(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "messages", "--mentions-me", "--account", tenantA+"/"+userA)
	its := items(t, decode(t, stdout))
	if len(its) == 0 {
		t.Fatal("expected mentions of me")
	}
	for _, it := range its {
		if it["mentions_me"] != true {
			t.Fatalf("not a mention: %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "search", "review", "--mentions-me")
	if len(items(t, decode(t, stdout))) == 0 {
		t.Fatal("search --mentions-me found nothing")
	}
	code, stdout, stderr := e.run("--max-age", "0", "unread", "--account", tenantA+"/"+userA)
	if code != 0 {
		t.Fatalf("unread: %s", stderr)
	}
	un := items(t, decode(t, stdout))
	if len(un) == 0 {
		t.Fatal("expected unread messages")
	}
	for _, it := range un {
		if it["sender_id"] == "8:orgid:"+userA {
			t.Fatalf("own message counted unread: %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "messages", "--unread", "--account", tenantA+"/"+userA)
	if len(items(t, decode(t, stdout))) != len(un) {
		t.Fatal("messages --unread and unread disagree")
	}
}

func TestActivity(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "activity")
	all := items(t, decode(t, stdout))
	if len(all) == 0 {
		t.Fatal("no activity")
	}
	for _, k := range []string{"id", "type", "is_read", "at", "conversation_display_name", "text", "sender_name"} {
		if _, ok := all[0][k]; !ok {
			t.Errorf("activity item lacks %q: %v", k, all[0])
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "activity", "--unread", "--type", "mentionInChat")
	for _, it := range items(t, decode(t, stdout)) {
		if it["is_read"] != false || it["type"] != "mentionInChat" {
			t.Fatalf("filter ignored: %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "activity", "--since", "1h")
	if len(items(t, decode(t, stdout))) != 0 {
		t.Fatal("fixture activity is years old; --since 1h must exclude it")
	}
}

func TestThreadByIDAndLink(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "thread", "19:topicchannel1@thread.tacv2", "1700000045000", "--account", tenantA+"/"+userA)
	its := items(t, decode(t, stdout))
	if len(its) != 2 || its[0]["id"] != "1700000045000" || its[1]["id"] != "1700000046000" {
		t.Fatalf("thread = %v", its)
	}
	if its[0]["subject"] == "" || its[0]["subject"] == nil {
		t.Fatalf("root should carry its subject: %v", its[0])
	}
	link := its[1]["link"].(string)
	if !strings.HasPrefix(link, "https://teams.microsoft.com/l/message/") {
		t.Fatalf("link = %q", link)
	}
	_, stdout, _ = e.run("--max-age", "0", "thread", link, "--account", tenantA+"/"+userA)
	byLink := items(t, decode(t, stdout))
	if len(byLink) != 2 || byLink[0]["id"] != "1700000045000" {
		t.Fatalf("thread by reply deep link must resolve the root via parentMessageId: %v", byLink)
	}
}

func TestWhoamiAndStatus(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("whoami")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	m := decode(t, stdout)
	accts := m["accounts"].([]any)
	if len(accts) != 2 || accts[0].(map[string]any)["tenant_id"] == "" {
		t.Fatalf("whoami = %s", stdout)
	}
	if _, ok := m["archive_age_seconds"]; !ok {
		t.Fatal("archive_age_seconds missing")
	}
	code, stdout, _ = e.run("--max-age", "0", "status")
	st := decode(t, stdout)
	if code != 0 || st["schema_version"].(float64) < 1 || len(st["accounts"].([]any)) != 2 {
		t.Fatalf("status = %s", stdout)
	}
	if _, ok := st["archive_age_seconds"]; !ok {
		t.Fatal("status lacks archive_age_seconds")
	}
}

func TestStatusEmptyArchive(t *testing.T) {
	e := newEnv(t)
	code, stdout, stderr := e.run("--max-age", "0", "status")
	if code != 0 || !strings.Contains(stderr, "run teamscrawl sync") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if m := decode(t, stdout); m["archive_exists"] != false {
		t.Fatalf("status = %v", m)
	}
}

func TestConversationsAndPeople(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "conversations", "--kind", "Chat")
	cs := items(t, decode(t, stdout))
	if len(cs) == 0 {
		t.Fatal("no conversations")
	}
	for _, c := range cs {
		if c["kind"] != "Chat" {
			t.Fatalf("kind filter: %v", c)
		}
		if _, ok := c["member_count"]; !ok {
			t.Fatalf("member_count missing: %v", c)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "conversations", "--query", "standup")
	if len(items(t, decode(t, stdout))) == 0 {
		t.Fatal("conversation query found nothing")
	}
	_, stdout, _ = e.run("--max-age", "0", "people", "--query", "alex")
	ps := items(t, decode(t, stdout))
	if len(ps) == 0 || ps[0]["display_name"] == nil {
		t.Fatalf("people = %v", ps)
	}
}

func TestFieldsAndMaxText(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "--fields", "id,text", "--max-text", "5", "search", "Hello")
	its := items(t, decode(t, stdout))
	if len(its) == 0 {
		t.Fatal("no hits")
	}
	for _, it := range its {
		if len(it) != 3 { // id, text, text_truncated
			t.Fatalf("projection kept extra keys: %v", it)
		}
		txt := it["text"].(string)
		if txt != "Hello…" || it["text_truncated"] != true {
			t.Fatalf("truncation: %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "--fields", "id", "--max-text", "500", "search", "Hello")
	for _, it := range items(t, decode(t, stdout)) {
		if len(it) != 1 {
			t.Fatalf("fields id only: %v", it)
		}
	}
	_, stdout, _ = e.run("--max-age", "0", "--max-text", "500", "search", "Hello")
	for _, it := range items(t, decode(t, stdout)) {
		if _, has := it["text_truncated"]; has {
			t.Fatalf("short text must not be flagged: %v", it)
		}
	}
}

func TestHTMLFlag(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "search", "Hello", "--html")
	for _, it := range items(t, decode(t, stdout)) {
		if h, _ := it["html"].(string); !strings.Contains(h, "<p>") {
			t.Fatalf("html missing: %v", it)
		}
	}
}

func TestAccountFilter(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "messages", "--conversation", "19:shared-fixture-conversation@thread.v2")
	both := items(t, decode(t, stdout))
	_, stdout, _ = e.run("--max-age", "0", "messages", "--conversation", "19:shared-fixture-conversation@thread.v2", "--account", tenantA+"/"+userA)
	one := items(t, decode(t, stdout))
	if len(one) == 0 || len(both) != 2*len(one) {
		t.Fatalf("both=%d one=%d", len(both), len(one))
	}
	for _, it := range one {
		if it["tenant_id"] != tenantA || it["user_id"] != userA {
			t.Fatalf("leak: %v", it)
		}
	}
}

func TestSinceUntil(t *testing.T) {
	e := newEnv(t)
	e.sync()
	_, stdout, _ := e.run("--max-age", "0", "messages", "--since", "2023-11-14", "--until", "2023-11-16T00:00:00Z", "--limit", "500")
	if len(items(t, decode(t, stdout))) == 0 {
		t.Fatal("date range should match fixture messages (Nov 2023)")
	}
	_, stdout, _ = e.run("--max-age", "0", "messages", "--since", "7d")
	if len(items(t, decode(t, stdout))) != 0 {
		t.Fatal("relative since must count back from now")
	}
}

func TestMalformedLinkNoPanic(t *testing.T) {
	e := newEnv(t)
	code, _, stderr := e.run("--max-age", "0", "thread", "https://teams.microsoft.com/l/message/%zz/1", "--json")
	if code != 2 || strings.Contains(stderr, "goroutine") || errorOf(t, stderr)["code"] != "usage" {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	e := newEnv(t)
	panicHook = func() { panic("boom") }
	t.Cleanup(func() { panicHook = nil })
	code, stdout, stderr := e.run("version", "--json")
	if code != 1 || stdout != "" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
	er := errorOf(t, stderr)
	if er["code"] != "internal" || !strings.Contains(er["fix"].(string), "TEAMSCRAWL_DEBUG=1") || !strings.Contains(er["fix"].(string), "github.com/ourostack/teamscrawl/issues") {
		t.Fatalf("error = %v", er)
	}
	if strings.Contains(stderr, "goroutine") {
		t.Fatalf("no stack without TEAMSCRAWL_DEBUG: %s", stderr)
	}
	t.Setenv("TEAMSCRAWL_DEBUG", "1")
	code, _, stderr = e.run("version", "--json")
	lines := strings.SplitN(stderr, "\n", 2)
	if code != 1 || !strings.HasPrefix(lines[0], `{"error"`) || !strings.Contains(lines[1], "goroutine") {
		t.Fatalf("debug: exit %d, stderr %q", code, stderr)
	}
}

func TestCommandSpecificUsageFix(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"search"}, []string{"teamscrawl search --help"}},
		{[]string{"messages", "--since", "zzz"}, []string{"teamscrawl messages --help"}},
		{[]string{"--max-age", "0", "sql", "delete from messages"}, []string{"read-only by design", "SELECT"}},
		{[]string{"--max-age", "0", "messages", "--fields", "bogus"}, []string{"Pick keys from the list in the message"}},
	}
	for _, c := range cases {
		code, _, stderr := e.run(append(c.args, "--json")...)
		fix, _ := errorOf(t, stderr)["fix"].(string)
		if code != 2 {
			t.Errorf("%v: exit %d", c.args, code)
		}
		for _, w := range c.want {
			if !strings.Contains(fix, w) {
				t.Errorf("%v: fix %q lacks %q", c.args, fix, w)
			}
		}
	}
}

func TestFieldsMaxTextOnNonListCommands(t *testing.T) {
	e := newEnv(t)
	e.sync()
	for _, cmd := range []string{"status", "whoami", "sync", "doctor", "sql"} {
		for _, flag := range [][]string{{"--fields", "id"}, {"--max-text", "5"}} {
			args := append([]string{"--max-age", "0", cmd}, flag...)
			if cmd == "sql" {
				args = append(args, "select 1")
			}
			code, _, stderr := e.run(append(args, "--json")...)
			er := errorOf(t, stderr)
			msg, _ := er["message"].(string)
			if code != 2 || er["code"] != "usage" || !strings.Contains(msg, "list commands") || !strings.Contains(msg, "search") {
				t.Errorf("%v: exit %d, %v", args, code, er)
			}
		}
	}
}

func TestLinkBytesIdenticalWithAndWithoutFields(t *testing.T) {
	e := newEnv(t)
	e.sync()
	linkOf := func(args ...string) string {
		t.Helper()
		_, stdout, _ := e.run(append([]string{"--max-age", "0", "messages", "--conversation", "Fixture chat 1", "--limit", "1"}, args...)...)
		i := strings.Index(stdout, `"link":"`)
		if i < 0 {
			t.Fatalf("no link in %s", stdout)
		}
		rest := stdout[i:]
		return rest[:strings.Index(rest[8:], `"`)+9]
	}
	plain, fields := linkOf(), linkOf("--fields", "id,link")
	if plain != fields || !strings.Contains(plain, "&context=") || strings.Contains(plain, `\u0026`) {
		t.Fatalf("plain %q vs fields %q", plain, fields)
	}
}
