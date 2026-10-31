//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// windowStub builds the synthetic WorkIQ once per test and writes a scenario mailbox for it: the
// six well-known folders, a child folder, and messages given as id, folder, Message-ID and received
// time. It returns the environment every command of the test runs with.
func windowStub(t *testing.T, e *env, pauseAt int, pauseFile string, messages ...[4]string) []string {
	t.Helper()
	helper := os.Getenv("M365CRAWL_TEST_ACQUISITION_STUB")
	if helper == "" {
		helper = filepath.Join(e.tmp, "synthetic-workiq.exe")
		cmd := exec.Command("go", "build", "-o", helper, "../internal/workiq/testdata/stub") //nolint:gosec // G204: fixed synthetic source, private test executable output.
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build synthetic transport: %v: %s", err, output)
		}
	}
	folders := []map[string]any{
		{"id": "inbox-id", "name": "Inbox", "well_known": "inbox"}, {"id": "sent-id", "name": "Sent Items", "well_known": "sentitems"},
		{"id": "drafts-id", "name": "Drafts", "well_known": "drafts"}, {"id": "deleted-id", "name": "Deleted Items", "well_known": "deleteditems"},
		{"id": "junk-id", "name": "Junk Email", "well_known": "junkemail"}, {"id": "archive-id", "name": "Archive", "well_known": "archive", "hidden": true},
		{"id": "child-id", "name": "Projects", "parent": "inbox-id"},
	}
	var items []map[string]any
	for _, m := range messages {
		items = append(items, map[string]any{"id": m[0], "folder": m[1], "header": m[2], "received": m[3], "modified": m[3], "subject": "Synthetic " + m[2], "sent": m[3]})
	}
	scenario := map[string]any{"folders": folders, "messages": items, "state": filepath.Join(e.tmp, "mailbox-state.json"), "pause_at": pauseAt, "pause_file": pauseFile}
	raw, err := json.Marshal(scenario)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(e.tmp, "mailbox.json")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"M365CRAWL_WORKIQ_BIN=" + helper, "WORKIQ_MAILBOX=" + name, "PATH="}
}

