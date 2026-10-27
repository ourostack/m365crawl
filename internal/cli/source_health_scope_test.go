package cli

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSourceHealthCalendarPrincipalAndUnknownAccountScope(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	e.exec(`insert into calendar_account_links(source,account_id,principal_id,method,linked_at) values('outlook','outlook/Main','tenant/user','config','2026-09-01')`)
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	main, other, teams := "outlook/Main", "outlook/Other", "tenant/user"
	for _, tc := range []struct {
		label, wantAccount, rawAccount string
		account                        *string
		source                         string
		want                           bool
	}{
		{"calendar", "", "", &main, "outlook_calendar", true},
		{"calendar event", teams, "", &main, "outlook_calendar", true},
		{"calendar event", teams, "", &other, "outlook_mail", true},
		{"calendar event", teams, "", &other, "outlook_calendar", false},
		{"calendar event", teams, "", nil, "teams", true},
		{"calendar event", teams, "", nil, "outlook_calendar", true},
		{"mail show", main, "", nil, "outlook_mail", true},
		{"calendar", "tenant/other", "", &teams, "teams", false},
		{"calendar", teams, "", &teams, "teams", true},
		{"mail show", main, "", &other, "outlook_mail", false},
		{"mail list", "", "", &main, "outlook_mail", true},
		{"search", "", main, &other, "outlook_mail", false},
		{"search", "", main, &main, "outlook_mail", true},
		{"messages", "", "", nil, "teams", true},
	} {
		rt := &runtime{ctx: context.Background(), g: &Globals{Account: tc.rawAccount}, healthAccount: tc.wantAccount}
		h := store.SourceHealth{Source: tc.source, AccountID: tc.account}
		got, err := rt.sourceHealthInScope(st, tc.label, h)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: got %v %v", tc, got, err)
		}
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, account: &teamsdesktop.Account{TenantID: "tenant", UserID: "user"}}
	for _, label := range []string{"messages", "calendar", "calendar actions"} {
		h := store.SourceHealth{Source: "teams", AccountID: &teams}
		if got, err := rt.sourceHealthInScope(st, label, h); err != nil || !got {
			t.Fatalf("%s excluded the selected account: %v %v", label, got, err)
		}
	}
}

func TestSourceHealthReadAndPrincipalErrorsAreNotHealthy(t *testing.T) {
	e := newEnv(t)
	e.emptyArchive()
	e.exec(`insert into sync_runs(started_at,source,status,error_code) values('2026-09-01','outlook|Main','failed','outlook_layout_unsupported')`)
	e.exec(`drop table calendar_account_links`)
	st, err := store.OpenReadOnly(context.Background(), e.db)
	if err != nil {
		t.Fatal(err)
	}
	rt := &runtime{ctx: context.Background(), g: &Globals{}, outlookOn: true, healthAccount: "tenant/user"}
	if err := rt.noteSourceHealth(st, "calendar", newList(nil, false), false); err == nil {
		t.Fatal("failed principal lookup became healthy")
	}
	_ = st.Close()
	if err := rt.noteSourceHealth(st, "messages", newList(nil, false), false); err == nil {
		t.Fatal("closed archive became healthy")
	}
	e.exec(`insert into sync_runs(started_at,source,status,source_accounts_json) values('2026-09-01','teams','failed','invalid')`)
	code, out, stderr := e.mail("--json", "messages")
	if code != 1 || out != "" || errorOf(t, stderr)["code"] != "db_error" {
		t.Fatalf("read boundary swallowed health error: %d %q %q", code, out, stderr)
	}
}
