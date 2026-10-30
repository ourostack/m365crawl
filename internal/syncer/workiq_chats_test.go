package syncer

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQChatAcquisitionUsesActualPagedMCPAndConservativeProducer(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	ctx := context.Background()
	account := seedWorkIQChatOwner(t, db)
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "chats", Since: since, MaxChats: 1,
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
	if err != nil {
		t.Fatal("actual synthetic chat acquisition failed", err)
	}
	if len(report.Accounts) != 1 || report.Accounts[0].Family != "chats" || report.Accounts[0].Status != "ok_with_omissions" ||
		report.Counts["messages_seen"] != 3 || report.Counts["messages_in_window"] != 2 ||
		report.Gaps["workiq_message_identity_unverified"] != 2 || report.Gaps["workiq_selection_capped"] != 1 ||
		report.Counts["messages_added"] != 0 {
		t.Fatalf("chat outcome did not reflect conservative full paging and cap: %#v", report)
	}
	raw, err := os.ReadFile(filepath.Clean(transcript))
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if !strings.Contains(log, "/me/chats?$skiptoken=second") || !strings.Contains(log, "/chats/chat/messages?$skiptoken=second") ||
		strings.Contains(log, "$select") || strings.Contains(log, "$expand") || strings.Contains(log, "/chats/server-only/messages") {
		t.Fatal("unqualified selection skipped paging, guessed server activity or exposed unsupported query options")
	}
	read, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	_, rows, _, err := read.SQL(ctx, `select count(*) from messages`, 1)
	if err != nil {
		t.Fatal(err)
	}
	progress, err := read.WorkIQTraversalProgressFor(ctx, store.WorkIQScope{Family: "chats", Account: account.TenantID + "/" + account.UserID,
		Qualification: store.WorkIQAbsoluteChatQualification, WindowStart: since}, "/chats/chat/messages")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0][0] != int64(0) || progress.ID == 0 || progress.Revision != progress.AdmittedRevision || progress.Revision != 2 {
		t.Fatal("unverified messages were published or fully prepared pages were left unadmitted")
	}
}

