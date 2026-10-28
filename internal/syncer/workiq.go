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
	restart                 *workIQExpired
	restarted               map[string]bool
	pageWork                int
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
	if options.Source == "" {
		options.Source = "all"
	}
	if options.DBPath == "" || options.Source != "mail" && options.Source != "chats" && options.Source != "all" ||
		options.MaxChats < 0 || options.MaxChats > 100000 || !options.Since.IsZero() && options.Since.After(workIQNow()) {
		return report, errs.Usage("an explicit archive and valid source selection are required")
	}
	if options.Account != "" {
		family := "chats"
		if strings.HasPrefix(options.Account, "outlook/") {
			family = "mail"
		}
		if options.Source != "all" && options.Source != family {
			return report, errs.Usage("the selected archive account conflicts with the explicit source")
		}
		options.Source = family
	}
	if options.Source == "chats" {
		return runWorkIQChats(ctx, options)
	}
	if options.Source == "mail" {
		return runWorkIQMail(ctx, options)
	}
	var resultErr error
	for _, family := range []string{"mail", "chats"} {
		part := options
		part.Source = family
		var next WorkIQReport
		var err error
		if family == "mail" {
			next, err = runWorkIQMail(ctx, part)
		} else {
			next, err = runWorkIQChats(ctx, part)
		}
		report.Accounts = append(report.Accounts, next.Accounts...)
		for key, n := range next.Counts {
			report.Counts[key] += n
		}
		for key, n := range next.Gaps {
			report.Gaps[key] += n
		}
		resultErr = errors.Join(resultErr, err)
	}
	return report, resultErr
}

func runWorkIQMail(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Counts: map[string]int{}, Gaps: map[string]int{}}
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
		if err := s.VerifyWorkIQAttempt(writeCtx, attempt, binding, pinned, started); err != nil {
			return err
		}
		if options.restart != nil {
			return s.RestartWorkIQFrontier(writeCtx, attempt, options.restart.resource, options.restart.revision)
		}
		return nil
	})
	account := WorkIQAccountReport{Family: "mail", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	if err == nil {
		err = acquireWorkIQMail(ctx, options, session, attempt, binding.Fact.ID, &account)
	}
	cleanupErr := session.close() // cleanup must precede any positive outcome
	err = errors.Join(err, cleanupErr)
	account.Status = "ok"
	code := ""
	if err != nil {
		account.Status, code = "failed", "workiq_failed"
		var coded *errs.Coded
		if errors.As(err, &coded) {
			code = coded.Code
		}
		var expired *workIQExpired
		if errors.As(err, &expired) {
			code = "workiq_delta_expired"
		}
		gap := code
		if !strings.HasPrefix(gap, "workiq_") {
			gap = "workiq_failed"
		}
		account.Gaps[gap]++
	} else if len(account.Gaps) > 0 {
		account.Status = "ok_with_omissions"
	}
	var persistErr error
	if attempt != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		persistErr = workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
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
	var expired *workIQExpired
	if errors.As(err, &expired) && cleanupErr == nil && persistErr == nil {
		return retryWorkIQBaseline(ctx, options, report, expired)
	}
	return report, err
}
