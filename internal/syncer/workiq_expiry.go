package syncer

import (
	"context"
)

type workIQExpired struct {
	resource string
	revision int64
}

func (*workIQExpired) Error() string    { return "workiq_delta_expired" }
func (*workIQExpired) GoString() string { return "workiq_delta_expired" }

// Each restart settles the failed attempt, closes its client, then uses a new
// verified attempt before retiring one frontier. No staged page is deleted.
func retryWorkIQBaseline(ctx context.Context, options WorkIQOptions, report WorkIQReport, expired *workIQExpired) (WorkIQReport, error) {
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if options.restarted == nil {
		options.restarted = map[string]bool{}
	}
	if options.restarted[expired.resource] || len(options.restarted) >= 16 {
		return report, workIQFailure("workiq_delta_expired")
	}
	options.restarted[expired.resource] = true
	options.restart = expired
	options.pageWork = report.Counts["pages"]
	// Recovery belongs only to the failed mail family, never to the enclosing
	// all-source command's independently completed chat work.
	options.Source = "mail"
	next, err := runWorkIQMail(ctx, options)
	next.Counts["baseline_restarts"]++
	for _, account := range next.Accounts {
		account.Counts["baseline_restarts"]++
	}
	// The restarted run settled its own attempt; report that outcome so the printed and stored codes
	// agree, whether it kept its position (resumable) or not.
	if err != nil {
		return next, err
	}
	return next, nil
}
