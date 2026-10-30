package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func runWorkIQProbe(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Kind: options.Kind, Counts: map[string]int{}, Gaps: map[string]int{}}
	var facts []workiq.AccountFact
	err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		report.Enabled, err = s.WorkIQEnabled(ctx)
		if err == nil {
			facts, err = s.WorkIQAccounts(ctx)
		}
		return err
	})
	if err != nil {
		return report, err
	}
	if !report.Enabled && options.Kind != "status" {
		return report, workIQFailure("workiq_disabled")
	}
	if options.Account != "" {
		known := false
		for _, fact := range facts {
			known = known || fact.ID == options.Account && fact.Family == options.Source
		}
		if !known {
			code := "workiq_chat_account_unmatched"
			if options.Source == "mail" {
				code = "workiq_mail_account_missing"
			}
			return report, workIQFailure(code)
		}
	}
	if options.MaxChats == 0 {
		options.MaxChats = 200
	}
	for _, family := range []string{"mail", "chats"} {
		if options.Source != "all" && options.Source != family {
			continue
		}
		var next error
		switch {
		case family == "mail":
			next = probeWorkIQMail(ctx, options, facts, &report)
		case options.Kind == "status":
			next = probeWorkIQChatStatus(ctx, options, facts, &report)
		default:
			next = measureWorkIQChats(ctx, options, facts, &report)
		}
		err = errors.Join(err, recordWorkIQFailure(ctx, options, &report, family, next))
		if ctx.Err() != nil {
			break
		}
	}
	return report, err
}

func beginWorkIQProbe(ctx context.Context, options WorkIQOptions, enabled bool, binding workiq.AccountBinding, user workiq.User) (int64, error) {
	if !enabled {
		return 0, nil
	}
	if err := holdWorkIQScope(ctx, options, store.WorkIQScope{Family: binding.Fact.Family, Account: binding.Fact.ID, Kind: options.Kind}); err != nil {
		return 0, err
	}
	var attempt int64
	at := workIQNow()
	err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
		attempt, err = s.BeginWorkIQAttempt(writeCtx, store.WorkIQScope{
			Family: binding.Fact.Family, Account: binding.Fact.ID, Kind: options.Kind,
		}, at)
		if err != nil {
			return err
		}
		return s.VerifyWorkIQAttempt(writeCtx, attempt, binding, user, at)
	})
	return attempt, err
}

func finishWorkIQProbe(ctx context.Context, options WorkIQOptions, report *WorkIQReport, account WorkIQAccountReport, attempt int64, cause error) error {
	account.Status = "ok"
	code := ""
	account.Diagnostic = workIQDiagnostic(cause)
	if cause != nil {
		account.Status, code = "failed", workIQOutcomeCode(cause)
		account.Gaps["workiq_probe_failed"]++
	} else if len(account.Gaps) > 0 {
		account.Status = "ok_with_omissions"
	}
	if attempt != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workIQPersistBudget)
		err := workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.CompleteWorkIQAttempt(writeCtx, attempt, store.WorkIQSummary{Status: account.Status,
				ErrorCode: code, FinishedAt: workIQNow(), Counts: account.Counts, Gaps: account.Gaps, Diagnostic: account.Diagnostic})
		})
		cancel()
		if err != nil {
			account.Status = "failed"
			account.Gaps["workiq_persistence_failed"]++
		}
		cause = errors.Join(cause, err)
	}
	report.Accounts = append(report.Accounts, account)
	for key, n := range account.Counts {
		report.Counts[key] += n
	}
	for key, n := range account.Gaps {
		report.Gaps[key] += n
	}
	return cause
}

func workIQCollection(page workiq.Page) ([]json.RawMessage, error) {
	var collection struct {
		Value *[]json.RawMessage `json:"value"`
	}
	if json.Unmarshal(page.Data, &collection) != nil || collection.Value == nil {
		return nil, workIQFailure("workiq_protocol")
	}
	return *collection.Value, nil
}

