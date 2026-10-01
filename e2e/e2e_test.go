//go:build e2e

package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "teamscrawl-e2e-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mkdir temp:", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binary = filepath.Join(dir, "teamscrawl")
	build := exec.Command( //nolint:gosec // fixed arguments; binary path is a temp dir we created
		"go", "build", "-ldflags", "-X github.com/ourostack/teamscrawl/internal/cli.version=e2e", "-o", binary, "../cmd/teamscrawl")
	build.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

func TestVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, "version")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("teamscrawl version: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "e2e" {
		t.Fatalf("version output = %q, want %q", got, "e2e")
	}
}

// SIGTERM while watch is mid-sync (held after the snapshot) exits 0 and removes the snapshot.
func TestWatchSIGTERM(t *testing.T) {
	tmp := t.TempDir()
	root, err := filepath.Abs("../testdata/teams-fixture/EBWebView")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary, "watch", "--every", "1h", "--db", filepath.Join(tmp, "a.db"), "--teams-root", root, "--json") //nolint:gosec // G204: binary is the one this test built
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT=30s")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(tmp, "teamscrawl-snapshot-*")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if m, _ := filepath.Glob(snap); len(m) > 0 {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("no snapshot appeared\nstderr: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("watch after SIGTERM: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if m, _ := filepath.Glob(snap); len(m) != 0 {
		t.Fatalf("snapshot left behind: %v", m)
	}
}

const (
	chat1 = "19:00000000-0000-4000-8000-0000000000a1_00000000-0000-4000-8000-0000000000ff@unq.gbl.spaces"
	chat2 = "19:00000000-0000-4000-8000-0000000000a2_00000000-0000-4000-8000-0000000000ff@unq.gbl.spaces"

	channel1 = "19:topicchannel1@thread.tacv2"
)

func num(t *testing.T, m map[string]any, key string) int {
	t.Helper()
	f, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%q is not a number in %v", key, m)
	}
	return int(f)
}

// goldenCount is the number of entries in one of the fixture's golden files; after one sync the
// archive holds exactly that many of each kind, both accounts together.
func goldenCount(t *testing.T, name string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "teams-fixture", "expected", name)) //nolint:gosec // G304: fixed golden file names
	if err != nil {
		t.Fatal(err)
	}
	var v []json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return len(v)
}

func TestE2EFreshMachine(t *testing.T) {
	e := newEnv(t)
	root := []string{"--teams-root", e.root}

	doc := ok(t, e.run(append([]string{"doctor"}, root...)...))
	if doc["ok"] != true {
		t.Fatalf("doctor not ok: %v", doc)
	}
	checks, _ := doc["checks"].([]any)
	if len(checks) == 0 {
		t.Fatal("doctor printed no checks")
	}
	for _, c := range checks {
		m, _ := c.(map[string]any)
		if m["ok"] != true {
			t.Fatalf("check failed: %v", m)
		}
	}

	// No --db: the archive lands at the default path under the fresh HOME.
	rep := ok(t, e.run(append([]string{"sync"}, root...)...))
	if rep["status"] != "ok" {
		t.Fatalf("sync status = %v", rep["status"])
	}
	if msgs, _ := rep["messages"].(map[string]any); num(t, msgs, "inserted") != goldenCount(t, "mapped-messages.json") {
		t.Fatalf("messages inserted = %v, golden %d", msgs, goldenCount(t, "mapped-messages.json"))
	}

	st := ok(t, e.run(append([]string{"status", "--json"}, root...)...))
	if want := filepath.Join(e.home, ".teamscrawl", "teamscrawl.db"); st["archive_path"] != want {
		t.Fatalf("archive_path = %v, want %s", st["archive_path"], want)
	}
	accts, _ := st["accounts"].([]any)
	if len(accts) != 2 {
		t.Fatalf("want 2 accounts, got %v", st["accounts"])
	}
	var convs, msgs, people, acts int
	for _, a := range accts {
		m, _ := a.(map[string]any)
		if num(t, m, "conversations") != 7 || num(t, m, "messages") != 52 || num(t, m, "people") != 3 || num(t, m, "activity") != 8 {
			t.Fatalf("account counts = %v, want 7/52/3/8", m)
		}
		convs += num(t, m, "conversations")
		msgs += num(t, m, "messages")
		people += num(t, m, "people")
		acts += num(t, m, "activity")
	}
	if convs != goldenCount(t, "mapped-conversations.json") || msgs != goldenCount(t, "mapped-messages.json") || acts != goldenCount(t, "mapped-activity.json") {
		t.Fatalf("totals %d/%d/%d do not match the golden files", convs, msgs, acts)
	}
	_ = people

	items, _ := list(t, e.run(append([]string{"search", "Hello from Alex"}, root...)...))
	if len(items) != 1 || items[0]["text"] != "Hello from Alex Fixture" || items[0]["conversation_display_name"] != "Fixture chat 1" {
		t.Fatalf("search items = %v", items)
	}

	items, _ = list(t, e.run(append([]string{"messages", "--conversation", "Fixture chat 1"}, root...)...))
	if len(items) < 10 {
		t.Fatalf("want the chat's messages, got %d", len(items))
	}
	ids := strs(items, "id")
	if !sort.StringsAreSorted(ids) || ids[0] != "1700000001000" {
		t.Fatalf("messages are not oldest first: %v", ids)
	}
	sent := strs(items, "sent_at")
	if !sort.StringsAreSorted(sent) {
		t.Fatalf("sent_at is not ascending: %v", sent)
	}
	for _, it := range items {
		if it["conversation_id"] != chat1 || it["tenant_id"] != tenant1 {
			t.Fatalf("a message from another conversation or account: %v", it)
		}
	}

	items, _ = list(t, e.run(append([]string{"conversations", "--kind", "chat"}, root...)...))
	if len(items) == 0 {
		t.Fatal("no chats")
	}
	for _, it := range items {
		if it["kind"] != "Chat" {
			t.Fatalf("--kind chat returned %v", it["kind"])
		}
	}
	if names := strs(items, "display_name"); !contains(names, "Fixture chat 1") || !contains(names, "Fixture chat 2") {
		t.Fatalf("chats = %v", names)
	}

	items, _ = list(t, e.run(append([]string{"people", "--query", "Pat"}, root...)...))
	if len(items) == 0 {
		t.Fatal("people --query Pat found nobody")
	}
	for _, it := range items {
		if it["display_name"] != "Pat Example" {
			t.Fatalf("person = %v", it)
		}
	}

	res := ok(t, e.run(append([]string{"sql", "select count(*) from messages"}, root...)...))
	if rows, _ := res["rows"].([]any); len(rows) != 1 || rows[0].([]any)[0] != float64(104) {
		t.Fatalf("sql rows = %v", res["rows"])
	}
}

