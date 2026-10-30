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

// A sync attempt keeps where and why it stopped, and the scope's latest settled attempt answers for
// the next run of that scope: an open attempt, another kind or another account never does.
func TestWorkIQLastStopIsTheScopesLatestSettledAttempt(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if stop := must(s.LastWorkIQStop(ctx, "mail", mailAcct, "sync")); stop != "" {
		t.Fatalf("a scope without attempts had a stop %q", stop)
	}
	attempt := seedWorkIQMailAttempt(t, s)
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_retry_later", FinishedAt: mailT0, StopAt: "page workiq_failed"}))
	open := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0))
	coverage := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "coverage"}, mailT0))
	must0(s.CompleteWorkIQAttempt(ctx, coverage, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0}))
	if stop := must(s.LastWorkIQStop(ctx, "mail", mailAcct, "sync")); stop != "page workiq_failed" {
		t.Fatalf("stop = %q", stop)
	}
	if stop := must(s.LastWorkIQStop(ctx, "mail", mailAcct, "coverage")); stop != "" {
		t.Fatalf("another kind's stop answered: %q", stop)
	}
	if stop := must(s.LastWorkIQStop(ctx, "mail", "outlook/Other", "sync")); stop != "" {
		t.Fatalf("another account's stop answered: %q", stop)
	}
	must0(s.CompleteWorkIQAttempt(ctx, open, WorkIQSummary{Status: "failed", ErrorCode: "workiq_page_limit", FinishedAt: mailT0}))
	if stop := must(s.LastWorkIQStop(ctx, "mail", mailAcct, "sync")); stop != "" {
		t.Fatalf("a later run that stopped elsewhere still answered with an older stop: %q", stop)
	}
	must0(s.Close())
	if _, err := s.LastWorkIQStop(ctx, "mail", mailAcct, "sync"); err == nil {
		t.Fatal("a closed archive answered")
	}
}
