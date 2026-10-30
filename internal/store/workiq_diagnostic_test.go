package store

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// A failed attempt keeps the WorkIQ child's redacted diagnostic, and source health (doctor and
// status) shows it with the failure it explains.
func TestWorkIQAttemptKeepsItsChildDiagnosticForHealth(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQMailAttempt(t, s)
	d := workiq.Diagnostic{Exit: "exit status 3", Stderr: "fatal: lost connection for [address]"}
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_retry_later", FinishedAt: mailT0, Diagnostic: &d}))
	health := must(s.SourceHealth(ctx))
	if len(health) != 1 || health[0].LastErrorCode != "workiq_retry_later" || health[0].LastDiagnostic == nil || *health[0].LastDiagnostic != d {
		t.Fatalf("health lost the failure's diagnostic: %+v", health)
	}
	next := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	must0(s.CompleteWorkIQAttempt(ctx, next, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0}))
	if health := must(s.SourceHealth(ctx)); len(health) != 1 || health[0].LastDiagnostic != nil {
		t.Fatal("an older attempt's diagnostic explained a newer failure")
	}
	must(s.db.ExecContext(ctx, `update meta set value='{' where key like 'workiq_diagnostic:%'`))
	must(s.db.ExecContext(ctx, `insert into meta(key,value) values(?, '{')`, "workiq_diagnostic:"+itoa(int(next))))
	if _, err := s.SourceHealth(ctx); err == nil {
		t.Fatal("a malformed saved diagnostic was accepted")
	}
}
