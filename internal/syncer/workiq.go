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
	Kind                    string
	Invocation              workiq.Invocation
	Since                   time.Time
	MaxChats                int
	restart                 *workIQExpired
	restarted               map[string]bool
	pageWork                int
	censusPasses            int
	runLocks                *workIQRunLocks
}

type WorkIQAccountReport struct {
	Family  string         `json:"family"`
	Account string         `json:"account_id"`
	Status  string         `json:"status"`
	Counts  map[string]int `json:"counts"`
	Gaps    map[string]int `json:"gaps"`
	// Diagnostic is the redacted trace a failed WorkIQ child left: exit status and output tails.
	Diagnostic *workiq.Diagnostic `json:"diagnostic,omitempty"`
}

type WorkIQReport struct {
	Accounts []WorkIQAccountReport `json:"accounts"`
	Counts   map[string]int        `json:"counts"`
	Gaps     map[string]int        `json:"gaps"`
	Kind     string                `json:"kind"`
	Enabled  bool                  `json:"enabled"`
	Mail     []WorkIQMailCoverage  `json:"mail"`
	Chats    []WorkIQChatCoverage  `json:"chats"`
	Probes   []WorkIQPathProbe     `json:"probes"`
	// Resumable says the run stopped only at a resumable stop (page budget, busy archive or a
	// transient server failure) with its position kept, so the same command run again continues.
	// It is not part of the report's JSON shape.
	Resumable bool `json:"-"`
}

type WorkIQMailCoverage struct {
	Account      string `json:"account_id"`
	FolderID     string `json:"folder_id"`
	Basis        string `json:"basis"`
	ServerTotal  *int64 `json:"server_total"`
	ArchiveCount *int   `json:"archive_count"`
	Overlap      *int   `json:"overlap"`
}

type WorkIQChatCoverage struct {
	Account      string    `json:"account_id"`
	Conversation string    `json:"conversation_id"`
	ServerBasis  string    `json:"server_basis"`
	ArchiveBasis string    `json:"archive_basis"`
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	ServerCount  int       `json:"server_count"`
	ArchiveCount int       `json:"archive_count"`
	UnknownClock int       `json:"unknown_clock"`
	Complete     bool      `json:"complete"`
}

type WorkIQPathProbe struct {
	Family    string `json:"family"`
	Account   string `json:"account_id,omitempty"`
	PathClass string `json:"path_class"`
	State     string `json:"state"`
	Code      string `json:"code,omitempty"`
	Scope     string `json:"scope"`
	// Next is the exact command that probes this account when it needs an explicit --account; the
	// command line fills it in.
	Next string `json:"next,omitempty"`
}

func RunWorkIQ(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Kind: "sync", Counts: map[string]int{}, Gaps: map[string]int{}}
	if options.Kind == "" {
		options.Kind = "sync"
	}
	if options.Source == "" {
		options.Source = "all"
	}
	if options.DBPath == "" || options.Source != "mail" && options.Source != "chats" && options.Source != "all" ||
		options.Kind != "sync" && options.Kind != "coverage" && options.Kind != "status" ||
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
	if options.runLocks == nil {
		options.runLocks = &workIQRunLocks{}
		defer options.runLocks.release()
	}
	if options.Kind != "sync" {
		return runWorkIQProbe(ctx, options)
	}
	if options.Source == "chats" {
		return runWorkIQChats(ctx, options)
	}
	if options.Source == "mail" {
		return runWorkIQMail(ctx, options)
	}
	var resultErr, mailErr, chatsErr error
	mailResumable, chatsResumable := false, false
	for _, family := range []string{"mail", "chats"} {
		part := options
		part.Source = family
		var next WorkIQReport
		var err error
		if family == "mail" {
			next, err = runWorkIQMail(ctx, part)
			mailErr, mailResumable = err, next.Resumable
		} else {
			next, err = runWorkIQChats(ctx, part)
			chatsErr, chatsResumable = err, next.Resumable
		}
		report.Accounts = append(report.Accounts, next.Accounts...)
		report.Enabled = report.Enabled || next.Enabled
		for key, n := range next.Counts {
			report.Counts[key] += n
		}
		for key, n := range next.Gaps {
			report.Gaps[key] += n
		}
		resultErr = errors.Join(resultErr, err)
	}
	report.Resumable = resultErr != nil && (mailErr == nil || mailResumable) && (chatsErr == nil || chatsResumable)
	return report, resultErr
}

// workIQConfirmCensus confirms a finished mail census in bounded batches; a test seam.
var workIQConfirmCensus = func(ctx context.Context, path string, attempt int64, limit int) (done bool, err error) {
	err = workIQWrite(ctx, path, func(writeCtx context.Context, s *store.Store) (err error) {
		done, err = s.ConfirmWorkIQMailCensusIfFull(writeCtx, attempt, limit)
		return err
	})
	return done, err
}

