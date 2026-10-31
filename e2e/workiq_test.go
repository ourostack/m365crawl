//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQEndToEndUsesOnlyExplicitSyntheticFetch(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	account := "outlook/Synthetic"
	_, err = st.CommitMail(ctx, store.MailBatch{Account: account, ReadAt: time.Now(), FreshAt: time.Now(), Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Native inbox", Kind: "inbox"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	helper := os.Getenv("M365CRAWL_TEST_ACQUISITION_STUB")
	if helper == "" {
		helper = filepath.Join(e.tmp, "synthetic-workiq.exe")
		cmd := exec.Command("go", "build", "-o", helper, "../internal/workiq/testdata/stub") //nolint:gosec // G204: fixed synthetic source, private test executable output.
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build synthetic transport: %v: %s", err, output)
		}
	}
	transcript := filepath.Join(e.tmp, "fetch-only.jsonl")
	extra := []string{"M365CRAWL_WORKIQ_BIN=" + helper, "WORKIQ_ACQUISITION_TRANSCRIPT=" + transcript,
		"WORKIQ_ACQUISITION_LOCK_PATH=" + e.db, "PATH="}
	run := func(args ...string) result {
		t.Helper()
		return e.runWith(extra, append(e.baseArgs(), args...)...)
	}
	if doc := ok(t, run("--account", account, "workiq", "status", "--source", "mail")); doc["enabled"] != false {
		t.Fatal("disabled status enabled the source")
	}
	if doc := ok(t, run("workiq", "enable")); doc["enabled"] != true {
		t.Fatal("enable did not persist opt-in")
	}
	if doc := ok(t, run("--account", account, "workiq", "coverage", "--source", "mail")); len(doc["mail"].([]any)) != 7 {
		t.Fatal("counts-only command did not enumerate folder totals")
	}
	doc := ok(t, run("--account", account, "workiq", "sync", "--source", "mail"))
	if doc["enabled"] != true || doc["counts"].(map[string]any)["mail_added"] != float64(1) {
		t.Fatal("actual CLI did not retain the synthetic source item")
	}
	read := ok(t, run("--max-age", "0", "mail", "list", "--fields", "id,provenance"))
	rows := read["items"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["provenance"] != "workiq_server" {
		t.Fatal("offline CLI lost server provenance")
	}
	id := rows[0].(map[string]any)["id"].(string)
	shown := ok(t, run("--max-age", "0", "mail", "show", id))
	if shown["id"] != id || shown["provenance"] != "workiq_server" {
		t.Fatal("server public ID did not resolve")
	}
	before, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript beneath the isolated test root.
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--help"}, {"metadata"}, {"skill"}, {"--max-age", "0", "status"},
		{"--max-age", "0", "mail", "thread", id}} {
		mustExit(t, run(args...), 0)
	}
	after, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript beneath the isolated test root.
	if err != nil || string(before) != string(after) {
		t.Fatal("ordinary commands spawned the optional transport", err)
	}
	doc = ok(t, run("workiq", "disable"))
	if doc["enabled"] != false {
		t.Fatal("disable did not persist")
	}
	read = ok(t, run("--max-age", "0", "mail", "list"))
	if len(read["items"].([]any)) != 1 {
		t.Fatal("disable erased or hid retained data")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(after)), "\n") {
		call := mustJSON(t, line)
		if call["tool"] != "fetch" {
			t.Fatal("read command issued a write tool")
		}
	}
	st, err = store.OpenReadOnly(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, receipts, _, err := st.SQL(ctx, `select count(*) from workiq_mail_receipts`, 1)
	if err != nil || len(receipts) != 1 || receipts[0][0] != int64(1) {
		t.Fatal("counts or status minted a data completion receipt", err)
	}
}

// commandWords splits a printed follow-up command at spaces outside the quotes that quoteArg adds.
func commandWords(command string) []string {
	var out []string
	var word strings.Builder
	quote, open := rune(0), false
	for _, r := range command {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote, open = r, true
		case quote == 0 && r == ' ':
			if open {
				out = append(out, word.String())
				word.Reset()
				open = false
			}
		default:
			word.WriteRune(r)
			open = true
		}
	}
	if open {
		out = append(out, word.String())
	}
	return out
}

func TestWorkIQRouteFromAnOldMailReadLeadsAnAgentToServerMail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		t.Fatal(err)
	}
	account := "outlook/Synthetic"
	cached := time.Now().Add(-2 * time.Hour).UTC()
	native := outlookmail.Message{Account: account, Folder: outlookmail.Folder{Key: 1, Kind: "inbox"}, Copies: 1}
	native.DetailKey, native.FolderKey = 7, 1
	native.MessageID, native.Subject, native.Received, native.Sent = "<native-route@example.test>", "Cached native mail", cached, cached
	native.Class, native.ReadState = "IPM.Note", "unknown"
	_, err = st.CommitMail(ctx, store.MailBatch{Account: account, ReadAt: time.Now(), FreshAt: time.Now(), Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Native inbox", Kind: "inbox"}},
			Messages: []outlookmail.Message{native}, Coverage: []outlookmail.Coverage{{FolderKey: 1, Oldest: cached, Newest: cached, Count: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(e.tmp, "fetch-only.jsonl")
	// The read's route is a mail window of the range before the cache; the synthetic mailbox holds
	// server mail in it.
	extra := append(windowStub(t, e, 0, "", [4]string{"server-route", "inbox-id", "<server-route@example.test>", "2001-02-03T04:05:06Z"}),
		"WORKIQ_ACQUISITION_TRANSCRIPT="+transcript, "WORKIQ_ACQUISITION_LOCK_PATH="+e.db)
	read := ok(t, e.runWith(extra, append(e.baseArgs(), "--max-age", "0", "mail", "list", "--since", "2000-01-01")...))
	routes, _ := read["workiq_routes"].([]any)
	if len(routes) != 1 {
		t.Fatalf("an old mail read did not route to WorkIQ: %v", read["workiq_routes"])
	}
	route := routes[0].(map[string]any)
	if route["account"] != account || route["workiq"] != "disabled" || route["source"] != "mail" {
		t.Fatalf("route = %v", route)
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatal("the ordinary read started WorkIQ")
	}
	steps := route["next"].([]any)
	if len(steps) != 4 {
		t.Fatalf("steps = %v", steps)
	}
	for _, step := range steps[:3] {
		words := commandWords(step.(map[string]any)["command"].(string))
		if len(words) < 2 || words[0] != "m365crawl" {
			t.Fatalf("not a runnable command: %q", words)
		}
		ok(t, e.runWith(extra, words[1:]...))
	}
	words := commandWords(steps[3].(map[string]any)["command"].(string))
	again := ok(t, e.runWith(extra, words[1:]...))
	if again["workiq_routes"] != nil {
		t.Fatalf("the route survived a complete WorkIQ sync: %v", again["workiq_routes"])
	}
	server := 0
	for _, item := range again["items"].([]any) {
		if item.(map[string]any)["provenance"] == "workiq_server" {
			server++
		}
	}
	if server == 0 {
		t.Fatal("the repeated read holds no server mail")
	}
}
