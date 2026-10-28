package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func verifyWorkIQTestAttempt(t *testing.T, s *Store, attempt int64) {
	t.Helper()
	ctx := context.Background()
	var family, account string
	must0(s.db.QueryRowContext(ctx, `select family,account_id from workiq_attempts where id=?`, attempt).Scan(&family, &account))
	facts := must(s.WorkIQAccounts(ctx))
	me := workiq.User{ID: "mail-owner", Mail: "owner@example.invalid"}
	var selected workiq.AccountFact
	for _, fact := range facts {
		if fact.ID == account && fact.Family == family {
			selected = fact
			if fact.UserID != "" {
				me.ID = fact.UserID
			}
			if len(fact.Addresses) > 0 {
				me.Mail = fact.Addresses[0]
			}
		}
	}
	var binding workiq.AccountBinding
	if family == "mail" {
		binding = must(workiq.SelectMailAccount(me, facts, account))
	} else {
		binding = must(workiq.SelectChatAccount(me, workiq.Chat{ID: "fixture", TenantID: selected.TenantID}, facts, account))
	}
	must0(s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base))
}

func TestWorkIQBindingPersistsOnlyExistingMatchingOwner(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	account := acctA.TenantID + "/" + acctA.UserID
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: account}, base))
	me := workiq.User{ID: acctA.UserID, Mail: "owner@example.invalid"}
	binding := must(workiq.SelectChatAccount(me, workiq.Chat{ID: "chat", TenantID: acctA.TenantID}, must(s.WorkIQAccounts(ctx)), ""))
	must0(s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base))
	if rowCount(t, s, `select count(*) from workiq_accounts where family='chats' and account_id=? and user_id=?`, account, acctA.UserID) != 1 ||
		rowCount(t, s, `select count(*) from accounts`) != 1 || rowCount(t, s, `select count(*) from sync_runs`) != 0 {
		t.Fatal("verified binding was not persisted or fabricated an account/sync success")
	}
	other := me
	other.ID = "different-user"
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, other, base); err == nil {
		t.Fatal("fresh pinned identity mismatch was accepted")
	}
}

func TestWorkIQBindingBootstrapCannotOverrideNewIdentityOrChangeUser(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	account := "outlook/Failed"
	must0(s.RecordRun(ctx, Run{Source: "outlook|Failed|mail", Status: "failed", StartedAt: base, FinishedAt: base, SourceAccounts: []string{account}}))
	must0(s.SetWorkIQEnabled(ctx, true))
	me := workiq.User{ID: "original-user", Mail: "owner@example.invalid"}
	binding := must(workiq.SelectMailAccount(me, must(s.WorkIQAccounts(ctx)), account))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: account}, base))
	must0(s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base))
	if rowCount(t, s, `select count(*) from workiq_accounts where binding='explicit_bootstrap'`) != 1 ||
		rowCount(t, s, `select count(*) from meta where key like 'outlook_identity:%'`) != 0 {
		t.Fatal("explicit bootstrap fabricated or failed to retain source identity evidence")
	}
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values(?,?)`, outlookIdentityKey+account, "other@example.invalid")
	must0(err)
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: account}, base))
	if err := s.VerifyWorkIQAttempt(ctx, next, binding, me, base); err == nil {
		t.Fatal("explicit selector overrode a later contradictory native identity")
	}
	_, err = s.db.ExecContext(ctx, `update meta set value=? where key=?`, me.Mail, outlookIdentityKey+account)
	must0(err)
	other := me
	other.ID = "different-directory-user"
	changed := must(workiq.SelectMailAccount(other, must(s.WorkIQAccounts(ctx)), account))
	if err := s.VerifyWorkIQAttempt(ctx, next, changed, other, base); err == nil {
		t.Fatal("recycled address reassigned an already bound archive account")
	}
}

func TestWorkIQUnverifiedAttemptCannotStageData(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
	page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
	err := s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/me/chats", Page: page, ObservedAt: base})
	if err == nil || rowCount(t, s, `select count(*) from workiq_pages`) != 0 || rowCount(t, s, `select count(*) from workiq_frontiers`) != 0 {
		t.Fatal("unverified account attempt retained server data or progress")
	}
}

func TestWorkIQAttemptOwnerIsRevalidatedBeforeEachPage(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	account := "outlook/Failed"
	must0(s.RecordRun(ctx, Run{Source: "outlook|Failed|mail", Status: "failed", StartedAt: base, FinishedAt: base, SourceAccounts: []string{account}}))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: account}, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[],"@odata.nextLink":"/me/mailFolders/inbox/messages/delta()?$skiptoken=one"}}]}}`)))
	batch := WorkIQPageBatch{AttemptID: attempt, Resource: "/me/mailFolders/inbox/messages/delta()", Page: page, ObservedAt: base}
	must0(s.StageWorkIQPage(ctx, batch))
	_, err := s.db.ExecContext(ctx, `insert into meta(key,value) values(?,?)`, outlookIdentityKey+account, "contradictory@example.invalid")
	must0(err)
	batch.ExpectedRevision = 1
	if err := s.StageWorkIQPage(ctx, batch); err == nil ||
		rowCount(t, s, `select count(*) from workiq_pages`) != 1 || rowCount(t, s, `select revision from workiq_frontiers`) != 1 {
		t.Fatal("a later contradictory native identity did not stop retention before cursor progress")
	}
}