// workIQResumableCodes are the mail and chat sync stops that keep their position, so the same command run
// again continues from it.
var workIQResumableCodes = map[string]bool{"workiq_page_limit": true, "workiq_archive_busy": true, "workiq_retry_later": true}

func runWorkIQMail(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Kind: "sync", Counts: map[string]int{}, Gaps: map[string]int{}}
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
	report.Enabled = enabled
	if !enabled {
		return report, workIQFailure("workiq_disabled")
	}
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", options.Account, err)
	}
	binding, err := workiq.SelectMailAccount(user, facts, options.Account)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", options.Account, workIQAccountChoice(err, facts))
	}
	options.Account = binding.Fact.ID
	if err := holdWorkIQScope(ctx, options, store.WorkIQScope{Family: "mail", Account: binding.Fact.ID}); err != nil {
		return report, err
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "mail", binding.Fact.ID, err)
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
	pending, fullRecheck := false, false
	if err == nil {
		err = workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			pending, err = s.WorkIQMailRecheckPending(ctx, attempt)
			return err
		})
		if err == nil && pending {
			fullRecheck = true
			options.censusPasses++
			// Reset once per owed check: a run after a page-budget stop continues the same rebuild,
			// so a recheck larger than one run's page budget still finishes.
			continues := false
			err = workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
				continues, err = s.WorkIQMailRecheckContinues(ctx, attempt)
				return err
			})
			if err == nil && !continues {
				err = resetWorkIQMailCensus(ctx, options, attempt)
				if err == nil {
					err = workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
						return s.MarkWorkIQMailRecheckRestarted(writeCtx, attempt)
					})
				}
			}
			if err == nil {
				account.Counts["census_rechecks"] = 1
			}
		}
	}
	// admitted says this run's traversal finished and admitted its cursors; after that a recheck
	// can no longer be continued, only restarted.
	admitted := false
	if err == nil {
		err = acquireWorkIQMail(ctx, options, session, attempt, binding.Fact.ID, &account)
		admitted = err == nil
	}
	cleanupErr := session.close() // close before absence confirmation or positive outcome
	err = errors.Join(err, cleanupErr)
	if err == nil && len(account.Gaps) == 0 {
		done, limit := false, 128
		for !done {
			done, err = workIQConfirmCensus(ctx, options.DBPath, attempt, limit)
			if err != nil {
				if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && limit > 1 {
					limit /= 2
					continue
				}
				break
			}
		}
	}
	if err == nil && len(account.Gaps) == 0 {
		err = workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			pending, err = s.WorkIQMailRecheckPending(ctx, attempt)
			return err
		})
		if pending {
			account.Counts["census_pending"] = 1
			if !fullRecheck {
				account.Gaps["workiq_mail_census_pending"]++
			}
		}
	}
	var mailProof *store.WorkIQMailCompletionProof
	if err == nil && len(account.Gaps) == 0 && !pending {
		err = workIQRead(ctx, options.DBPath, func(s *store.Store) error {
			proof, err := s.InspectWorkIQMailCompletion(ctx, attempt)
			if err == nil {
				mailProof = &proof
			}
			return err
		})
	}
	account.Status = "ok"
	code := ""
	account.Diagnostic = workIQDiagnostic(err)
	if err != nil {
		account.Status, code = "failed", workIQOutcomeCode(err)
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
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workIQPersistBudget)
		persistErr = workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.CompleteWorkIQAttempt(writeCtx, attempt, store.WorkIQSummary{
				Status: account.Status, ErrorCode: code, FinishedAt: workIQNow(), AllFolders: err == nil && len(account.Gaps) == 0 && !pending,
				Folders: account.Counts["folders"], Counts: account.Counts, Gaps: account.Gaps,
				MailProof: mailProof, Diagnostic: account.Diagnostic,
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
	// Only a stop that kept its position is resumable; an owed recheck continues across such stops
	// rather than resetting. A recheck that already admitted its cursors cannot be continued, so a
	// later stop in it (a busy archive while confirming its census) is not resumable.
	report.Resumable = workIQResumableCodes[code] && account.Gaps[code] > 0 && cleanupErr == nil && persistErr == nil && (!fullRecheck || !admitted)
	var expired *workIQExpired
	if errors.As(err, &expired) && cleanupErr == nil && persistErr == nil {
		return retryWorkIQBaseline(ctx, options, report, expired)
	}
	if err == nil && pending && (len(account.Gaps) == 0 || len(account.Gaps) == 1 && account.Gaps["workiq_mail_census_pending"] == 1) {
		return retryWorkIQMailCensus(ctx, options, report)
	}
	return report, err
}
