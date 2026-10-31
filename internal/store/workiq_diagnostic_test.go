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

// A gap a page or mail group already recorded counts toward the run's total once: the run-level
// row (empty resource) holds only the rest, so the table sums to the run's gaps.
func TestWorkIQGapTableStoresEachGapOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	attempt := seedWorkIQMailAttempt(t, s)
	for _, row := range []struct {
		code, resource string
		n              int
	}{{"workiq_message_identity_unverified", "chat-page:1:0", 2}, {"workiq_message_identity_unverified", "chat-page:2:0", 1}, {"workiq_clock_unknown", "chat-page:1:0", 4}} {
		must(s.db.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,resource,n) values(?,?,?,?)`, attempt, row.code, row.resource, row.n))
	}
	// Every failing query leaves the attempt open and the table unchanged; the unfaulted call settles it.
	sweepKind(t, s, injectFaults(t, s), "query", true, func(ctx context.Context, s *Store) error {
		return s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0,
			Gaps: map[string]int{"workiq_message_identity_unverified": 5, "workiq_clock_unknown": 4, "workiq_selection_capped": 1}})
	}, nil)
	for code, want := range map[string]int{"workiq_message_identity_unverified": 5, "workiq_clock_unknown": 4, "workiq_selection_capped": 1} {
		var got int
		must0(s.db.QueryRowContext(ctx, `select coalesce(sum(n),0) from workiq_gaps where attempt_id=? and code=?`, attempt, code).Scan(&got))
		if got != want {
			t.Fatalf("%s sums to %d, want %d", code, got, want)
		}
	}
	var runRows int
	must0(s.db.QueryRowContext(ctx, `select count(*) from workiq_gaps where attempt_id=? and resource='' and code='workiq_clock_unknown'`, attempt).Scan(&runRows))
	if runRows != 0 {
		t.Fatal("a gap fully recorded by its pages was stored again as a run total")
	}
}
