package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
)

// renderToString renders v as plain text (no color, UTC times) and returns what was printed.
func renderToString(t *testing.T, label string, v any) string {
	t.Helper()
	old := displayZone
	displayZone = time.UTC
	t.Cleanup(func() { displayZone = old })
	var out bytes.Buffer
	rt := &runtime{stdout: &out, format: output.Text}
	t.Setenv("COLUMNS", "120")
	if err := rt.renderText(label, v); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestStampOfAZeroTimeIsADash(t *testing.T) {
	if got := stamp(time.Time{}); got != "-" {
		t.Fatalf("stamp(zero) = %q", got)
	}
}

func TestListTextFooterShowsTruncationAgeAndSyncError(t *testing.T) {
	res := newList(nil, true)
	res.setMeta(nil, &syncError{Code: "locked", Message: "archive busy"})
	got := renderToString(t, "search", res)
	for _, want := range []string{"items", "0 items (more exist; raise --limit)", "archive age: never synced", "sync error: locked: archive busy"} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
	age := int64(90)
	res = newList(nil, false)
	res.setMeta(&age, nil)
	got = renderToString(t, "search", res)
	if !strings.Contains(got, "archive age: 1m30s") || strings.Contains(got, "more exist") || strings.Contains(got, "sync error") {
		t.Errorf("fresh list footer wrong:\n%s", got)
	}
}

func TestSyncTextListsOmissionsInNameOrder(t *testing.T) {
	got := renderToString(t, "sync", syncer.Report{Status: "ok_with_omissions", Omissions: map[string]int{"zeta": 2, "alpha": 1}})
	if !strings.Contains(got, "omitted alpha: 1\nomitted zeta: 2\n") {
		t.Fatalf("omissions not sorted:\n%s", got)
	}
}

func TestSyncTextShowsRedactionsOnlyWhenThereAreAny(t *testing.T) {
	if got := renderToString(t, "sync", syncer.Report{Status: "ok", Redacted: 4}); !strings.Contains(got, "redacted: 4 credential-looking values") {
		t.Fatalf("redactions missing:\n%s", got)
	}
	if got := renderToString(t, "sync", syncer.Report{Status: "ok"}); strings.Contains(got, "redacted") {
		t.Fatalf("zero redactions shown:\n%s", got)
	}
}

func TestWhoamiTextNamesUnnamedAccountsAndSaysWhenThereAreNone(t *testing.T) {
	got := renderToString(t, "whoami", &whoamiResult{Accounts: []store.WhoamiRow{{TenantID: tenantA, UserID: userA}}, Archive: &statusResult{}})
	if !strings.Contains(got, "(name not seen yet)") || strings.Contains(got, "no accounts archived yet") {
		t.Fatalf("unnamed account:\n%s", got)
	}
	got = renderToString(t, "whoami", &whoamiResult{Accounts: []store.WhoamiRow{}, Archive: &statusResult{}})
	if !strings.Contains(got, "no accounts archived yet; run teamscrawl sync") {
		t.Fatalf("no accounts:\n%s", got)
	}
}

func TestStatusTextCoversMissingArchiveOriginsAndEmptyArchive(t *testing.T) {
	got := renderToString(t, "status", &statusResult{ArchivePath: "/x/a.db"})
	if !strings.Contains(got, "run teamscrawl sync to create it") || !strings.Contains(got, "/x/a.db") {
		t.Fatalf("missing archive:\n%s", got)
	}
	got = renderToString(t, "status", &statusResult{ArchivePath: "/x/a.db", ArchiveExists: true, OtherOrigins: []string{"https_a_0", "https_b_0"}})
	if !strings.Contains(got, "https_a_0, https_b_0") || strings.Contains(got, "newest") {
		t.Fatalf("archive without accounts:\n%s", got)
	}
}

func TestUnknownResultsRenderAsABlock(t *testing.T) {
	got := renderToString(t, "thing", map[string]any{"answer": 42})
	if !strings.Contains(got, "answer") || !strings.Contains(got, "42") {
		t.Fatalf("block:\n%s", got)
	}
}

func TestListTableShowsEachItemKind(t *testing.T) {
	sent := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	cases := []struct {
		name  string
		items []any
		want  []string
	}{
		{"deleted message", []any{messageItem{SentAt: sent, ConversationDisplayName: "Chat", SenderName: "Ann", Text: "gone\nnow", DeletedAt: sent}},
			[]string{"2026-09-01 10:30", "Chat", "Ann", "gone now (deleted)"}},
		{"conversation", []any{conversationItem{LastMessageAt: sent, Kind: "Chat", DisplayName: "Team chat", MemberCount: 3}},
			[]string{"last_message_at", "Team chat", "3"}},
		{"person", []any{personItem{DisplayName: "Ann", ID: "8:orgid:x", LastSeenAt: sent}},
			[]string{"last_seen_at", "Ann", "8:orgid:x"}},
		{"activity", []any{activityItem{At: sent, Type: "mention", IsRead: true, SenderName: "Bob", Text: "hi"}, activityItem{At: sent, Type: "reply"}},
			[]string{"state", "read", "unread", "Bob"}},
		{"projected", []any{projected{keys: []string{"id", "text", "missing"}, vals: map[string]json.RawMessage{"id": json.RawMessage(`7`), "text": json.RawMessage(`"line\none"`), "missing": json.RawMessage(`null`)}}},
			[]string{"id", "text", "7", "line one"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderToString(t, "x", newList(c.items, false))
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("output lacks %q:\n%s", want, got)
				}
			}
		})
	}
}