func probeWorkIQMail(ctx context.Context, options WorkIQOptions, facts []workiq.AccountFact, report *WorkIQReport) error {
	options.Source = "mail"
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		var failure error
		*report, failure = failedWorkIQStartupReport(ctx, options, *report, facts, "mail", options.Account, err)
		return failure
	}
	binding, err := workiq.SelectMailAccount(user, facts, options.Account)
	var coded *errs.Coded
	if errors.As(err, &coded) && coded.Code == "workiq_account_required" && options.Kind == "status" {
		// Status probes what it can: name each Outlook account that needs an explicit selector.
		for _, fact := range facts {
			if fact.Family == "mail" {
				report.Probes = append(report.Probes, WorkIQPathProbe{Family: "mail", Account: fact.ID, PathClass: "mail_folders",
					State: "account_required", Code: coded.Code, Scope: "initial_page_only"})
				report.Gaps[coded.Code]++
			}
		}
		return nil
	}
	if err != nil {
		var failure error
		*report, failure = failedWorkIQStartupReport(ctx, options, *report, facts, "mail", options.Account, workIQAccountChoice(err, facts))
		return failure
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		var failure error
		*report, failure = failedWorkIQStartupReport(ctx, options, *report, facts, "mail", binding.Fact.ID, err)
		return failure
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	account := WorkIQAccountReport{Family: "mail", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	attempt, err := beginWorkIQProbe(ctx, options, report.Enabled, binding, pinned)
	if err == nil {
		if options.Kind == "status" {
			_, err = readWorkIQProbeCollection(ctx, session, "/me/mailFolders?includeHiddenFolders=true")
			state, code := workIQProbeState(err)
			report.Probes = append(report.Probes, WorkIQPathProbe{Family: "mail", Account: binding.Fact.ID,
				PathClass: "mail_folders", State: state, Code: code, Scope: "initial_page_only"})
		} else {
			err = measureWorkIQMail(ctx, options, session, binding.Fact.ID, &account, report)
		}
	}
	err = errors.Join(err, session.close())
	return finishWorkIQProbe(ctx, options, report, account, attempt, err)
}

func readWorkIQProbeCollection(ctx context.Context, session *workIQSession, path string) ([]json.RawMessage, error) {
	page, err := session.fetch(ctx, path)
	if err != nil {
		return nil, err
	}
	return workIQCollection(page)
}

func workIQProbeState(err error) (string, string) {
	if err == nil {
		return "allowed", ""
	}
	code := "workiq_failed"
	var coded *errs.Coded
	if errors.As(err, &coded) {
		code = coded.Code
	}
	return "failed", code
}

func measureWorkIQMail(ctx context.Context, options WorkIQOptions, session *workIQSession, id string, account *WorkIQAccountReport, report *WorkIQReport) error {
	type folderJob struct{ path, parent string }
	jobs := []folderJob{{path: "/me/mailFolders?includeHiddenFolders=true"}}
	seen := map[string]bool{}
	bytes := 0
	for len(jobs) > 0 {
		job := jobs[0]
		jobs = jobs[1:]
		base := job.path
		for path := base; path != ""; {
			account.Counts["pages"]++
			if account.Counts["pages"] > 10000 {
				return workIQFailure("workiq_page_limit")
			}
			page, err := session.fetch(ctx, path)
			if err != nil {
				return err
			}
			items, err := workIQCollection(page)
			if err != nil {
				return err
			}
			for _, raw := range items {
				bytes += len(raw)
				if len(seen) >= 100000 || bytes > 64*1024*1024 {
					return workIQFailure("workiq_folder_limit")
				}
				folder, err := workiq.DecodeFolder(ctx, raw)
				if err != nil {
					return err
				}
				if seen[folder.ID] || job.parent != "" && folder.ParentID != "" && folder.ParentID != job.parent {
					return workIQFailure("workiq_protocol")
				}
				seen[folder.ID] = true
				if folder.TotalCount == nil {
					account.Gaps["workiq_folder_count_unknown"]++
				}
				var local *int
				if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
					local, err = s.WorkIQMailFolderCount(ctx, id, folder.ID)
					return err
				}); err != nil {
					return err
				}
				report.Mail = append(report.Mail, WorkIQMailCoverage{Account: id, FolderID: folder.ID,
					Basis: "folder_total", ServerTotal: folder.TotalCount, ArchiveCount: local})
				if folder.ChildCount == nil || *folder.ChildCount > 0 {
					jobs = append(jobs, folderJob{path: "/me/mailFolders/" + url.PathEscape(folder.ID) +
						"/childFolders?includeHiddenFolders=true", parent: folder.ID})
				}
				account.Counts["folders"]++
			}
			path = page.NextCursor
			if path != "" {
				u, _ := url.ParseRequestURI(base)
				path, err = workiq.RelativePath(path, u.Path)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func measureWorkIQChats(ctx context.Context, options WorkIQOptions, facts []workiq.AccountFact, report *WorkIQReport) error {
	chats, err := discoverWorkIQChats(ctx, options, facts, report)
	if err != nil {
		return err
	}
	groups := map[string][]workIQSelectedChat{}
	var ids []string
	for _, chat := range chats {
		id := chat.binding.Fact.ID
		if _, ok := groups[id]; !ok {
			ids = append(ids, id)
		}
		groups[id] = append(groups[id], chat)
	}
	sort.Strings(ids)
	for _, id := range ids {
		err = errors.Join(err, measureWorkIQChatAccount(ctx, options, groups[id], report))
		if ctx.Err() != nil {
			break
		}
	}
	return err
}

func measureWorkIQChatAccount(ctx context.Context, options WorkIQOptions, chats []workIQSelectedChat, report *WorkIQReport) error {
	binding := chats[0].binding
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return err
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	account := WorkIQAccountReport{Family: "chats", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	attempt, err := beginWorkIQProbe(ctx, options, report.Enabled, binding, pinned)
	end, start := workIQNow(), options.Since
	if start.IsZero() {
		start = end.Add(-365 * 24 * time.Hour)
	}
	if err == nil {
		for _, selected := range chats {
			next := measureWorkIQChat(ctx, options, session, selected, start, end, &account, report)
			err = errors.Join(err, next)
			if ctx.Err() != nil {
				break
			}
		}
	}
	err = errors.Join(err, session.close())
	return finishWorkIQProbe(ctx, options, report, account, attempt, err)
}

func measureWorkIQChat(ctx context.Context, options WorkIQOptions, session *workIQSession, selected workIQSelectedChat,
	start, end time.Time, account *WorkIQAccountReport, report *WorkIQReport) error {
	row := WorkIQChatCoverage{Account: selected.binding.Fact.ID, Conversation: selected.chat.ID, Since: start, Until: end,
		ServerBasis: "all_message_types", ArchiveBasis: "comparable_message_types"}
	if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		row.ArchiveCount, err = s.WorkIQChatCoverageCount(ctx, selected.binding.Fact, selected.chat.ID, start, end)
		return err
	}); err != nil {
		return err
	}
	base := "/chats/" + url.PathEscape(selected.chat.ID) + "/messages"
	var result error
	seen := map[string]string{} // ID to the exact server clocks first seen
	identityBytes := 0
	changed := false
	for path := base; path != ""; {
		account.Counts["pages"]++
		if account.Counts["pages"] > 10000 {
			result = workIQFailure("workiq_page_limit")
			break
		}
		page, err := session.fetch(ctx, path)
		if err != nil {
			result = err
			break
		}
		items, err := workIQCollection(page)
		if err != nil {
			result = err
			break
		}
		for _, raw := range items {
			header, err := workiq.DecodeMessageHeader(ctx, raw)
			if err != nil {
				result = err
				break
			}
			// Live, Graph repeats unchanged messages on the next page: count those once. A repeat with
			// other clocks changed during traversal, so this chat's count is incomplete.
			if version, ok := seen[header.ID]; ok {
				if version == header.Version {
					account.Counts["duplicates_skipped"]++
				} else {
					changed = true
					account.Gaps["workiq_message_changed_during_traversal"]++
				}
				continue
			}
			seen[header.ID] = header.Version
			identityBytes += len(header.ID) + len(header.Version)
			if len(seen) > 1000000 || identityBytes > 64*1024*1024 {
				result = workIQFailure("workiq_message_count_limit")
				break
			}
			if header.Removed {
				continue
			}
			if header.CreatedAt == nil {
				row.UnknownClock++
				account.Gaps["workiq_clock_unknown"]++
			} else if !header.CreatedAt.Before(start) && !header.CreatedAt.After(end) {
				row.ServerCount++
				account.Gaps["workiq_message_identity_unverified"]++
			}
		}
		if result != nil {
			break
		}
		path = page.NextCursor
		if path != "" {
			path, result = workiq.RelativePath(path, base)
			if result != nil {
				break
			}
		}
	}
	row.Complete = result == nil && row.UnknownClock == 0 && !changed
	report.Chats = append(report.Chats, row)
	account.Counts["messages_in_window"] += row.ServerCount
	return result
}

func probeWorkIQChatStatus(ctx context.Context, options WorkIQOptions, facts []workiq.AccountFact, report *WorkIQReport) error {
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		return err
	}
	binding, err := user.DiscoveryBinding()
	if err != nil {
		return err
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return err
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	items, err := readWorkIQProbeCollection(ctx, session, "/me/chats")
	err = errors.Join(err, session.close())
	state, code := workIQProbeState(err)
	report.Probes = append(report.Probes, WorkIQPathProbe{Family: "chats", PathClass: "chat_list", State: state, Code: code, Scope: "initial_page_only"})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, raw := range items {
		chat, err := workiq.DecodeChat(ctx, raw)
		if err != nil {
			return err
		}
		binding, err := workiq.SelectChatAccount(user, chat, facts, options.Account)
		if err != nil {
			report.Gaps["workiq_chat_account_unmatched"]++
			continue
		}
		if seen[binding.Fact.ID] {
			continue
		}
		seen[binding.Fact.ID] = true
		attempt, err := beginWorkIQProbe(ctx, options, report.Enabled, binding, pinned)
		account := WorkIQAccountReport{Family: "chats", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
		if err := finishWorkIQProbe(ctx, options, report, account, attempt, err); err != nil {
			return err
		}
	}
	return nil
}