func seedWorkIQChatOwner(t *testing.T, db string) teamsdesktop.Account {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	account := teamsdesktop.Account{TenantID: "synthetic-tenant", UserID: "synthetic-user"}
	if err := s.ApplyAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyConversations(ctx, []teamsdesktop.Conversation{
		{TenantID: account.TenantID, UserID: account.UserID, ID: "chat", Kind: "group", LastMessageAt: time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return account
}

func TestWorkIQAllSourceFailureDoesNotBlockMatchedChatAcquisition(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	seedWorkIQChatOwner(t, db)
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db,
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
	if err == nil || len(report.Accounts) != 1 || report.Accounts[0].Family != "chats" || report.Counts["chats_completed"] != 2 {
		t.Fatal("default all source did not preserve matched chat work after mail's explicit-account refusal", err)
	}
}

func TestWorkIQChatFailureKeepsItsCursorAndDoesNotBlockTheNextSelectedChat(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	owner := seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "fail-second")
	ctx := context.Background()
	options := WorkIQOptions{DBPath: db, Source: "chats", Invocation: workiq.Invocation{ExplicitTeamsRoot: true}}
	first, err := RunWorkIQ(ctx, options)
	if err == nil || first.Counts["chats_completed"] != 1 || len(first.Accounts) != 1 || first.Accounts[0].Status != "failed" {
		t.Fatal("one failed selected chat stopped independent work or was called successful", err)
	}
	read, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	window, err := read.ResolveWorkIQChatWindow(ctx, owner.TenantID+"/"+owner.UserID, time.Now())
	if err != nil || !window.Resumed {
		t.Fatal("failed chat did not retain the original fixed window", err)
	}
	progress, err := read.WorkIQTraversalProgressFor(ctx, store.WorkIQScope{Family: "chats", Account: owner.TenantID + "/" + owner.UserID,
		Qualification: window.Qualification, WindowStart: window.Start}, "/chats/chat/messages")
	if err != nil || progress.Revision != 1 || progress.AdmittedRevision != 0 || !strings.Contains(progress.PendingCursor, "$skiptoken=second") {
		t.Fatal("failed fetch advanced the good cursor or discarded the staged page", err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	next, err := RunWorkIQ(ctx, options)
	if err != nil || next.Counts["chats_completed"] != 2 || next.Counts["messages_seen"] != 3 {
		t.Fatal("retry did not prepare retained work then resume the pending cursor", err)
	}
	raw, err := os.ReadFile(filepath.Clean(transcript))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"path":"/chats/chat/messages"`) != 1 {
		t.Fatal("retry fetched an already staged first page instead of resuming it")
	}
}

func TestWorkIQChatDiscoveryRejectsMalformedPagesAndDisclosesUnmatchedTenants(t *testing.T) {
	for _, mode := range []string{"malformed", "bad-entity", "unmatched", "missing-tenant", "empty"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", mode)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats"})
			switch mode {
			case "malformed", "bad-entity":
				// The failure is recorded against the only chat account, with no content work.
				if err == nil || len(report.Accounts) != 1 || report.Accounts[0].Status != "failed" || report.Counts["chats_completed"] != 0 ||
					count(t, db, `select count(*) from workiq_attempts where status='open'`) != 0 {
					t.Fatal("malformed discovery became selected content work or left no closed attempt", err, report.Accounts)
				}
			case "unmatched", "missing-tenant":
				if err != nil || report.Gaps["workiq_chat_account_unmatched"] != 1 || report.Counts["chats_completed"] != 1 {
					t.Fatal("unmatched actual tenant was guessed or blocked a matched chat", err)
				}
			case "empty":
				if err != nil || len(report.Accounts) != 0 || report.Counts["chats_discovered"] != 0 {
					t.Fatal("empty discovery invented an attempted content account", err)
				}
			}
		})
	}
}

func TestWorkIQExplicitSourceAndAccountConflictsRefuseBeforeSpawn(t *testing.T) {
	for _, options := range []WorkIQOptions{
		{}, {DBPath: "unused", Source: "unknown"}, {DBPath: "unused", MaxChats: -1}, {DBPath: "unused", MaxChats: 100001},
		{DBPath: "unused", Source: "mail", Account: "tenant/user"},
		{DBPath: "unused", Source: "chats", Account: "outlook/known"},
		{DBPath: "unused", Since: time.Now().Add(24 * time.Hour)},
	} {
		if _, err := RunWorkIQ(context.Background(), options); err == nil {
			t.Fatal("invalid source/account/bounds reached acquisition")
		}
	}
}

func TestWorkIQChatAcquisitionReadWriteAndClientFailuresStayNegative(t *testing.T) {
	for _, role := range []string{"read", "write", "open", "close"} {
		limit := 14
		if role == "open" || role == "close" {
			limit = 3
		}
		for fail := 1; fail <= limit; fail++ {
			t.Run(role+time.Duration(fail).String(), func(t *testing.T) {
				db, _ := workIQAcquisitionArchive(t)
				workIQAcquisitionStub(t)
				seedWorkIQChatOwner(t, db)
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
				releaseFixture := workIQBoundaryFixture(t, db)
				defer releaseFixture()
				oldRead, oldWrite, oldOpen, oldClose := workIQRead, workIQWrite, workIQOpen, workIQClose
				defer func() { workIQRead, workIQWrite, workIQOpen, workIQClose = oldRead, oldWrite, oldOpen, oldClose }()
				n := 0
				broken := errors.New("synthetic chat operation failure")
				switch role {
				case "read":
					workIQRead = func(ctx context.Context, path string, operation func(*store.Store) error) error {
						n++
						if n == fail {
							return broken
						}
						return oldRead(ctx, path, operation)
					}
				case "write":
					workIQWrite = func(ctx context.Context, path string, operation func(context.Context, *store.Store) error) error {
						n++
						if n == fail {
							return &errs.Coded{Code: "locked", Message: "synthetic writer failure"}
						}
						return oldWrite(ctx, path, operation)
					}
				case "open":
					workIQOpen = func(ctx context.Context, invocation workiq.Invocation, account string) (*workiq.Client, error) {
						n++
						if n == fail {
							return nil, broken
						}
						return oldOpen(ctx, invocation, account)
					}
				case "close":
					workIQClose = func(client *workiq.Client) error {
						n++
						err := oldClose(client)
						if n == fail {
							return errors.Join(err, broken)
						}
						return err
					}
				}

				report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats"})
				if n >= fail && err == nil {
					t.Fatal("actual intercepted failure was called a positive acquisition")
				}
				if err != nil {
					for _, account := range report.Accounts {
						if account.Status != "failed" {
							t.Fatal("a failed account reported a positive or empty state")
						}
					}
				}
				unlock, err := store.AcquireWriter(context.Background(), db, store.WriterOrdinarySync)
				if err != nil {
					t.Fatal("failure leaked the archive writer", err)
				}
				unlock()
			})
		}
	}
}

func TestWorkIQChatDiscoveryActualBoundsAndRefusals(t *testing.T) {
	for _, mode := range []string{"page-limit", "count-limit", "byte-limit", "discovery-fail", "invalid-address", "activity-corrupt", "disabled", "tie"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", mode)
			switch mode {
			case "invalid-address":
				t.Setenv("WORKIQ_ACQUISITION_ME", mode)
			case "activity-corrupt":
				mutateWorkIQChatFixture(t, db, `update conversations set last_message_at='broken'`)
			case "disabled":
				s, err := store.Open(context.Background(), db)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.SetWorkIQEnabled(context.Background(), false); err != nil {
					t.Fatal(err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats"})
			if mode == "tie" {
				if err != nil || report.Counts["chats_completed"] != 3 {
					t.Fatal("unknown-activity ties omitted eligible empty-window chats", err)
				}
				return
			}
			// The refusal is recorded against the only chat account, never as content work. A disabled
			// source records nothing, and WorkIQ's own unusable address belongs to no account.
			unowned := mode == "disabled" || mode == "invalid-address"
			if err == nil || report.Counts["chats_completed"] != 0 || count(t, db, `select count(*) from workiq_attempts where status='open'`) != 0 ||
				!unowned && (len(report.Accounts) != 1 || report.Accounts[0].Status != "failed") ||
				unowned && len(report.Accounts) != 0 {
				t.Fatal("discovery refusal/cap produced positive content work or no closed attempt", report.Accounts)
			}
			if mode == "count-limit" && report.Counts["chats_discovered"] != 100000 {
				t.Fatal("actual discovered-ID bound was not exercised")
			}
		})
	}
}

func mutateWorkIQChatFixture(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, execErr := db.Exec(statement)
	if err := errors.Join(execErr, db.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkIQChatShortWriteDeadlineAndCancellationControls(t *testing.T) {
	for _, mode := range []string{"shrink", "deadline", "begin-refused", "verify-schema", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			old := workIQWrite
			t.Cleanup(func() { workIQWrite = old })
			writes := 0
			workIQWrite = func(ctx context.Context, path string, operation func(context.Context, *store.Store) error) error {
				writes++
				if writes == 1 && mode == "begin-refused" {
					return old(ctx, path, func(writeCtx context.Context, s *store.Store) error {
						if err := s.SetWorkIQEnabled(writeCtx, false); err != nil {
							return err
						}
						return operation(writeCtx, s)
					})
				}
				if writes == 1 && mode == "verify-schema" {
					mutateWorkIQChatFixture(t, path, `alter table workiq_attempts rename column verified_at to unavailable_verification`)
				}
				if writes == 2 && mode == "cancel" {
					cancel()
					return ctx.Err()
				}
				if writes == 4 && mode == "shrink" || writes >= 4 && writes <= 11 && mode == "deadline" {
					return context.DeadlineExceeded
				}
				return old(ctx, path, operation)
			}
			report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "chats"})
			if mode == "shrink" {
				if err != nil || report.Counts["chats_completed"] != 2 {
					t.Fatal("short-write deadline could not reduce bounded preparation", err)
				}
			} else if err == nil || len(report.Accounts) != 1 || report.Accounts[0].Status != "failed" {
				t.Fatal("deadline/cancellation/owner-verification refusal was called successful", err)
			}
		})
	}
}

func TestWorkIQChatAdmittedCursorAndCorruptPendingScope(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "admitted", true: "invalid-pending"}[corrupt], func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			mode := "delta"
			if corrupt {
				mode = "fail-second"
			}
			t.Setenv("WORKIQ_ACQUISITION_CHATS", mode)
			options := WorkIQOptions{DBPath: db, Source: "chats", Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
			_, err := RunWorkIQ(context.Background(), options)
			if (err != nil) != corrupt {
				t.Fatal("cursor fixture did not reach the actual intended state", err)
			}
			if corrupt {
				mutateWorkIQChatFixture(t, db, `update workiq_frontiers set pending_cursor='/chats/foreign/messages' where resource='/chats/chat/messages'`)
			}
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			report, err := RunWorkIQ(context.Background(), options)
			if corrupt {
				if err == nil || report.Accounts[0].Status != "failed" {
					t.Fatal("foreign pending cursor was followed")
				}
			} else if err != nil || report.Counts["chats_completed"] != 2 || report.Counts["messages_seen"] != 0 {
				t.Fatal("admitted cursor was not resumed exactly", err)
			}
		})
	}
}

func TestWorkIQChatAccountPageBudgetDoesNotAdmitTheOverLimitPage(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	owner := seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	ctx := context.Background()
	user := workiq.User{ID: owner.UserID, Mail: "test.user@example.invalid"}
	chat, err := workiq.DecodeChat(ctx, []byte(`{"id":"chat","tenantId":"synthetic-tenant","chatType":"group"}`))
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workiq.SelectChatAccount(user, chat, facts, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	scope := store.WorkIQScope{Family: "chats", Account: binding.Fact.ID, WindowStart: now.Add(-365 * 24 * time.Hour), WindowEnd: now}
	attempt, err := s.BeginWorkIQAttempt(ctx, scope, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, user, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	options := WorkIQOptions{DBPath: db, Source: "chats"}
	client, _, err := workIQPinned(ctx, options, binding)
	if err != nil {
		t.Fatal(err)
	}
	session := &workIQSession{options: options, binding: binding, client: client}
	// This account has spent its fetched-page budget on preceding selected resources, so the next
	// page is neither fetched, staged, prepared nor admitted.
	report := WorkIQAccountReport{Counts: map[string]int{"pages": 10000}, Gaps: map[string]int{}}
	err = acquireWorkIQChat(ctx, options, session, attempt, scope, chat, &report)
	closeErr := session.close()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_page_limit" || closeErr != nil {
		t.Fatal("account-wide page budget did not stop the next actual stored page", err, closeErr)
	}
	read, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	progress, err := read.WorkIQTraversalProgressFor(ctx, scope, "/chats/chat/messages")
	if err != nil || progress.Revision != 0 || progress.AdmittedRevision != 0 {
		t.Fatal("over-limit page was fetched or admitted", err)
	}
}

func TestWorkIQGlobalAccountSelectsOnlyItsArchivedFamily(t *testing.T) {
	for _, family := range []string{"mail", "chats"} {
		t.Run(family, func(t *testing.T) {
			db, mail := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			owner := seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			account := mail
			if family == "chats" {
				account = owner.TenantID + "/" + owner.UserID
			}
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Account: account})
			if err != nil || len(report.Accounts) != 1 || report.Accounts[0].Family != family || report.Accounts[0].Account != account {
				t.Fatal("global archive-account selection crossed source families", err)
			}
		})
	}
}

func TestWorkIQUnknownChatSelectorRefusesWithoutInventedAccountWork(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Account: "unarchived/user"})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_chat_account_unmatched" || len(report.Accounts) != 0 {
		t.Fatal("unknown explicit account became a positive empty sync instead of a named refusal", err)
	}
}

func TestWorkIQMailRecheckHelpersStopOnReadWriteCancelAndBudgetBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report := WorkIQReport{Counts: map[string]int{}, Gaps: map[string]int{}}
	if _, err := retryWorkIQMailCensus(ctx, WorkIQOptions{}, report); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled census restarted")
	}
	if _, err := retryWorkIQMailCensus(context.Background(), WorkIQOptions{censusPasses: 2}, report); err == nil {
		t.Fatal("census restart exceeded its command budget")
	}
	for _, role := range []string{"read", "write"} {
		t.Run(role, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			ctx := context.Background()
			if _, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "mail", Account: account}); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			facts, err := s.WorkIQAccounts(ctx)
			if err != nil {
				t.Fatal(err)
			}
			user := workiq.User{ID: "synthetic-user", Mail: "test.user@example.invalid"}
			binding, err := workiq.SelectMailAccount(user, facts, account)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			attempt, err := s.BeginWorkIQAttempt(ctx, store.WorkIQScope{Family: "mail", Account: account}, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, user, now); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			oldRead, oldWrite := workIQRead, workIQWrite
			t.Cleanup(func() { workIQRead, workIQWrite = oldRead, oldWrite })
			broken := errors.New("synthetic census boundary failure")
			if role == "read" {
				workIQRead = func(context.Context, string, func(*store.Store) error) error { return broken }
			} else {
				workIQWrite = func(context.Context, string, func(context.Context, *store.Store) error) error { return broken }
			}
			if err := resetWorkIQMailCensus(ctx, WorkIQOptions{DBPath: db}, attempt); !errors.Is(err, broken) {
				t.Fatal("failed actual baseline reset was called successful", err)
			}
		})
	}
}
