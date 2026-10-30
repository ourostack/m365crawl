package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

// sourceHealthArchive starts these post-sync tests with real completed-run freshness, without capturing a cache they never inspect.
func (e *env) sourceHealthArchive() {
	e.t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			e.t.Fatal(err)
		}
	}()
	if err := st.RecordRun(ctx, store.Run{StartedAt: mailT0, FinishedAt: mailT0, Status: "ok", Accounts: []string{"*"}}); err != nil {
		e.t.Fatal(err)
	}
	if last, err := st.LastSuccess(ctx); err != nil || !last.Equal(mailT0) {
		e.t.Fatalf("post-sync fixture has no complete-success marker: %v %v", last, err)
	}
}

func TestSourceHealthCommandScopeAndProjection(t *testing.T) {
	e := mailEnv(t)
	t.Setenv("HOME", t.TempDir())
	e.sync()
	root := t.TempDir()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, source := range []string{"synthetic|teams", "outlook|Main", "outlook|Main|mail", "outlook|Other|mail"} {
		accounts := []string(nil)
		if source == "synthetic|teams" {
			accounts = []string{tenantA + "/" + userA}
		}
		if err := st.RecordRun(ctx, store.Run{Source: source, Status: "ok", Fingerprint: "good", StartedAt: mailT0, FinishedAt: mailT0, SourceAccounts: accounts}); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordRun(ctx, store.Run{Source: source, Status: "failed", ErrorCode: "synthetic_failure", StartedAt: mailT0, FinishedAt: mailT0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.Close()
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"messages", "--fields", "id"}, 1},
		{[]string{"conversations"}, 1},
		{[]string{"unread"}, 1},
		{[]string{"activity"}, 1},
		{[]string{"mail", "list", "--fields", "id"}, 2},
		{[]string{"mail", "folders"}, 2},
		{[]string{"mail", "unread"}, 2},
		{[]string{"mail", "show", mailAccount + ":101", "--fields", "id"}, 1},
		{[]string{"mail", "thread", mailAccount + ":101"}, 1},
		{[]string{"calendar", "--from", "2026-09-01", "--to", "2026-09-30"}, 2},
		{[]string{"calendar", "actions", "--from", "2026-09-01", "--to", "2026-09-30"}, 2},
		{[]string{"search", "Hello", "--source", "all", "--fields", "id"}, 3},
		{[]string{"search", "Quarterly", "--source", "all"}, 3},
		{[]string{"search", "not-present", "--source", "all"}, 3},
		{[]string{"search", "not-present", "--source", "chats"}, 1},
		{[]string{"search", "not-present", "--source", "mail"}, 2},
		{[]string{"--account", "outlook/Main", "search", "not-present"}, 1},
		{[]string{"--account", "outlook/Other", "mail", "list"}, 1},
		{[]string{"sql", "select 1"}, 4},
		{[]string{"--account", "outlook/Main", "sql", "select 1"}, 4},
		{[]string{"whoami"}, -1},
		{[]string{"status"}, -1},
		{[]string{"teams"}, -1},
		{[]string{"people"}, -1},
		{[]string{"drafts", "list"}, -1},
		{[]string{"transcripts"}, -1},
		{[]string{"calendar", "sources"}, -1},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			flags := []string{"--json", "--outlook-root", root}
			if tc.args[0] == "drafts" {
				flags = []string{"--json"}
			}
			code, out, stderr := e.mail(append(flags, tc.args...)...)
			if tc.args[0] == "drafts" {
				code, out, stderr = e.runDefault(append([]string{"--json", "--no-sync"}, tc.args...)...)
			}
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			doc := decode(t, out)
			health, present := doc["source_health"].([]any)
			if tc.want < 0 {
				if present {
					t.Fatalf("out-of-scope command gained health: %v", health)
				}
			} else if !present || len(health) != tc.want {
				t.Fatalf("scope: got %v want %d", doc["source_health"], tc.want)
			}
		})
	}
}

func TestSourceHealthEmptyAndDisabled(t *testing.T) {
	e := newEnv(t)
	if doc := mailJSON(t, e, "messages"); doc["source_health"] != nil {
		t.Fatal("never-synced result claimed a health check")
	}
	e.sourceHealthArchive()
	if doc := mailJSON(t, e, "messages"); len(doc["source_health"].([]any)) != 0 {
		t.Fatal("healthy source did not give []")
	}
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRun(context.Background(), store.Run{Source: "outlook|Main|mail", Status: "failed", ErrorCode: "synthetic_failure", StartedAt: mailT0, FinishedAt: mailT0}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if doc := mailJSON(t, e, "--outlook-root", "none", "mail", "list"); len(doc["source_health"].([]any)) != 0 {
		t.Fatal("disabled source retained a health failure")
	}
}

// Missing health metadata must not turn an archived read failure into a healthy empty answer.
func TestSourceHealthRetainedMailFailure(t *testing.T) {
	e := mailEnv(t)
	root := t.TempDir()
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMailFailure(context.Background(), mailAccount, store.OutlookFailure{
		Code: "outlook_mail_layout_unsupported", Message: "Synthetic layout refusal",
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	for _, args := range [][]string{
		{"mail", "list"}, {"mail", "folders"}, {"mail", "unread"},
		{"mail", "show", mailAccount + ":101", "--fields", "id"},
		{"mail", "thread", mailAccount + ":101"},
		{"search", "not-present", "--source", "mail", "--fields", "id"},
	} {
		doc := mailJSON(t, e, append([]string{"--outlook-root", root}, args...)...)
		health, ok := doc["source_health"].([]any)
		if !ok || len(health) != 1 {
			t.Fatalf("%v lost source failure: %v", args, doc["source_health"])
		}
		row := health[0].(map[string]any)
		if row["source"] != "outlook_mail" || row["account_id"] != mailAccount ||
			row["last_error_code"] != "outlook_mail_layout_unsupported" || row["doctor_check"] != "mail_readable" {
			t.Fatalf("wrong failure: %v", row)
		}
	}
}
