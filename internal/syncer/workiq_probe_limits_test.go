package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQProbeLimitsAndForeignFolderEdgesRemainFailed(t *testing.T) {
	for _, mode := range []string{"folder-limit", "message-limit", "wrong-child-parent", "wrong-folder-cursor"} {
		t.Run(mode, func(t *testing.T) {
			db, account := workIQContentFixture(t)
			workIQAcquisitionStub(t)
			family := "mail"
			if mode == "message-limit" {
				account, family = "", "chats"
				seedWorkIQChatOwner(t, db)
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			}
			t.Setenv("WORKIQ_PROBE_CASE", mode)
			_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: family, Account: account, Kind: "coverage", MaxChats: 1})
			if err == nil {
				t.Fatal("foreign or oversized source became positive counts", mode)
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
}

func TestWorkIQProbeSharedPageBudgetIncludesEarlierCompletedResources(t *testing.T) {
	for _, family := range []string{"mail", "chats"} {
		t.Run(family, func(t *testing.T) {
			db, id := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			options := WorkIQOptions{DBPath: db, Kind: "coverage", Source: family}
			if family == "chats" {
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			}
			ctx := context.Background()
			user, err := workIQBootstrap(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := user.DiscoveryBinding()
			if err != nil {
				t.Fatal(err)
			}
			client, _, err := workIQPinned(ctx, options, binding)
			if err != nil {
				t.Fatal(err)
			}
			session := &workIQSession{options: options, binding: binding, client: client}
			defer func() {
				if err := session.close(); err != nil {
					t.Fatal(err)
				}
			}()
			report := WorkIQReport{Counts: map[string]int{}, Gaps: map[string]int{}}
			account := WorkIQAccountReport{Counts: map[string]int{"pages": 9999}, Gaps: map[string]int{}}
			if family == "mail" {
				err = measureWorkIQMail(ctx, options, session, id, &account, &report)
			} else {
				owner := seedWorkIQChatOwner(t, db)
				binding.Fact = workiq.AccountFact{ID: owner.TenantID + "/" + owner.UserID, Family: "chats",
					TenantID: owner.TenantID, UserID: owner.UserID}
				err = measureWorkIQChat(ctx, options, session, workIQSelectedChat{chat: workiq.Chat{ID: "chat"}, binding: binding},
					time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Now(), &account, &report)
			}
			var coded *errs.Coded
			if !errors.As(err, &coded) || coded.Code != "workiq_page_limit" {
				t.Fatal("counts reset its account-wide page work budget", err)
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
}

func TestWorkIQProbeCanceledWorkStopsRemainingChatsAndFamilies(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := workIQRead
	defer func() { workIQRead = old }()
	calls := 0
	workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
		err := old(ctx, path, op)
		calls++
		if calls == 4 {
			cancel()
		}
		return err
	}
	report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage"})
	if !errors.Is(err, context.Canceled) || len(report.Chats) != 1 || report.Chats[0].Complete {
		t.Fatal("canceled counts continued the next selected chat or claimed full traversal", err)
	}
	assertWorkIQProbeHasNoData(t, db)
}

func TestWorkIQProbeAttemptCreationCannotBypassActualKnownOwner(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	locks := &workIQRunLocks{}
	defer locks.release()
	_, err := beginWorkIQProbe(context.Background(), WorkIQOptions{DBPath: db, Kind: "coverage", runLocks: locks}, true,
		workiq.AccountBinding{Fact: workiq.AccountFact{ID: "unarchived", Family: "mail"}}, workiq.User{})
	if err == nil || count(t, db, `select count(*) from workiq_attempts`) != 0 {
		t.Fatal("unknown owner acquired a probe attempt")
	}
	if _, err := beginWorkIQProbe(context.Background(), WorkIQOptions{DBPath: db, Kind: "coverage"}, true,
		workiq.AccountBinding{Fact: workiq.AccountFact{ID: "unarchived", Family: "mail"}}, workiq.User{}); err == nil {
		t.Fatal("a probe attempt began without owning its scope")
	}
}

func TestWorkIQStatusInvalidAndUnmatchedChatFactsStayExplicit(t *testing.T) {
	for _, mode := range []string{"status-bad-chat", "status-mixed-chats", "invalid-address"} {
		t.Run(mode, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			if mode == "invalid-address" {
				t.Setenv("WORKIQ_ACQUISITION_ME", mode)
			} else {
				t.Setenv("WORKIQ_PROBE_CASE", mode)
			}
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "status"})
			if mode == "status-mixed-chats" {
				if err != nil || len(report.Accounts) != 1 || report.Gaps["workiq_chat_account_unmatched"] != 1 {
					t.Fatal("status invented a tenant owner or repeated a matched account", err)
				}
			} else if err == nil {
				t.Fatal("invalid source identity became positive status")
			}
			assertWorkIQProbeHasNoData(t, db)
		})
	}
}