func TestE2EIdempotent(t *testing.T) {
	e := newEnv(t)
	first := e.sync()
	if first["status"] != "ok" {
		t.Fatalf("first sync = %v", first["status"])
	}
	second := e.sync()
	if second["status"] != "unchanged" {
		t.Fatalf("second sync status = %v, want unchanged", second["status"])
	}
	if m, _ := second["messages"].(map[string]any); num(t, m, "inserted") != 0 || num(t, m, "updated") != 0 {
		t.Fatalf("second sync changed messages: %v", m)
	}
	res := ok(t, e.cmd("sql", "select count(*) from messages"))
	if rows, _ := res["rows"].([]any); rows[0].([]any)[0] != float64(104) {
		t.Fatalf("rows after two syncs = %v", res["rows"])
	}
}

func TestE2ETwoAccounts(t *testing.T) {
	e := newEnv(t)
	e.sync()

	who := ok(t, e.cmd("whoami"))
	accts, _ := who["accounts"].([]any)
	if len(accts) != 2 {
		t.Fatalf("whoami accounts = %v", who["accounts"])
	}
	names := map[string]string{}
	for _, a := range accts {
		m, _ := a.(map[string]any)
		names[m["tenant_id"].(string)+"/"+m["user_id"].(string)] = m["display_name"].(string)
	}
	if names[account1] != "Alex Fixture" || names[account2] != "Blair Fixture" {
		t.Fatalf("whoami names = %v", names)
	}

	// The shared conversation id exists once per account and stays partitioned.
	all, _ := list(t, e.cmd("conversations"))
	var shared int
	for _, it := range all {
		if it["id"] == "19:shared-fixture-conversation@thread.v2" {
			shared++
		}
	}
	if shared != 2 {
		t.Fatalf("the shared conversation id should appear once per account, got %d", shared)
	}

	for _, tc := range []struct{ account, tenant, user, ownChat, otherChat string }{
		{account1, tenant1, user1, "Fixture chat 1", "Fixture chat 2"},
		{account2, tenant2, user2, "Fixture chat 2", "Fixture chat 1"},
	} {
		items, _ := list(t, e.cmd("conversations", "--account", tc.account))
		if len(items) != 7 {
			t.Fatalf("%s: %d conversations, want 7", tc.account, len(items))
		}
		for _, it := range items {
			if it["tenant_id"] != tc.tenant || it["user_id"] != tc.user {
				t.Fatalf("--account %s leaked %v", tc.account, it)
			}
		}
		own, _ := list(t, e.cmd("messages", "-c", tc.ownChat, "--account", tc.account))
		if len(own) == 0 {
			t.Fatalf("%s sees none of its own chat", tc.account)
		}
		other, _ := list(t, e.cmd("messages", "-c", tc.otherChat, "--account", tc.account))
		if len(other) != 0 {
			t.Fatalf("%s sees the other account's chat: %v", tc.account, other)
		}
		hits, _ := list(t, e.cmd("search", "Hello", "--account", tc.account))
		if len(hits) != 1 || hits[0]["tenant_id"] != tc.tenant {
			t.Fatalf("%s search = %v", tc.account, hits)
		}
	}
	// The user id may carry the 8:orgid: prefix.
	items, _ := list(t, e.cmd("conversations", "--account", tenant2+"/8:orgid:"+user2))
	if len(items) != 7 {
		t.Fatalf("prefixed account id: %d conversations", len(items))
	}
	wantError(t, e.cmd("conversations", "--account", "nonsense"), 2, "usage")
}