func TestWorkIQBindingPersistenceFaultsRollback(t *testing.T) {
	var attempt int64
	var binding workiq.AccountBinding
	var me workiq.User
	setup := func(t *testing.T, s *Store) {
		ctx := context.Background()
		must0(s.ApplyAccount(ctx, acctA))
		must0(s.SetWorkIQEnabled(ctx, true))
		account := acctA.TenantID + "/" + acctA.UserID
		attempt = must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: account}, base))
		me = workiq.User{ID: acctA.UserID, Mail: "owner@example.invalid"}
		binding = must(workiq.SelectChatAccount(me, workiq.Chat{ID: "chat", TenantID: acctA.TenantID}, must(s.WorkIQAccounts(ctx)), ""))
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		return s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base)
	})
}

func TestWorkIQUnverifiedAttemptCannotFabricateSuccess(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
	err := s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: base})
	if err == nil || rowCount(t, s, `select count(*) from sync_runs`) != 0 {
		t.Fatal("an unverified identity fabricated a successful source outcome")
	}
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_signin_required", FinishedAt: base}))
	before := dump(t, s)
	if err := s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_signin_required", FinishedAt: base}); err == nil ||
		dump(t, s) != before {
		t.Fatal("a repeated failed completion rewrote an immutable attempt outcome")
	}
}

func TestWorkIQCorruptAttemptVerificationIsNotRetentionAuthority(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
	verifyWorkIQTestAttempt(t, s, attempt)
	_, err := s.db.ExecContext(ctx, `update workiq_attempts set verified_at='not-a-clock' where id=?`, attempt)
	must0(err)
	page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
	if err := s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt, Resource: "/me/chats", Page: page, ObservedAt: base}); err == nil {
		t.Fatal("corrupt verification time became authority to retain server data")
	}
}

func TestWorkIQBindingRejectsDisabledClockScopeAndChangedAttempt(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.SetWorkIQEnabled(ctx, true))
	account := acctA.TenantID + "/" + acctA.UserID
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: account}, base))
	me := workiq.User{ID: acctA.UserID, Mail: "owner@example.invalid"}
	binding := must(workiq.SelectChatAccount(me, workiq.Chat{ID: "chat", TenantID: acctA.TenantID}, must(s.WorkIQAccounts(ctx)), ""))
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, me, time.Time{}); err == nil {
		t.Fatal("unknown verification time was accepted")
	}
	wrong := binding
	wrong.Fact.ID = "another/account"
	if err := s.VerifyWorkIQAttempt(ctx, attempt, wrong, me, base); err == nil {
		t.Fatal("binding escaped its attempt scope")
	}
	_, err := s.db.ExecContext(ctx, `update workiq_attempts set bound_user_id='someone-else' where id=?`, attempt)
	must0(err)
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base); err == nil {
		t.Fatal("changed attempt identity was silently rebound")
	}
	must0(s.SetWorkIQEnabled(ctx, false))
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, me, base); err == nil {
		t.Fatal("disabled source accepted identity verification")
	}
}

func TestWorkIQBindingRetentionRefusesDisabledCorruptOrChangedAuthority(t *testing.T) {
	for _, statement := range []string{
		`update meta set value='0' where key='workiq_enabled'`,
		`update workiq_attempts set verified_at='0001-01-01T00:00:00Z'`,
		`update workiq_attempts set bound_address=''`,
		`update workiq_accounts set user_id='someone-else'`,
		`update workiq_attempts set status='failed'`,
	} {
		s := newStore(t)
		ctx := context.Background()
		must0(s.ApplyAccount(ctx, acctA))
		must0(s.SetWorkIQEnabled(ctx, true))
		attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "chats", Account: acctA.TenantID + "/" + acctA.UserID}, base))
		verifyWorkIQTestAttempt(t, s, attempt)
		_, err := s.db.ExecContext(ctx, statement)
		must0(err)
		before := dump(t, s)
		if _, err := s.validateWorkIQAttemptOwner(ctx, attempt); err == nil || dump(t, s) != before {
			t.Fatal("disabled, corrupt or changed authority permitted retention or mutated state")
		}
	}
}
