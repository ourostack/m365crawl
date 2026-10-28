package syncer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQOptions struct {
	DBPath, Source, Account string
	Invocation              workiq.Invocation
	Since                   time.Time
	MaxChats                int
}

type WorkIQAccountReport struct {
	Family, Account, Status string
	Counts, Gaps            map[string]int
}

type WorkIQReport struct {
	Accounts []WorkIQAccountReport
	Counts   map[string]int
	Gaps     map[string]int
}

func RunWorkIQ(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Counts: map[string]int{}, Gaps: map[string]int{}}
	if options.DBPath == "" || options.Source != "mail" {
		return report, errs.Usage("an explicit archive and the mail source are required")
	}
	var enabled bool
	var facts []workiq.AccountFact
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		enabled, err = s.WorkIQEnabled(ctx)
		if err == nil && enabled {
			facts, err = s.WorkIQAccounts(ctx)
		}
		return err
	}); err != nil {
		return report, err
	}
	if !enabled {
		return report, workIQFailure("workiq_disabled")
	}
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		return report, err
	}
	binding, err := workiq.SelectMailAccount(user, facts, options.Account)
	if err != nil {
		return report, err
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return report, err
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	var attempt int64
	started := workIQNow()
	err = workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
		attempt, err = s.BeginWorkIQAttempt(writeCtx, store.WorkIQScope{Family: "mail", Account: binding.Fact.ID}, started)
		if err != nil {
			return err
		}
		return s.VerifyWorkIQAttempt(writeCtx, attempt, binding, pinned, started)
	})
	account := WorkIQAccountReport{Family: "mail", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	if err == nil {
		err = acquireWorkIQMail(ctx, options, session, attempt, binding.Fact.ID, &account)
	}
	err = errors.Join(err, session.close()) // cleanup must precede any positive outcome
	account.Status = "ok"
	code := ""
	if err != nil {
		account.Status, code = "failed", "workiq_failed"
		var coded *errs.Coded
		if errors.As(err, &coded) {
			code = coded.Code
		}
		gap := code
		if !strings.HasPrefix(gap, "workiq_") {
			gap = "workiq_failed"
		}
		account.Gaps[gap]++
	} else if len(account.Gaps) > 0 {
		account.Status = "ok_with_omissions"
	}
	if attempt != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		persistErr := workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.CompleteWorkIQAttempt(writeCtx, attempt, store.WorkIQSummary{
				Status: account.Status, ErrorCode: code, FinishedAt: workIQNow(), AllFolders: err == nil && len(account.Gaps) == 0,
				Folders: account.Counts["folders"], Counts: account.Counts, Gaps: account.Gaps,
			})
		})
		cancel()
		err = errors.Join(err, persistErr)
		if persistErr != nil {
			account.Status = "failed"
			account.Gaps["workiq_persistence_failed"]++
		}
	}
	report.Accounts = append(report.Accounts, account)
	for name, n := range account.Counts {
		report.Counts[name] += n
	}
	for name, n := range account.Gaps {
		report.Gaps[name] += n
	}
	return report, err
}