func seedNativeMail(t *testing.T, e *env, account string, messages ...outlookmail.Message) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	oldest := time.Now().Add(-time.Hour).UTC()
	for _, m := range messages {
		if m.Received.Before(oldest) {
			oldest = m.Received
		}
	}
	_, err = st.CommitMail(ctx, store.MailBatch{Account: account, ReadAt: time.Now(), FreshAt: time.Now(), Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Native inbox", Kind: "inbox"}}, Messages: messages,
			Coverage: []outlookmail.Coverage{{FolderKey: 1, Oldest: oldest, Newest: time.Now().UTC(), Count: len(messages)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func dbCount(t *testing.T, db, q string) int {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, rows, _, err := st.SQL(context.Background(), q, 1)
	if err != nil || len(rows) != 1 {
		t.Fatal(q, err)
	}
	n, _ := rows[0][0].(int64)
	return int(n)
}

// A whole-account sync paused before its first message page while a window of the same account
// runs to completion: the window admits one server copy of a message whose second copy, received
// before the window, only the whole-account census sees. Nothing merges, and the census vouches for
// the window's row. A second window of the account meanwhile gets locked.
func TestWorkIQWindowAndWholeAccountSyncRunConcurrently(t *testing.T) {
	e := newEnv(t)
	account := "outlook/Synthetic"
	seedNativeMail(t, e, account)
	pause := filepath.Join(e.tmp, "pause")
	if err := os.WriteFile(pause, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Whole-account mail fetches 1-8 list the tree; fetch 9 is its first message page.
	extra := windowStub(t, e, 9, pause,
		[4]string{"in-window", "inbox-id", "<dup@example.test>", "2026-09-02T09:00:00Z"},
		[4]string{"earlier-copy", "child-id", "<dup@example.test>", "2026-08-31T23:59:58Z"},
		[4]string{"other", "sent-id", "<other@example.test>", "2026-09-03T09:00:00Z"})
	ok(t, e.runWith(extra, append(e.baseArgs(), "workiq", "enable")...))
	whole := e.start(extra, append(e.baseArgs(), "--json", "--account", account, "workiq", "sync", "--source", "mail")...)
	state := filepath.Join(e.tmp, "mailbox-state.json")
	waitLong(t, whole, "the whole-account sync to reach its first message page", func() bool {
		raw, err := os.ReadFile(state) //nolint:gosec // G304: the test's own scenario state.
		var st struct{ Fetches int }
		return err == nil && json.Unmarshal(raw, &st) == nil && st.Fetches >= 9
	})
	window := []string{"--json", "--account", account, "workiq", "sync", "--source", "mail", "--since", "2026-09-01T00:00:00Z", "--until", "2026-09-08T00:00:00Z"}
	doc := ok(t, e.runWith(extra, append(e.baseArgs(), window...)...))
	if w := doc["window"].(map[string]any); w["covered"] != true {
		t.Fatalf("the window was not proven beside a running whole-account sync: %v", doc)
	}
	if dbCount(t, e.db, `select count(*) from workiq_rows where graph_id='in-window' and identity_unproven=1`) != 1 {
		t.Fatal("the window did not admit its copy as unproven")
	}
	if err := os.Remove(pause); err != nil {
		t.Fatal(err)
	}
	select {
	case <-whole.done:
	case <-time.After(60 * time.Second):
		t.Fatal("the whole-account sync did not finish")
	}
	res := finish(t, whole.cmd, whole.err, whole.stdout.String(), whole.stderr.String())
	if res.code != 0 {
		t.Fatalf("whole-account sync failed: %d %s", res.code, res.stderr)
	}
	merged := dbCount(t, e.db, `select count(*) from workiq_aliases a join workiq_aliases b on a.item_rowid=b.item_rowid and a.account_id=b.account_id
where a.kind='mail' and b.kind='mail' and a.namespace='graph' and b.namespace='graph' and a.alias='in-window' and b.alias='earlier-copy'`)
	if merged != 0 || dbCount(t, e.db, `select count(*) from workiq_rows where graph_id in ('in-window','earlier-copy')`) != 2 {
		t.Fatal("two server copies of one Message-ID merged across a window and a whole-account census")
	}
	if dbCount(t, e.db, `select count(*) from workiq_rows where identity_unproven=1`) != 0 {
		t.Fatal("the whole-account census did not vouch for the window's rows")
	}

	// A second window of the same account while one runs is locked.
	pause2 := filepath.Join(e.tmp, "pause-2")
	if err := os.WriteFile(pause2, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(state) //nolint:gosec // G304: the test's own scenario state.
	if err != nil {
		t.Fatal(err)
	}
	var st struct{ Fetches int }
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	extra = windowStub(t, e, st.Fetches+1, pause2,
		[4]string{"in-window", "inbox-id", "<dup@example.test>", "2026-09-02T09:00:00Z"})
	first := e.start(extra, append(e.baseArgs(), window...)...)
	waitLong(t, first, "the first window to fetch", func() bool {
		raw, err := os.ReadFile(state) //nolint:gosec // G304: the test's own scenario state.
		var now struct{ Fetches int }
		return err == nil && json.Unmarshal(raw, &now) == nil && now.Fetches > st.Fetches
	})
	wantError(t, e.runWith(extra, append(e.baseArgs(), window...)...), 4, "locked")
	if err := os.Remove(pause2); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-time.After(60 * time.Second):
		t.Fatal(fmt.Sprint("the first window did not finish: ", first.stderr.String()))
	}
}

// waitLong is waitFor with a budget for a loaded machine: the paused process starts WorkIQ, lists
// the folder tree and reaches its pause point first.
func waitLong(t *testing.T, s *started, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		select {
		case <-s.done:
			t.Fatalf("process exited while waiting for %s (err %v)\nstdout: %s\nstderr: %s", what, s.err, s.stdout.String(), s.stderr.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s\nstdout: %s\nstderr: %s", what, s.stdout.String(), s.stderr.String())
}
