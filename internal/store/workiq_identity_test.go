package store

import (
	"context"
	"database/sql"
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

func TestWorkIQStableIDAllocationUsesDisjointSourceNamespaces(t *testing.T) {
	s := newStore(t)
	cache := allocateMailSource(t, s, mailAcct, "message", "same-source-id")
	server := allocateMailSource(t, s, mailAcct, "workiq:message", "same-source-id")
	if cache == server {
		t.Fatal("Graph and native source identities shared a canonical key")
	}
	if got := allocateMailSource(t, s, mailAcct, "workiq:message", "same-source-id"); got != server {
		t.Fatal("repeated server allocation changed identity")
	}
	if got := allocateMailSource(t, s, mailAcct, "message", "new-native"); got <= server {
		t.Fatal("future native allocation collided with a reserved server identity")
	}
}

func TestWorkIQStableIDPublicAliasesResolveWithoutRekeying(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<one@test.invalid>", "One", 0)))
	original := must(s.MailGet(ctx, mailAcct, 21))
	_, err := s.db.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail',?,'public','101',?)`, mailAcct, original.Rowid)
	must0(err)
	aliased, err := s.MailGet(ctx, mailAcct, 101)
	if err != nil || aliased.ID() != original.ID() || aliased.Rowid != original.Rowid {
		t.Fatal("published alternate ID did not resolve to the original canonical row")
	}
	rows, _, _, err := s.MailThread(ctx, mailAcct, 101)
	if err != nil || len(rows) != 1 || rows[0].Rowid != original.Rowid {
		t.Fatal("thread seed did not resolve the published alternate ID")
	}
	if _, err := s.MailGet(ctx, "outlook/another-account", 101); err == nil {
		t.Fatal("alias crossed archive accounts")
	}
	if rowCount(t, s, `select detail_key from mail_messages where rowid=?`, original.Rowid) != 21 {
		t.Fatal("alias lookup changed the first canonical ID")
	}
}

func TestWorkIQStableIDAllocationReservesPublishedAliases(t *testing.T) {
	s := newStore(t)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<one@test.invalid>", "One", 0)))
	_, err := s.db.Exec(`insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid)
select 'mail',account,'public','101',rowid from mail_messages`)
	must0(err)
	if got := allocateMailSource(t, s, mailAcct, "workiq:message", "another-message"); got != 102 {
		t.Fatal("new canonical key could eventually hide a retained public alias")
	}
}

func TestWorkIQStableIDAliasBindingNeverSteals(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<one@test.invalid>", "One", 0), mailMsg(22, fInbox, "<two@test.invalid>", "Two", 0)))
	one := must(s.MailGet(ctx, mailAcct, 21))
	two := must(s.MailGet(ctx, mailAcct, 22))
	bind := func(namespace, alias string, rowid int64) error {
		return s.inTx(ctx, func(tx *sql.Tx) error { return bindWorkIQMailAlias(ctx, tx, mailAcct, namespace, alias, rowid) })
	}
	must0(bind("public", "101", one.Rowid))
	if got, err := s.MailGet(ctx, mailAcct, 101); err != nil || got.Rowid != one.Rowid {
		t.Fatal("alias binding did not persist the canonical row")
	}
	must0(bind("public", "101", one.Rowid))
	for _, invalid := range []struct {
		namespace, alias string
		rowid            int64
	}{
		{"public", "101", two.Rowid},
		{"public", "22", one.Rowid},
		{"public", "not-a-number", one.Rowid},
		{"surprise", "value", one.Rowid},
		{"graph", "", one.Rowid},
		{"graph", "missing-target", 90000},
	} {
		if err := bind(invalid.namespace, invalid.alias, invalid.rowid); err == nil {
			t.Fatal("invalid binding stole an alias, canonical key or namespace")
		}
	}
	must0(bind("graph", "opaque%2Fid", one.Rowid))
	if err := bind("graph", "opaque%2Fid", two.Rowid); err == nil {
		t.Fatal("a Graph move reassigned an already bound identity to another row")
	}
	if rowCount(t, s, `select count(*) from mail_messages`) != 2 || rowCount(t, s, `select count(*) from workiq_aliases`) != 2 {
		t.Fatal("binding duplicated or replaced canonical rows")
	}
}

func TestWorkIQStableIDAliasAndAllocationFaults(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		t.Helper()
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(21, fInbox, "<one@test.invalid>", "One", 0)))
		_, err := s.db.Exec(`insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid)
select 'mail',account,'public','101',rowid from mail_messages`)
		must0(err)
	}
	t.Run("alias read", func(t *testing.T) {
		sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
			_, err := s.MailGet(ctx, mailAcct, 101)
			return err
		})
	})
	t.Run("alias bind", func(t *testing.T) {
		sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
			return s.inTx(ctx, func(tx *sql.Tx) error { return bindWorkIQMailAlias(ctx, tx, mailAcct, "public", "102", 1) })
		})
	})
	t.Run("key reservation", func(t *testing.T) {
		sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
			return s.inTx(ctx, func(tx *sql.Tx) error {
				_, err := mailSourceKey(ctx, tx, mailAcct, "workiq:message", "another-message")
				return err
			})
		})
	})
}

func TestWorkIQStableIDCorruptAliasesAreNotCheckedEmpty(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, err := s.db.Exec(`insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail',?,'public','101',90000)`, mailAcct)
	must0(err)
	if _, err := s.MailGet(ctx, mailAcct, 101); err == nil || err == ErrMailNotFound {
		t.Fatal("orphan alias silently became a missing message")
	}
	_, err = s.db.Exec(`update workiq_aliases set alias='not-numeric'`)
	must0(err)
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := mailSourceKey(ctx, tx, mailAcct, "workiq:message", "new")
		return err
	})
	if err == nil || rowCount(t, s, `select count(*) from workiq_source_keys`) != 0 {
		t.Fatal("corrupt public key reservation allowed a new identity")
	}
}