func TestE2EErrors(t *testing.T) {
	t.Run("no full disk access", func(t *testing.T) {
		skipIfRoot(t)
		e := newEnv(t)
		if err := os.Chmod(e.root, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(e.root, 0o700) }) //nolint:gosec // G302: a directory must be searchable
		wantError(t, e.cmd("sync"), 3, "no_full_disk_access")
		wantError(t, e.cmd("sync", "--max-age", "0"), 3, "no_full_disk_access")

		// doctor reports the failing check on stdout and fails with doctor_failed on stderr.
		res := e.cmd("doctor")
		mustExit(t, res, 3)
		doc := mustJSON(t, res.stdout)
		if doc["ok"] != false {
			t.Fatalf("doctor ok = %v", doc["ok"])
		}
		var failing []string
		for _, c := range doc["checks"].([]any) {
			m := c.(map[string]any)
			if m["ok"] == false {
				failing = append(failing, m["name"].(string))
				if m["fix"] == "" {
					t.Fatalf("failing check without a fix: %v", m)
				}
			}
		}
		if !contains(failing, "full_disk_access") {
			t.Fatalf("failing checks = %v", failing)
		}
		wantError(t, result{stderr: res.stderr}, 0, "doctor_failed")
	})

	t.Run("teams not installed", func(t *testing.T) {
		e := newEnv(t)
		e.root = filepath.Join(filepath.Dir(e.root), "missing")
		wantError(t, e.cmd("sync"), 3, "teams_not_installed")
		res := e.cmd("doctor")
		mustExit(t, res, 3)
		if doc := mustJSON(t, res.stdout); doc["ok"] != false {
			t.Fatalf("doctor ok = %v", doc["ok"])
		}
		wantError(t, result{stderr: res.stderr}, 0, "doctor_failed")
	})

	t.Run("no teams origin", func(t *testing.T) {
		e := newEnv(t)
		empty := filepath.Join(filepath.Dir(e.root), "empty")
		if err := os.MkdirAll(filepath.Join(empty, "WV2Profile_x", "IndexedDB"), 0o700); err != nil {
			t.Fatal(err)
		}
		e.root = empty
		wantError(t, e.cmd("sync"), 3, "no_teams_origin")
	})

	t.Run("usage", func(t *testing.T) {
		e := newEnv(t)
		for _, args := range [][]string{
			{"bogus"},
			{"search"},
			{"messages", "--limit", "0"},
			{"messages", "--format", "xml"},
			{"messages", "--since", "yesterday-ish"},
			{"messages", "--max-age", "soon"},
			{"sync", "--fields", "id"},
			{"messages", "--fields", "nope"},
			{"thread", chat1},
			{"sql", "delete from messages"},
		} {
			e.sync() // the archive exists, so only the arguments can be at fault
			wantError(t, e.cmd(args...), 2, "usage")
		}
	})

	t.Run("archive locked", func(t *testing.T) {
		e := newEnv(t)
		holder := e.start([]string{"TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT=30s"}, append([]string{"sync"}, e.baseArgs()...)...)
		holder.waitFor("the snapshot", func() bool { return len(snapshots(t, e.tmp)) > 0 })
		errBody := wantError(t, e.cmd("sync"), 4, "locked")
		if !strings.Contains(errBody["message"].(string), ".lock") {
			t.Fatalf("locked message = %v", errBody["message"])
		}
		holder.signal(syscall.SIGINT)
	})

	t.Run("cache cannot be copied consistently", func(t *testing.T) {
		e := newEnv(t)
		matches, _ := filepath.Glob(filepath.Join(e.root, "*", "IndexedDB", "*.leveldb", "MANIFEST-*"))
		if len(matches) != 1 {
			t.Fatalf("manifest files: %v", matches)
		}
		if err := os.Remove(matches[0]); err != nil {
			t.Fatal(err)
		}
		wantError(t, e.cmd("sync"), 1, "snapshot_inconsistent")
		// The attempt is recorded as failed and the archive is still readable.
		// (stderr carries the "run teamscrawl sync" hint: no run has succeeded.)
		res := mustJSON(t, mustRun(t, e.cmd("sql", "--max-age", "0", "select status from sync_runs")))
		rows, _ := res["rows"].([]any)
		if len(rows) != 1 || rows[0].([]any)[0] != "failed" {
			t.Fatalf("sync_runs = %v", res["rows"])
		}
	})

	t.Run("a bad flag value never prints a stack trace", func(t *testing.T) {
		e := newEnv(t)
		res := e.run("sync", "--db", filepath.Join(e.tmp, "nodir", "a", "b.db"), "--teams-root", filepath.Join(e.tmp, "missing"))
		wantError(t, res, 3, "teams_not_installed")
	})
}

