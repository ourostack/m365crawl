package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

func twoAccountStore(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	must0(s.ApplyAccount(context.Background(), acctA))
	must0(s.ApplyAccount(context.Background(), acctB))
	return s
}

func at(d time.Duration) time.Time { return base.Add(d) }

func TestFreshnessIsPerAccount(t *testing.T) {
	ctx := context.Background()
	s := twoAccountStore(t)
	keyA := acctA.TenantID + "/" + acctA.UserID
	// A full sync refreshes every account.
	must0(s.RecordRun(ctx, Run{StartedAt: at(0), FinishedAt: at(time.Minute), Status: "ok", Accounts: []string{"*"}}))
	// A sync filtered to account A refreshes only A.
	must0(s.RecordRun(ctx, Run{StartedAt: at(time.Hour), FinishedAt: at(time.Hour + time.Minute), Status: "ok", Accounts: []string{keyA}}))
	// A partial or failed run refreshes nobody, and neither do per-source rows.
	must0(s.RecordRun(ctx, Run{StartedAt: at(2 * time.Hour), FinishedAt: at(2*time.Hour + time.Minute), Status: "partial", Accounts: []string{"*"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: at(3 * time.Hour), FinishedAt: at(3*time.Hour + time.Minute), Status: "failed", Accounts: []string{"*"}}))
	must0(s.RecordRun(ctx, Run{StartedAt: at(4 * time.Hour), FinishedAt: at(4*time.Hour + time.Minute), Source: "p|o", Fingerprint: "fp", Status: "ok"}))

	gotA := must(s.LastSuccessFor(ctx, keyA))
	gotB := must(s.LastSuccessFor(ctx, acctB.TenantID+"/"+acctB.UserID))
	gotAll := must(s.LastSuccess(ctx))
	if !gotA.Equal(at(time.Hour+time.Minute)) || !gotB.Equal(at(time.Minute)) || !gotAll.Equal(at(time.Minute)) {
		t.Fatalf("A %v, B %v, stalest %v", gotA, gotB, gotAll)
	}
	if st := must(s.Status(ctx)); !st.LastSuccessAt.Equal(at(time.Minute)) || st.LastRun == nil || st.LastRun.Status != "ok" {
		t.Fatalf("status %+v", st)
	}
}

func TestFreshnessOfAnAccountNeverSynced(t *testing.T) {
	ctx := context.Background()
	s := twoAccountStore(t)
	keyA := acctA.TenantID + "/" + acctA.UserID
	must0(s.RecordRun(ctx, Run{StartedAt: at(0), FinishedAt: at(time.Minute), Status: "ok", Accounts: []string{keyA}}))
	if got := must(s.LastSuccess(ctx)); !got.IsZero() {
		t.Fatalf("account B was never synced, so the stalest is zero, got %v", got)
	}
}

func TestFreshnessWithoutAccountsUsesAnyRun(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.RecordRun(ctx, Run{StartedAt: at(0), FinishedAt: at(time.Minute), Status: "unchanged", Accounts: []string{"*"}}))
	if got := must(s.LastSuccess(ctx)); !got.Equal(at(time.Minute)) {
		t.Fatalf("got %v", got)
	}
}

func TestMigrateAddsAccountsJSONToOlderSyncRuns(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "old.db")
	s := must(Open(ctx, p))
	must0(s.RecordRun(ctx, Run{StartedAt: at(0), FinishedAt: at(time.Minute), Source: "p|o", Fingerprint: "fp", Status: "ok"}))
	must0(s.RecordRun(ctx, Run{StartedAt: at(time.Hour), FinishedAt: at(time.Hour + time.Minute), Source: "p|o", Fingerprint: "fp2", Status: "failed"}))
	if _, err := s.db.ExecContext(ctx, `alter table sync_runs drop column accounts_json`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = must(Open(ctx, p))
	defer func() { _ = s.Close() }()
	var n int
	must0(s.db.QueryRow(`select count(*) from pragma_table_info('sync_runs') where name='accounts_json'`).Scan(&n))
	if n != 1 {
		t.Fatal("accounts_json was not added")
	}
	// A run recorded before run-level rows existed counted for every account, and still does.
	if got := must(s.LastSuccess(ctx)); !got.Equal(at(time.Minute)) {
		t.Fatalf("legacy row survives: %v", got)
	}
	if got := must(s.LastSuccessFor(ctx, "any/account")); !got.Equal(at(time.Minute)) {
		t.Fatalf("legacy row covers every account: %v", got)
	}
}

func TestMigrateSyncRunsFailureIsReported(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	defer func() { _ = s.Close() }()
	if _, err := s.db.ExecContext(ctx, `drop table sync_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `create view sync_runs as select 1 as id`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(ctx); err == nil {
		t.Fatal("migrate over a sync_runs view must fail")
	}
}

func TestFreshnessQueryErrors(t *testing.T) {
	ctx := context.Background()
	s := twoAccountStore(t)
	must0(s.RecordRun(ctx, Run{StartedAt: at(0), FinishedAt: at(time.Minute), Status: "ok", Accounts: []string{"*"}}))
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.LastSuccess(cctx); err == nil {
		t.Fatal("LastSuccess on a cancelled context")
	}
	if _, err := s.LastSuccessFor(cctx, "a/b"); err == nil {
		t.Fatal("LastSuccessFor on a cancelled context")
	}
}

func TestDerivationVersion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if v := must(s.DerivationVersion(ctx)); v != alpha1Version {
		t.Fatalf("no row means alpha.1, got %d", v)
	}
	must0(s.inTx(ctx, func(tx *sql.Tx) error { _, err := rederive(ctx, tx); return err }))
	if v := must(s.DerivationVersion(ctx)); v != DerivationVersion {
		t.Fatalf("after rederive: %d", v)
	}
	if _, err := s.db.ExecContext(ctx, `update meta set value='x' where key='derivation_version'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DerivationVersion(ctx); err == nil {
		t.Fatal("a non-numeric version must be an error")
	}
	if _, err := s.db.ExecContext(ctx, `drop table meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DerivationVersion(ctx); err == nil {
		t.Fatal("a missing meta table must be an error")
	}
}

func TestCheckTeam(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	err := s.CheckTeam(ctx, nil, "nothing")
	var c *errs.Coded
	if !errors.As(err, &c) || c.Code != errs.CodeUsage || !strings.Contains(c.Fix, "teamscrawl sync") {
		t.Fatalf("unknown team: %v", err)
	}
	team := conv(acctA, "19:t@thread.v2", "Space", "Alpha team")
	team.TeamID = "19:t@thread.v2"
	chat := conv(acctA, "19:c@thread.v2", "Topic", "Alpha channel")
	chat.TeamID = "19:t@thread.v2"
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{team, chat}))
	if err := s.CheckTeam(ctx, &acctA, "Alpha team"); err != nil {
		t.Fatalf("known team: %v", err)
	}
}
