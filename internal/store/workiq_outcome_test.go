package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQProbeKindsCannotWriteDataOrClearDataOutcomeKeys(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"coverage", "status"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			_ = seedWorkIQMailAttempt(t, s)
			attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: kind}, mailT0))
			verifyWorkIQTestAttempt(t, s, attempt)
			page := must(workiq.DecodePage(ctx, []byte(`{"structuredContent":{"results":[{"statusCode":200,"data":{"value":[]}}]}}`)))
			before := dump(t, s)
			if err := s.StageWorkIQPage(ctx, WorkIQPageBatch{AttemptID: attempt,
				Resource: "/me/mailFolders?includeHiddenFolders=true", Page: page, ObservedAt: mailT0}); err == nil || dump(t, s) != before {
				t.Fatal("counts/status attempt obtained data retention or cursor permission")
			}
			must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0, AllFolders: true}))
			key := `workiq:["mail","` + mailAcct + `","` + kind + `"]`
			if rowCount(t, s, `select count(*) from sync_runs where source=? and status='ok'`, key) != 1 ||
				rowCount(t, s, `select count(*) from workiq_attempts where id=? and all_folders=1`, attempt) != 0 {
				t.Fatal("probe overwrote a data-sync outcome or acquired a covering-success claim")
			}
		})
	}
}

func TestWorkIQOutcomesPreserveActualNanosecondStartAndCompletionClocks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_ = seedWorkIQMailAttempt(t, s)
	start := mailT0.Add(123456789 * time.Nanosecond)
	finish := start.Add(789 * time.Nanosecond)
	attempt := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, start))
	verifyWorkIQTestAttempt(t, s, attempt)
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: finish}))
	var started, finished string
	must0(s.db.QueryRowContext(ctx, `select started_at,finished_at from workiq_attempts where id=?`, attempt).Scan(&started, &finished))
	if !must(time.Parse(time.RFC3339Nano, started)).Equal(start) || !must(time.Parse(time.RFC3339Nano, finished)).Equal(finish) {
		t.Fatal("data attempt clocks were rounded into different source evidence")
	}
	if _, err := s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "write"}, finish); err == nil {
		t.Fatal("unsupported outcome kind acquired an attempt")
	}
}