func TestE2EDBModes(t *testing.T) {
	e := newEnv(t)
	e.sync()
	dir := filepath.Dir(e.db)
	for path, want := range map[string]os.FileMode{dir: 0o700, e.db: 0o600} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
	// Companion files (WAL, lock) must not be wider than the archive.
	for _, g := range []string{e.db + "-*", e.db + ".lock"} {
		m, _ := filepath.Glob(g)
		for _, p := range m {
			if fi, err := os.Stat(p); err == nil && fi.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s mode = %o, want no group or other access", p, fi.Mode().Perm())
			}
		}
	}
	// The default location under HOME gets the same modes.
	d := newEnv(t)
	mustExit(t, d.run("sync", "--teams-root", d.root), 0)
	for path, want := range map[string]os.FileMode{
		filepath.Join(d.home, ".teamscrawl"):                  0o700,
		filepath.Join(d.home, ".teamscrawl", "teamscrawl.db"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestE2ENoSnapshotLeft(t *testing.T) {
	t.Run("after success", func(t *testing.T) {
		e := newEnv(t)
		e.sync()
		if m := snapshots(t, e.tmp); len(m) != 0 {
			t.Fatalf("snapshot left behind: %v", m)
		}
	})
	t.Run("after failure", func(t *testing.T) {
		e := newEnv(t)
		matches, _ := filepath.Glob(filepath.Join(e.root, "*", "IndexedDB", "*.leveldb", "MANIFEST-*"))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				t.Fatal(err)
			}
		}
		mustExit(t, e.cmd("sync"), 1)
		if m := snapshots(t, e.tmp); len(m) != 0 {
			t.Fatalf("snapshot left behind: %v", m)
		}
	})
	t.Run("after SIGINT mid-sync", func(t *testing.T) {
		e := newEnv(t)
		s := e.start([]string{"TEAMSCRAWL_TEST_PAUSE_AFTER_SNAPSHOT=5s"}, append([]string{"sync"}, e.baseArgs()...)...)
		s.waitFor("the snapshot", func() bool { return len(snapshots(t, e.tmp)) > 0 })
		fi, err := os.Stat(snapshots(t, e.tmp)[0])
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("snapshot dir mode = %o, want 700", fi.Mode().Perm())
		}
		start := time.Now()
		res := s.signal(syscall.SIGINT)
		if time.Since(start) > 4*time.Second {
			t.Errorf("SIGINT took %v; the pause was not interrupted", time.Since(start))
		}
		if res.code == 0 {
			t.Errorf("an interrupted sync must not exit 0")
		}
		if m := snapshots(t, e.tmp); len(m) != 0 {
			t.Fatalf("snapshot left behind after SIGINT: %v", m)
		}
		// The interrupted run released the lock: the next sync works.
		e.sync()
	})
}

func TestE2EActivity(t *testing.T) {
	e := newEnv(t)
	e.sync()

	all, _ := list(t, e.cmd("activity"))
	if len(all) != 16 {
		t.Fatalf("activity items = %d, want 16", len(all))
	}
	for _, k := range []string{"id", "type", "is_read", "at", "conversation_display_name", "sender_name", "text"} {
		if _, has := all[0][k]; !has {
			t.Errorf("activity item has no %q: %v", k, all[0])
		}
	}
	unread, _ := list(t, e.cmd("activity", "--unread"))
	if len(unread) != 10 {
		t.Fatalf("unread activity = %d, want 10", len(unread))
	}
	for _, it := range unread {
		if it["is_read"] != false {
			t.Fatalf("--unread returned a read item: %v", it)
		}
	}
	mentions, _ := list(t, e.cmd("activity", "--type", "mentionInChat"))
	if len(mentions) != 2 || mentions[0]["text"] != "Alex Fixture and Sam Tag see this" {
		t.Fatalf("mentionInChat = %v", mentions)
	}
	one, _ := list(t, e.cmd("activity", "--account", account1))
	if len(one) != 8 {
		t.Fatalf("account 1 activity = %d, want 8", len(one))
	}
	newest, whole := list(t, e.cmd("activity", "--limit", "1"))
	if len(newest) != 1 || whole["truncated"] != true {
		t.Fatalf("--limit 1 = %v truncated=%v", newest, whole["truncated"])
	}
}

