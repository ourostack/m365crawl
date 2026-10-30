package syncer

import (
	"context"
	"errors"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func recordWorkIQStartupFailure(ctx context.Context, options WorkIQOptions, enabled bool, account WorkIQAccountReport, cause error) (WorkIQAccountReport, error) {
	if account.Counts == nil {
		account.Counts = map[string]int{}
	}
	if account.Gaps == nil {
		account.Gaps = map[string]int{}
	}
	account.Status = "failed"
	account.Gaps["workiq_startup_failed"]++
	if !enabled {
		return account, cause
	}
	code := workIQOutcomeCode(cause)
	kind := options.Kind
	if kind == "" {
		kind = "sync"
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workIQPersistBudget)
	defer cancel()
	at := workIQNow()
	err := workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
		id, err := s.BeginWorkIQAttempt(writeCtx, store.WorkIQScope{
			Family: account.Family, Account: account.Account, Kind: kind,
		}, at)
		if err != nil {
			return err
		}
		return s.CompleteWorkIQAttempt(writeCtx, id, store.WorkIQSummary{
			Status: "failed", ErrorCode: code, FinishedAt: workIQNow(), Gaps: account.Gaps,
		})
	})
	if err != nil {
		account.Gaps["workiq_persistence_failed"]++
		return account, errors.Join(errs.DBError(err), cause)
	}
	return account, cause
}

func failedWorkIQStartupReport(ctx context.Context, options WorkIQOptions, report WorkIQReport, facts []workiq.AccountFact, family, selected string, cause error) (WorkIQReport, error) {
	for _, fact := range facts {
		if selected != "" && fact.ID == selected && fact.Family == family {
			account, err := recordWorkIQStartupFailure(ctx, options, report.Enabled,
				WorkIQAccountReport{Family: family, Account: selected}, cause)
			report.Accounts = append(report.Accounts, account)
			for code, n := range account.Gaps {
				report.Gaps[code] += n
			}
			return report, err
		}
	}
	return report, cause
}
