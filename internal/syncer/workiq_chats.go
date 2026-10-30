package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

type workIQSelectedChat struct {
	chat    workiq.Chat
	binding workiq.AccountBinding
	last    time.Time
	capped  int
}

func discoverWorkIQChats(ctx context.Context, options WorkIQOptions, facts []workiq.AccountFact, report *WorkIQReport) ([]workIQSelectedChat, error) {
	user, err := workIQBootstrap(ctx, options)
	if err != nil {
		return nil, err
	}
	discovery, err := user.DiscoveryBinding()
	if err != nil {
		return nil, err
	}
	client, _, err := workIQPinned(ctx, options, discovery)
	if err != nil {
		return nil, err
	}
	session := &workIQSession{options: options, binding: discovery, client: client}
	var out []workIQSelectedChat
	seen := map[string]bool{}
	path, pages, bytes := "/me/chats", 0, 0
	for path != "" && err == nil {
		pages++
		if pages > 10000 {
			err = workIQFailure("workiq_page_limit")
			break
		}
		var page workiq.Page
		page, err = session.fetch(ctx, path)
		if err != nil {
			break
		}
		var collection struct {
			Value *[]json.RawMessage `json:"value"`
		}
		if json.Unmarshal(page.Data, &collection) != nil || collection.Value == nil {
			err = workIQFailure("workiq_protocol")
			break
		}
		if len(*collection.Value) > 0 {
			err = workIQRead(ctx, options.DBPath, func(s *store.Store) error {
				for _, raw := range *collection.Value {
					bytes += len(raw)
					if len(seen) >= 100000 || bytes > 64*1024*1024 {
						return workIQFailure("workiq_discovery_limit")
					}
					chat, err := workiq.DecodeChat(ctx, raw)
					if err != nil {
						return err
					}
					if seen[chat.ID] {
						return workIQFailure("workiq_protocol")
					}
					seen[chat.ID] = true
					report.Counts["chats_discovered"]++
					binding, err := workiq.SelectChatAccount(user, chat, facts, options.Account)
					if err != nil {
						report.Gaps["workiq_chat_account_unmatched"]++
						continue
					}
					last, err := s.WorkIQChatActivity(ctx, binding.Fact, chat.ID)
					if err != nil {
						return err
					}
					out = append(out, workIQSelectedChat{chat: chat, binding: binding, last: last})
				}
				return nil
			})
		}
		if err == nil {
			path = page.NextCursor
			if path != "" {
				path, err = workiq.RelativePath(path, "/me/chats")
			}
		}
	}
	err = errors.Join(err, session.close())
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].last.Equal(out[j].last) {
			return out[i].last.After(out[j].last)
		}
		// Discovery rejects duplicate resource IDs, including conflicting tenants.
		return out[i].chat.ID < out[j].chat.ID
	})
	report.Counts["chats_eligible"] = len(out)
	if len(out) > options.MaxChats {
		report.Gaps["workiq_selection_capped"] += len(out) - options.MaxChats
		excluded := map[string]int{}
		for _, chat := range out[options.MaxChats:] {
			excluded[chat.binding.Fact.ID]++
		}
		out = out[:options.MaxChats]
		for i := range out {
			id := out[i].binding.Fact.ID
			out[i].capped = excluded[id]
			excluded[id] = 0
		}
	}
	return out, nil
}

// workIQChatPageLimit bounds the pages one chat sync fetches per account; a test seam.
var workIQChatPageLimit = 10000

// workIQChatSeenLimit bounds the run-local message IDs one chat sync remembers; a test seam.
var workIQChatSeenLimit = 1000000