func TestE2EUnread(t *testing.T) {
	e := newEnv(t)
	e.sync()

	def, whole := list(t, e.cmd("unread"))
	if len(def) != 12 || whole["channels_excluded"] != true {
		t.Fatalf("default unread = %d items, channels_excluded=%v; want 12 and true", len(def), whole["channels_excluded"])
	}
	for _, it := range def {
		if it["conversation_id"] == channel1 || it["conversation_id"] == "19:planningchannel1@thread.tacv2" {
			t.Fatalf("a channel message in the default unread list: %v", it)
		}
	}
	inc, whole := list(t, e.cmd("unread", "--include-channels"))
	if len(inc) != 16 {
		t.Fatalf("--include-channels = %d, want 16", len(inc))
	}
	if _, has := whole["channels_excluded"]; has {
		t.Fatalf("channels_excluded must be absent with --include-channels: %v", whole)
	}

	by, whole := list(t, e.cmd("unread", "--by-conversation"))
	if len(by) != 2 || whole["channels_excluded"] != true {
		t.Fatalf("--by-conversation = %v", by)
	}
	for _, it := range by {
		if num(t, it, "unread_count") != 6 || it["kind"] != "Chat" || !strings.HasPrefix(it["link"].(string), "https://teams.microsoft.com/l/message/") {
			t.Fatalf("by-conversation item = %v", it)
		}
		for _, k := range []string{"conversation_id", "conversation_display_name", "oldest_unread_at", "newest_unread_at"} {
			if _, has := it[k]; !has {
				t.Fatalf("by-conversation item has no %q: %v", k, it)
			}
		}
	}
	byAll, _ := list(t, e.cmd("unread", "--by-conversation", "--include-channels"))
	if len(byAll) != 4 {
		t.Fatalf("--by-conversation --include-channels = %d, want 4", len(byAll))
	}
	one, _ := list(t, e.cmd("unread", "--account", account2, "--fields", "tenant_id"))
	if len(one) != 6 {
		t.Fatalf("account 2 unread = %d, want 6", len(one))
	}

	// messages --unread follows the same rule.
	mu, whole := list(t, e.cmd("messages", "--unread"))
	if len(mu) != 12 || whole["channels_excluded"] != true {
		t.Fatalf("messages --unread = %d channels_excluded=%v", len(mu), whole["channels_excluded"])
	}
	mi, _ := list(t, e.cmd("messages", "--unread", "--include-channels"))
	if len(mi) != 16 {
		t.Fatalf("messages --unread --include-channels = %d, want 16", len(mi))
	}
}

func TestE2EThread(t *testing.T) {
	e := newEnv(t)
	e.sync()

	items, _ := list(t, e.cmd("thread", channel1, "1700000045000"))
	ids := strs(items, "id")
	if len(ids) != 2 || ids[0] != "1700000045000" || ids[1] != "1700000046000" {
		t.Fatalf("thread ids = %v", ids)
	}
	if items[0]["subject"] != "Fixture subject" || items[0]["importance"] != "high" || items[0]["pinned"] != true || items[1]["parent_message_id"] != "1700000045000" {
		t.Fatalf("thread items = %v", items)
	}
	limited, whole := list(t, e.cmd("thread", channel1, "1700000045000", "--limit", "1"))
	if len(limited) != 1 || whole["truncated"] != true {
		t.Fatalf("--limit 1 = %v truncated=%v", limited, whole["truncated"])
	}
	link, _ := list(t, e.cmd("thread", "https://teams.microsoft.com/l/message/"+channel1+"/1700000045000"))
	if len(link) != 2 {
		t.Fatalf("thread by link = %d items", len(link))
	}
	wantError(t, e.cmd("thread", channel1), 2, "usage")
	wantError(t, e.cmd("thread", "https://example.org/not-teams"), 2, "usage")
}

func TestE2EWhoami(t *testing.T) {
	e := newEnv(t)
	res := e.cmd("whoami", "--max-age", "0")
	mustExit(t, res, 0)
	if !strings.Contains(res.stderr, "teamscrawl sync") {
		t.Fatalf("stderr should hint at sync: %q", res.stderr)
	}
	who := mustJSON(t, res.stdout)
	if accts, _ := who["accounts"].([]any); len(accts) != 0 {
		t.Fatalf("accounts before a sync = %v", who["accounts"])
	}
	if who["needs_sync"] != true || who["hint"] == "" {
		t.Fatalf("never synced: %v", who)
	}
	e.sync()
	who = ok(t, e.cmd("whoami"))
	accts, _ := who["accounts"].([]any)
	if len(accts) != 2 {
		t.Fatalf("accounts = %v", who["accounts"])
	}
	a := accts[0].(map[string]any)
	for _, k := range []string{"tenant_id", "user_id", "self_id", "display_name", "locale", "first_seen_at", "last_synced_at"} {
		if _, has := a[k]; !has {
			t.Errorf("account has no %q: %v", k, a)
		}
	}
	arch, _ := who["archive"].(map[string]any)
	if arch["archive_exists"] != true || arch["fts_present"] != true {
		t.Fatalf("archive = %v", arch)
	}
	if _, has := who["archive_age_seconds"]; !has {
		t.Fatalf("no archive_age_seconds: %v", who)
	}
}

