package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQHealthScopesActualReadsIndependentlyOfLocalOutlook(t *testing.T) {
	e := mailEnv(t)
	t.Setenv("HOME", t.TempDir())
	e.sourceHealthArchive()
	e.sync()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetWorkIQEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][2]string{{"mail", mailAccount}, {"chats", tenantA + "/" + userA}} {
		key, _ := json.Marshal([]string{scope[0], scope[1]})
		if err := st.RecordRun(ctx, store.Run{Source: "workiq:" + string(key), Status: "failed", ErrorCode: "workiq_policy_denied",
			StartedAt: mailT0, FinishedAt: mailT0, SourceAccounts: []string{scope[1]}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"mail", "list"}, 1},
		{[]string{"mail", "folders"}, 1},
		{[]string{"messages"}, 1},
		{[]string{"conversations"}, 1},
		{[]string{"search", "not-present", "--source", "all"}, 2},
		{[]string{"search", "not-present", "--source", "mail"}, 1},
		{[]string{"search", "not-present", "--source", "chats"}, 1},
		{[]string{"sql", "select 1"}, 2},
		{[]string{"whoami"}, 0},
		{[]string{"status"}, 0},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			flags := []string{"--json", "--outlook-root", "none"}
			code, out, stderr := e.mail(append(flags, tc.args...)...)
			if code != 0 || stderr != "" {
				t.Fatal("ordinary scoped read failed or tried the forbidden executable", code, stderr)
			}
			doc := decode(t, out)
			n := 0
			if health, ok := doc["source_health"].([]any); ok {
				for _, item := range health {
					h := item.(map[string]any)
					if h["source"] == "workiq" {
						n++
						if h["doctor_check"] != "workiq" || h["account_id"] == nil {
							t.Fatal("scoped source lost its existing archive owner or doctor")
						}
					}
				}
			}
			if n != tc.want {
				t.Fatal("actual source/query family scope or local Outlook-off hid/misapplied WorkIQ health", n, tc.want)
			}
		})
	}
}

func TestWorkIQHealthNoticesDoNotClaimLocalOutlookRecovery(t *testing.T) {
	through := mailT0.Add(time.Hour)
	text := sourceHealthNotice(store.SourceHealth{Source: "outlook_mail", State: "covered_by_server",
		LastErrorCode: "outlook_mail_layout_unsupported", CoveredThrough: &through})
	if !strings.Contains(text, "WorkIQ") || !strings.Contains(text, "still") ||
		!strings.Contains(text, "outlook_mail_layout_unsupported") || !strings.Contains(text, through.UTC().Format(time.RFC3339)) {
		t.Fatal("coverage notice erased the native failure or omitted its actual server-covered clock")
	}
	text = sourceHealthNotice(store.SourceHealth{Source: "workiq", State: "failing", LastErrorCode: "workiq_policy_denied"})
	if !strings.Contains(text, "WorkIQ") || !strings.Contains(text, "workiq sync") {
		t.Fatal("WorkIQ failure did not name the optional explicit recovery path")
	}
}

func TestWorkIQEnabledUnattemptedMailOffersExplicitRecoveryWithoutSpawning(t *testing.T) {
	e := mailEnv(t)
	t.Setenv("HOME", t.TempDir())
	e.sourceHealthArchive()
	t.Setenv("M365CRAWL_WORKIQ_BIN", filepath.Join(t.TempDir(), "must-not-start"))
	st, err := store.Open(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := st.SetWorkIQEnabled(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordRun(ctx, store.Run{Source: "outlook|Main|mail", Status: "failed", ErrorCode: "outlook_mail_layout_unsupported",
		StartedAt: mailT0, FinishedAt: mailT0, SourceAccounts: []string{mailAccount}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	doc := mailJSON(t, e, "--outlook-root", t.TempDir(), "mail", "list")
	encoded, err := json.Marshal(doc["notices"])
	if err != nil || !strings.Contains(string(encoded), "workiq sync") {
		t.Fatal("enabled optional source did not offer explicit recovery for failing mail", err)
	}
	for _, row := range doc["source_health"].([]any) {
		if row.(map[string]any)["source"] == "workiq" {
			t.Fatal("unattempted source invented a health outcome")
		}
	}
}

func TestWorkIQUnknownHealthFamilyIsNeverTreatedAsTeams(t *testing.T) {
	if sourceHealthFamily(store.SourceHealth{Source: "workiq", WorkIQFamily: "unsupported"}) != "" {
		t.Fatal("unknown optional family fell through to native Teams scope")
	}
}
