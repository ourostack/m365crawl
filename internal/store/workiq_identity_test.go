package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQAccountsIncludeFailuresWithoutInventingIdentity(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Broken|mail", Status: "failed", ErrorCode: "outlook_profile_unreadable", StartedAt: base, FinishedAt: base, SourceAccounts: []string{"outlook/Broken"}}))
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values
('outlook_identity:outlook/Main','Test.User@Example.Invalid'),
('outlook_identity:outlook/Empty','')`)
	must0(err)
	facts, err := s.WorkIQAccounts(ctx)
	must0(err)
	byID := map[string]workiq.AccountFact{}
	for _, fact := range facts {
		byID[fact.ID] = fact
	}
	if len(byID) != 4 || byID["outlook/Broken"].IdentityPresent || !byID["outlook/Empty"].IdentityPresent ||
		len(byID["outlook/Main"].Addresses) != 1 || byID["outlook/Main"].Addresses[0] != "test.user@example.invalid" ||
		byID[acctA.TenantID+"/"+acctA.UserID].UserID != acctA.UserID {
		t.Fatal("account discovery lost a failure or confused missing and empty identity")
	}
	encoded := must(json.Marshal(facts))
	if strings.Contains(strings.ToLower(string(encoded)), "test.user@") {
		t.Fatal("private account address became a public JSON field")
	}
	if rowCount(t, s, `select count(*) from workiq_accounts`) != 0 {
		t.Fatal("reading account facts registered a server identity")
	}
}

func seedWorkIQAccountFacts(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Known|mail", Status: "failed", StartedAt: base, FinishedAt: base, SourceAccounts: []string{"outlook/Known"}}))
	must0(s.RecordRun(ctx, Run{Source: "outlook|Legacy|mail", Status: "failed", StartedAt: base, FinishedAt: base}))
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values
('outlook_identity:outlook/Main','Test.User@Example.Invalid'),
('outlook_identity:outlook/Empty',''),
('outlook_mail_failure:outlook/Multiple','{"code":"outlook_mail_layout_unsupported","message":"multiple_mailboxes"}'),
('outlook_failure:outlook/Failed','{"code":"outlook_profile_unreadable"}')`)
	must0(err)
	_, err = s.db.ExecContext(ctx, `insert into records(source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at)
values('fixture',?,?,'fixture','profiles',?,?,'fixture','','')`,
		acctA.TenantID, acctA.UserID, `"8:orgid:`+acctA.UserID+`"`, `{"mail":"Test.User@Example.Invalid","email":"","userPrincipalName":"test.user@example.invalid"}`)
	must0(err)
}

func TestWorkIQAccountMetadataPrefixesAreLiteral(t *testing.T) {
	s := newStore(t)
	_, err := s.db.Exec(`insert into meta(key,value) values('outlookXidentity:outlook/NotAnAccount','not a refusal')`)
	must0(err)
	facts, err := s.WorkIQAccounts(context.Background())
	if err != nil || len(facts) != 0 {
		t.Fatal("unrelated metadata became an Outlook account or refusal")
	}
}

func TestWorkIQMalformedArchivedIdentityFactsFailClosed(t *testing.T) {
	for _, statement := range []string{
		`insert into sync_runs(started_at,source,status,source_accounts_json) values('','outlook|Broken|mail','failed','{')`,
		`insert into meta(key,value) values('outlook_mail_failure:outlook/Broken','{')`,
		`insert into meta(key,value) values('workiq_enabled','2')`,
		`alter table workiq_rows rename to broken_workiq_rows`,
	} {
		t.Run(statement, func(t *testing.T) {
			s := newStore(t)
			_, err := s.db.Exec(statement)
			must0(err)
			if strings.Contains(statement, "workiq_enabled") {
				if _, err := s.WorkIQEnabled(context.Background()); err == nil {
					t.Fatal("corrupt opt-in silently became disabled")
				}
				return
			}
			if _, err := s.WorkIQAccounts(context.Background()); err == nil {
				t.Fatal("corrupt archived facts silently became checked-empty accounts")
			}
		})
	}
}
