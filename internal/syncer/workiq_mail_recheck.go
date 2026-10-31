package syncer

import (
	"context"

	"github.com/ourostack/m365crawl/internal/store"
)

func resetWorkIQMailCensus(ctx context.Context, options WorkIQOptions, attempt int64) error {
	var after int64
	for {
		var frontiers []store.WorkIQMailFrontier
		if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			frontiers, err = s.WorkIQMailResetFrontiers(ctx, attempt, after, 128)
			return err
		}); err != nil {
			return err
		}
		if len(frontiers) == 0 {
			return nil
		}
		for _, f := range frontiers {
			if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
				return s.RestartWorkIQFrontier(writeCtx, attempt, f.Resource, f.Revision)
			}); err != nil {
				return err
			}
			after = f.ID
		}
	}
}

func retryWorkIQMailCensus(ctx context.Context, options WorkIQOptions, report WorkIQReport) (WorkIQReport, error) {
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if options.censusPasses >= 2 {
		return report, workIQFailure("workiq_mail_census_pending")
	}
	options.restart = nil
	options.Source = "mail"
	options.pageWork = report.Counts["pages"]
	next, err := runWorkIQMail(ctx, options)
	// Effects are command totals; pages already carry the shared work budget,
	// folder totals describe the final inventory, and a resolved pending gap
	// must not survive as a current failure.
	for _, key := range []string{"mail_added", "mail_updated", "census_rechecks", "baseline_restarts"} {
		next.Counts[key] += report.Counts[key]
		for _, account := range next.Accounts {
			account.Counts[key] += report.Counts[key]
		}
	}
	return next, err
}