func TestE2EMentionsMe(t *testing.T) {
	e := newEnv(t)
	e.sync()
	items, _ := list(t, e.cmd("messages", "--mentions-me"))
	if len(items) != 6 {
		t.Fatalf("messages --mentions-me = %d, want 6", len(items))
	}
	for _, it := range items {
		if it["mentions_me"] != true {
			t.Fatalf("item without mentions_me: %v", it)
		}
	}
	one, _ := list(t, e.cmd("messages", "--mentions-me", "--account", account2))
	if len(one) != 3 {
		t.Fatalf("account 2 mentions = %d, want 3", len(one))
	}
	s, _ := list(t, e.cmd("search", "Alex", "--mentions-me"))
	if len(s) != 2 {
		t.Fatalf("search --mentions-me = %d, want 2", len(s))
	}
}

func TestE2EMaxAge(t *testing.T) {
	t.Run("a never-synced archive syncs implicitly", func(t *testing.T) {
		e := newEnv(t)
		items, whole := list(t, e.cmd("messages", "-c", "Fixture chat 1"))
		if len(items) == 0 {
			t.Fatal("the implicit sync produced no messages")
		}
		if age, ok := whole["archive_age_seconds"].(float64); !ok || age > 30 {
			t.Fatalf("archive_age_seconds = %v, want a small number", whole["archive_age_seconds"])
		}
		if _, has := whole["needs_sync"]; has {
			t.Fatalf("needs_sync after a successful implicit sync: %v", whole)
		}
	})

	t.Run("0 disables the implicit sync", func(t *testing.T) {
		e := newEnv(t)
		res := e.cmd("messages", "--max-age", "0")
		mustExit(t, res, 0)
		whole := mustJSON(t, res.stdout)
		if whole["needs_sync"] != true || whole["hint"] != "run teamscrawl sync" || whole["archive_age_seconds"] != nil || whole["count"] != float64(0) {
			t.Fatalf("never synced with --max-age 0 = %v", whole)
		}
		if _, err := os.Stat(e.db); err == nil {
			t.Fatal("--max-age 0 must not create the archive")
		}
		if !strings.Contains(res.stderr, "teamscrawl sync") {
			t.Fatalf("stderr should hint at sync: %q", res.stderr)
		}
		// The same through the environment.
		res = e.runWith([]string{"TEAMSCRAWL_MAX_AGE=0"}, append([]string{"people"}, e.baseArgs()...)...)
		if mustExit(t, res, 0); mustJSON(t, res.stdout)["needs_sync"] != true {
			t.Fatalf("TEAMSCRAWL_MAX_AGE=0: %s", res.stdout)
		}
	})

	t.Run("a fresh archive is not synced again", func(t *testing.T) {
		e := newEnv(t)
		e.sync()
		// Break the Teams root: a read that tried to sync would warn.
		bad := []string{"--teams-root", filepath.Join(e.tmp, "missing"), "--db", e.db}
		res := e.run(append([]string{"people"}, bad...)...)
		_, whole := list(t, res)
		if _, has := whole["sync_error"]; has || res.stderr != "" {
			t.Fatalf("a fresh archive triggered a sync: %v / %q", whole, res.stderr)
		}
	})

	t.Run("a failing implicit sync warns and still returns results", func(t *testing.T) {
		e := newEnv(t)
		e.sync()
		time.Sleep(1100 * time.Millisecond)
		bad := []string{"--teams-root", filepath.Join(e.tmp, "missing"), "--db", e.db}
		res := e.run(append([]string{"people", "--max-age", "1s"}, bad...)...)
		mustExit(t, res, 0)
		whole := mustJSON(t, res.stdout)
		if n, _ := whole["items"].([]any); len(n) == 0 {
			t.Fatalf("no results from the stale archive: %s", res.stdout)
		}
		se, _ := whole["sync_error"].(map[string]any)
		if se == nil || se["code"] != "teams_not_installed" || se["message"] == "" {
			t.Fatalf("sync_error = %v", whole["sync_error"])
		}
		warn := mustJSON(t, res.stderr)
		w, _ := warn["warning"].(map[string]any)
		if w == nil || w["code"] != "teams_not_installed" || w["fix"] == "" {
			t.Fatalf("stderr warning = %s", res.stderr)
		}
		if _, has := warn["error"]; has {
			t.Fatalf("a warning must not be an error: %s", res.stderr)
		}
	})
}

