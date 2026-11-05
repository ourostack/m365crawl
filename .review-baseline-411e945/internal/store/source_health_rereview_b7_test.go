package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestReReviewB7LegacyScopedRecoveryKeepsOtherAccountFailure(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	must0(s.ApplyAccount(ctx, acctA))
	must0(s.ApplyAccount(ctx, acctB))
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, "Synthetic-A", "Rows", `"a"`, `{}`),
		grec(&acctB, "Synthetic-B", "Rows", `"b"`, `{}`),
	}, base)
	a, b := acctA.TenantID+"/"+acctA.UserID, acctB.TenantID+"/"+acctB.UserID
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "ok", Fingerprint: "historic", StartedAt: base, FinishedAt: base}))
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "failed", ErrorCode: "store_missing", StartedAt: base.Add(time.Second), FinishedAt: base.Add(time.Second)}))
	before, err := s.SourceHealth(ctx)
	if err != nil || len(before) != 2 {
		t.Fatalf("reproduction did not establish both accounts' failures: %+v %v", before, err)
	}
	must0(s.RecordRun(ctx, Run{Source: srcA, Status: "ok", SourceAccounts: []string{a}, StartedAt: base.Add(2 * time.Second), FinishedAt: base.Add(2 * time.Second)}))
	after, err := s.SourceHealth(ctx)
	if err != nil || len(after) != 1 || after[0].AccountID == nil || *after[0].AccountID != b {
		t.Fatalf("account-A recovery erased account-B legacy failure: before=%d after=%+v err=%v", len(before), after, err)
	}
}