func runWorkIQChats(ctx context.Context, options WorkIQOptions) (WorkIQReport, error) {
	report := WorkIQReport{Kind: "sync", Counts: map[string]int{}, Gaps: map[string]int{}}
	if options.MaxChats == 0 {
		options.MaxChats = 200
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
	report.Enabled = enabled
	if !enabled {
		return report, workIQFailure("workiq_disabled")
	}
	if options.Account != "" {
		known := false
		for _, fact := range facts {
			known = known || fact.Family == "chats" && fact.ID == options.Account
		}
		if !known {
			return report, workIQFailure("workiq_chat_account_unmatched")
		}
	}
	chats, err := discoverWorkIQChats(ctx, options, facts, &report)
	if err != nil {
		return failedWorkIQStartupReport(ctx, options, report, facts, "chats", options.Account, err)
	}
	groups := map[string][]workIQSelectedChat{}
	var accounts []string
	for _, chat := range chats {
		id := chat.binding.Fact.ID
		if _, ok := groups[id]; !ok {
			accounts = append(accounts, id)
		}
		groups[id] = append(groups[id], chat)
	}
	sort.Strings(accounts)
	resumable := true
	for _, id := range accounts {
		account, accountErr := acquireWorkIQChatAccount(ctx, options, groups[id])
		resumable = resumable && (accountErr == nil || workIQResumableCodes[workIQOutcomeCode(accountErr)])
		report.Accounts = append(report.Accounts, account)
		for key, n := range account.Counts {
			report.Counts[key] += n
		}
		for key, n := range account.Gaps {
			if key != "workiq_selection_capped" { // already counted across discovery
				report.Gaps[key] += n
			}
		}
		err = errors.Join(err, accountErr)
	}
	report.Resumable = err != nil && resumable
	return report, err
}

func acquireWorkIQChatAccount(ctx context.Context, options WorkIQOptions, chats []workIQSelectedChat) (WorkIQAccountReport, error) {
	binding := chats[0].binding
	report := WorkIQAccountReport{Family: "chats", Account: binding.Fact.ID, Counts: map[string]int{}, Gaps: map[string]int{}}
	for _, selected := range chats {
		if selected.capped > 0 {
			report.Gaps["workiq_selection_capped"] += selected.capped
		}
	}
	started := workIQNow()
	var window store.WorkIQChatWindow
	err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
		if options.Since.IsZero() {
			window, err = s.ResolveWorkIQChatWindow(ctx, binding.Fact.ID, started)
		} else {
			window, err = s.ResolveWorkIQChatWindow(ctx, binding.Fact.ID, started, options.Since)
		}
		return err
	})
	if err != nil {
		report.Status = "failed"
		report.Gaps["workiq_failed"]++
		return report, err
	}
	if err := holdWorkIQScope(ctx, options, store.WorkIQScope{Family: "chats", Account: binding.Fact.ID}); err != nil {
		report.Status = "failed"
		return report, err
	}
	client, pinned, err := workIQPinned(ctx, options, binding)
	if err != nil {
		return recordWorkIQStartupFailure(ctx, options, true, report, err)
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	scope := store.WorkIQScope{Family: "chats", Account: binding.Fact.ID, Qualification: window.Qualification,
		WindowStart: window.Start, WindowEnd: window.End}
	var attempt int64
	err = workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
		attempt, err = s.BeginWorkIQAttempt(writeCtx, scope, started)
		if err != nil {
			return err
		}
		return s.VerifyWorkIQAttempt(writeCtx, attempt, binding, pinned, started)
	})
	if err == nil {
		for _, selected := range chats {
			if selected.last.IsZero() {
				report.Gaps["workiq_clock_unknown"]++
			}
			chatErr := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
				_, err := s.CommitWorkIQConversation(writeCtx, attempt, selected.chat.Raw, workIQNow())
				return err
			})
			if chatErr == nil {
				chatErr = acquireWorkIQChat(ctx, options, session, attempt, scope, selected.chat, &report)
			}
			err = errors.Join(err, chatErr)
			if ctx.Err() != nil {
				break
			}
		}
	}
	err = errors.Join(err, session.close())
	report.Status = "ok"
	report.Diagnostic = workIQDiagnostic(err)
	code := ""
	if err != nil {
		report.Status, code = "failed", workIQOutcomeCode(err)
		gap := code
		if !strings.HasPrefix(gap, "workiq_") {
			gap = "workiq_failed"
		}
		report.Gaps[gap]++
	} else if len(report.Gaps) > 0 {
		report.Status = "ok_with_omissions"
	}
	if attempt != 0 {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), workIQPersistBudget)
		persistErr := workIQWrite(persistCtx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.CompleteWorkIQAttempt(writeCtx, attempt, store.WorkIQSummary{
				Status: report.Status, ErrorCode: code, FinishedAt: workIQNow(), Counts: report.Counts, Gaps: report.Gaps,
				Diagnostic: report.Diagnostic,
			})
		})
		cancel()
		if persistErr != nil {
			report.Status = "failed"
			report.Gaps["workiq_persistence_failed"]++
		}
		err = errors.Join(err, persistErr)
	}
	return report, err
}