func TestE2EFieldsAndMaxText(t *testing.T) {
	e := newEnv(t)
	e.sync()

	items, _ := list(t, e.cmd("messages", "-c", "Fixture chat 1", "--fields", "id,text"))
	for _, it := range items {
		if len(it) != 2 || it["id"] == nil || it["text"] == nil {
			t.Fatalf("--fields id,text kept %v", it)
		}
	}
	for _, args := range [][]string{
		{"search", "Hello", "--fields", "id"},
		{"conversations", "--fields", "id,display_name"},
		{"people", "--fields", "display_name"},
		{"activity", "--fields", "id,type"},
		{"unread", "--fields", "id"},
		{"thread", channel1, "1700000045000", "--fields", "id"},
	} {
		its, _ := list(t, e.cmd(args...))
		if len(its) == 0 {
			t.Fatalf("%v: no items", args)
		}
		want := len(strings.Split(args[len(args)-1], ","))
		for _, it := range its {
			if len(it) != want {
				t.Fatalf("%v kept %v", args, it)
			}
		}
	}

	cut, _ := list(t, e.cmd("messages", "-c", "Fixture chat 1", "--max-text", "5", "--fields", "id,text"))
	var truncated int
	for _, it := range cut {
		// text_truncated travels with text, so --fields id,text keeps it.
		if it["text_truncated"] == true {
			truncated++
		}
	}
	full, _ := list(t, e.cmd("messages", "-c", "Fixture chat 1", "--max-text", "5"))
	var flagged int
	for _, it := range full {
		text, _ := it["text"].(string)
		if n := len([]rune(text)); n > 6 { // 5 characters and the ellipsis
			t.Fatalf("text not truncated to 5: %q", text)
		}
		if it["text_truncated"] == true {
			flagged++
			if !strings.HasSuffix(text, "…") {
				t.Fatalf("truncated text without an ellipsis: %q", text)
			}
		}
	}
	if flagged == 0 || truncated == 0 {
		t.Fatalf("no item was truncated (flagged=%d)", flagged)
	}
	whole, _ := list(t, e.cmd("messages", "-c", "Fixture chat 1", "--max-text", "0"))
	for _, it := range whole {
		if _, has := it["text_truncated"]; has {
			t.Fatalf("--max-text 0 truncated: %v", it)
		}
	}

	// Bad keys and non-list commands are usage errors with a fix.
	wantError(t, e.cmd("messages", "--fields", "id,text_truncated"), 2, "usage")
	wantError(t, e.cmd("whoami", "--fields", "id"), 2, "usage")
	wantError(t, e.cmd("status", "--max-text", "5"), 2, "usage")
	wantError(t, e.cmd("messages", "--max-text", "-1"), 2, "usage")
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

func TestE2EOutputContract(t *testing.T) {
	e := newEnv(t)
	e.sync()

	t.Run("non-TTY default is JSON", func(t *testing.T) {
		res := e.cmd("conversations")
		mustExit(t, res, 0)
		if !json.Valid([]byte(res.stdout)) || !strings.HasPrefix(res.stdout, "{") {
			t.Fatalf("default output is not JSON: %s", res.stdout)
		}
		if strings.Count(res.stdout, "\n") != 1 {
			t.Fatalf("JSON output should be one line: %q", res.stdout)
		}
	})

	t.Run("--format text prints no JSON", func(t *testing.T) {
		for _, args := range [][]string{{"conversations"}, {"messages", "-c", "Fixture chat 1"}, {"status"}, {"whoami"}, {"doctor"}, {"unread", "--by-conversation"}} {
			res := e.cmd(append(args, "--format", "text")...)
			mustExit(t, res, 0)
			if json.Valid([]byte(strings.TrimSpace(res.stdout))) || strings.HasPrefix(strings.TrimSpace(res.stdout), "{") {
				t.Fatalf("%v: text mode printed JSON: %.200s", args, res.stdout)
			}
			if strings.TrimSpace(res.stdout) == "" {
				t.Fatalf("%v: text mode printed nothing", args)
			}
		}
		res := e.cmd("conversations", "--format", "text")
		if !strings.Contains(res.stdout, "Fixture chat 1") {
			t.Fatalf("text output misses the data: %s", res.stdout)
		}
	})

	t.Run("--json overrides text", func(t *testing.T) {
		res := e.cmd("conversations", "--json")
		if mustExit(t, res, 0); !json.Valid([]byte(res.stdout)) {
			t.Fatalf("--json did not print JSON: %s", res.stdout)
		}
	})

	t.Run("no ANSI escapes without a terminal, with NO_COLOR or --no-color", func(t *testing.T) {
		base := append([]string{"conversations", "--format", "text"}, e.baseArgs()...)
		for name, tc := range map[string]struct {
			env  []string
			args []string
		}{
			"piped":                  {nil, nil},
			"NO_COLOR":               {[]string{"NO_COLOR=1"}, nil},
			"--no-color":             {nil, []string{"--no-color"}},
			"NO_COLOR beats force":   {[]string{"NO_COLOR=1", "CLICOLOR_FORCE=1"}, nil},
			"--no-color beats force": {[]string{"CLICOLOR_FORCE=1"}, []string{"--no-color"}},
		} {
			res := e.runWith(tc.env, append(append([]string{}, base...), tc.args...)...)
			mustExit(t, res, 0)
			if ansi.MatchString(res.stdout) || ansi.MatchString(res.stderr) {
				t.Errorf("%s: ANSI escapes in output: %q", name, res.stdout)
			}
		}
		// Sanity: forcing color really does color, so the checks above prove something.
		res := e.runWith([]string{"CLICOLOR_FORCE=1"}, base...)
		if !ansi.MatchString(res.stdout) {
			t.Errorf("CLICOLOR_FORCE=1 produced no color; the no-color assertions are vacuous")
		}
	})

	t.Run("errors are coded on stderr: JSON by default, lines in text mode", func(t *testing.T) {
		for _, format := range []string{"json", "text"} {
			e2 := newEnv(t)
			e2.root = filepath.Join(e2.tmp, "missing")
			res := e2.cmd("sync", "--format", format)
			if format == "json" {
				body := wantError(t, res, 3, "teams_not_installed")
				if !strings.Contains(body["fix"].(string), "Teams") {
					t.Fatalf("fix = %v", body["fix"])
				}
				continue
			}
			// Text mode prints the same error as "error:" and "fix:" lines, still on stderr.
			mustExit(t, res, 3)
			if res.stdout != "" || !strings.Contains(res.stderr, "error: Teams desktop data not found") || !strings.Contains(res.stderr, "fix: Install") {
				t.Fatalf("text-mode error: stdout %q stderr %q", res.stdout, res.stderr)
			}
			if json.Valid([]byte(strings.TrimSpace(res.stderr))) {
				t.Fatalf("text-mode stderr is JSON: %s", res.stderr)
			}
		}
	})
}

// TestE2EWatch runs the real watch loop: the baseline sync prints nothing, then a change in the
// (copied) cache produces a message line.
func TestE2EWatch(t *testing.T) {
	e := newEnv(t)
	s := e.start(nil, append([]string{"watch", "--every", "1s"}, e.baseArgs()...)...)
	count := func(q string) int { return archiveCount(t, e.db, q) }
	s.waitFor("the baseline sync", func() bool { return count("select count(*) from messages") == 104 })
	if out := s.stdout.String(); out != "" {
		t.Fatalf("the baseline must print nothing, got %q", out)
	}

	// Make the next sync see one message as new: forget it in the archive, then touch the cache so
	// its fingerprint changes.
	archiveExec(t, e.db, `delete from message_fts where rowid in (select rowid from messages where id='1700000001000' and tenant_id=?)`, tenant1)
	archiveExec(t, e.db, `delete from messages where id='1700000001000' and tenant_id=?`, tenant1)
	logs, _ := filepath.Glob(filepath.Join(e.root, "*", "IndexedDB", "*.leveldb", "*.log"))
	if len(logs) == 0 {
		t.Fatal("no leveldb log to touch")
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(logs[0], later, later); err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	s.waitFor("a message line", func() bool {
		for _, l := range strings.Split(s.stdout.String(), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil && m["kind"] == "message" {
				line = m
				return true
			}
		}
		return false
	})
	if line["change"] != "new" {
		t.Fatalf("change = %v, want new: %v", line["change"], line)
	}
	item, _ := line["item"].(map[string]any)
	if item["id"] != "1700000001000" || item["text"] != "Hello from Alex Fixture" {
		t.Fatalf("item = %v", item)
	}
	res := s.signal(syscall.SIGTERM)
	if res.code != 0 {
		t.Fatalf("exit %d after SIGTERM\nstderr: %s", res.code, res.stderr)
	}
	for _, l := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		if !json.Valid([]byte(l)) {
			t.Fatalf("watch printed a non-JSON line: %q", l)
		}
	}
	if m := snapshots(t, e.tmp); len(m) != 0 {
		t.Fatalf("snapshot left behind: %v", m)
	}
}

func archiveCount(t *testing.T, path, q string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return 0
	}
	defer func() { _ = db.Close() }()
	var n int
	if db.QueryRow(q).Scan(&n) != nil {
		return 0 // the archive may not exist yet
	}
	return n
}

func archiveExec(t *testing.T, path, q string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// mustRun asserts a clean exit and returns stdout.
func mustRun(t *testing.T, res result) string {
	t.Helper()
	mustExit(t, res, 0)
	return res.stdout
}