func acquireWorkIQChat(ctx context.Context, options WorkIQOptions, session *workIQSession, attempt int64, scope store.WorkIQScope, chat workiq.Chat, report *WorkIQAccountReport) error {
	resource := "/chats/" + url.PathEscape(chat.ID) + "/messages"
	var after int64
	// The run-local seen set counts Graph's cross-page repeats once, within the same bounds as coverage.
	seen, seenBytes := map[string]string{}, 0
	prepare := func() error {
		for {
			var ids []int64
			if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
				ids, err = s.WorkIQPendingChatPages(ctx, attempt, resource, after, 128)
				return err
			}); err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			for _, id := range ids {
				done, limit := false, 128
				for !done {
					var result store.WorkIQChatPageResult
					err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) (err error) {
						result, err = s.PrepareWorkIQChatPageSeen(writeCtx, attempt, id, chat.Raw, limit, seen)
						return err
					})
					if err != nil {
						if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) && limit > 1 {
							limit /= 2
							continue
						}
						return err
					}
					report.Counts["messages_seen"] += result.Seen
					report.Counts["duplicates_skipped"] += result.DuplicatesSkipped
					if seenBytes += result.SeenBytes; len(seen) > workIQChatSeenLimit || seenBytes > 64*1024*1024 {
						return workIQFailure("workiq_message_count_limit")
					}
					report.Counts["messages_in_window"] += result.InWindow
					report.Counts["messages_outside_window"] += result.OutsideWindow
					report.Counts["messages_unknown_clock"] += result.UnknownClock
					report.Counts["messages_added"] += result.Counts.Inserted
					report.Counts["messages_updated"] += result.Counts.Updated
					for code, n := range result.Gaps {
						report.Gaps[code] += n
					}
					done = result.Complete
				}
				after = id
			}
		}
	}
	if err := prepare(); err != nil {
		return err
	}
	for {
		var p store.WorkIQTraversalProgress
		if err := workIQRead(ctx, options.DBPath, func(s *store.Store) (err error) {
			p, err = s.WorkIQTraversalProgressFor(ctx, scope, resource)
			return err
		}); err != nil {
			return err
		}
		if p.Terminal {
			if err := workIQWrite(ctx, options.DBPath, func(writeCtx context.Context, s *store.Store) error {
				return s.AdmitWorkIQFrontier(writeCtx, attempt, resource, p.Revision)
			}); err != nil {
				return err
			}
			report.Counts["chats_completed"]++
			return nil
		}
		path := resource
		if p.Revision > p.AdmittedRevision {
			path = p.PendingCursor
		} else if p.Cursor != "" {
			path = p.Cursor
		}
		path, err := workiq.RelativePath(path, resource)
		if err != nil {
			return err
		}
		// Only pages fetched in this run count: pages staged by earlier runs are prepared again for
		// free, so a chat larger than the cap continues on the next run.
		if report.Counts["pages"] >= workIQChatPageLimit {
			return workIQFailure("workiq_page_limit")
		}
		page, err := session.fetch(ctx, path)
		if err != nil {
			return err
		}
		report.Counts["pages"]++
		if err := workIQWrite(workIQPageWrite(ctx, len(page.JSON)), options.DBPath, func(writeCtx context.Context, s *store.Store) error {
			return s.StageWorkIQPage(writeCtx, store.WorkIQPageBatch{AttemptID: attempt, Resource: resource,
				ExpectedRevision: p.Revision, Page: page, ObservedAt: workIQNow()})
		}); err != nil {
			return err
		}
		if err := prepare(); err != nil {
			return err
		}
	}
}
