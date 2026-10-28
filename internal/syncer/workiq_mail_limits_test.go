package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func workIQStagedMailRun(t *testing.T) (*store.Store, *workIQMailRun) {
	t.Helper()
	db, account := workIQAcquisitionArchive(t)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user := workiq.User{ID: "synthetic-user", Mail: "test.user@example.invalid", UPN: "test.user@example.invalid"}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := workiq.SelectMailAccount(user, facts, account)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	attempt, err := s.BeginWorkIQAttempt(ctx, store.WorkIQScope{Family: "mail", Account: account}, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.VerifyWorkIQAttempt(ctx, attempt, binding, user, at); err != nil {
		t.Fatal(err)
	}
	return s, &workIQMailRun{options: WorkIQOptions{DBPath: db}, attempt: attempt, account: account,
		prepared: map[string]int64{}, visited: map[string]bool{},
		report: &WorkIQAccountReport{Counts: map[string]int{}, Gaps: map[string]int{}}}
}

func stageWorkIQRunRoot(t *testing.T, s *store.Store, run *workIQMailRun, next string) {
	t.Helper()
	resource := "/me/mailFolders?includeHiddenFolders=true"
	data := map[string]any{"value": []any{}}
	if next != "" {
		data["@odata.nextLink"] = next
	}
	raw, err := json.Marshal(map[string]any{"structuredContent": map[string]any{"results": []any{map[string]any{"statusCode": 200, "data": data}}}})
	if err != nil {
		t.Fatal(err)
	}
	page, err := workiq.DecodePage(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.WorkIQProgressFor(context.Background(), store.WorkIQScope{Family: "mail", Account: run.account}, resource)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StageWorkIQPage(context.Background(), store.WorkIQPageBatch{AttemptID: run.attempt, Resource: resource,
		ExpectedRevision: p.Revision, Page: page, ObservedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkIQMailStoredPageBoundsAndInvalidCursorFailBeforeFetch(t *testing.T) {
	ctx := context.Background()
	root := "/me/mailFolders?includeHiddenFolders=true"
	t.Run("page-bound", func(t *testing.T) {
		s, run := workIQStagedMailRun(t)
		stageWorkIQRunRoot(t, s, run, root+"&$skiptoken=second")
		stageWorkIQRunRoot(t, s, run, "")
		run.pages = 9999
		if err := run.prepare(ctx, root); err == nil || run.report.Gaps["workiq_page_limit"] != 1 {
			t.Fatal("resumed original pages exceeded the actual account page bound")
		}
	})
	t.Run("invalid-pending", func(t *testing.T) {
		s, run := workIQStagedMailRun(t)
		stageWorkIQRunRoot(t, s, run, root+"&$skiptoken=second")
		if _, err := openRaw(t, run.options.DBPath).Exec(`update workiq_frontiers set pending_cursor='https://other.invalid/private'`); err != nil {
			t.Fatal(err)
		}
		if err := run.fetchResource(ctx, root); err == nil {
			t.Fatal("corrupt retained pending cursor reached a client")
		}
	})
	t.Run("already-visited", func(t *testing.T) {
		_, run := workIQStagedMailRun(t)
		run.visited[root] = true
		if err := run.fetchResource(ctx, root); err != nil {
			t.Fatal("completed run resource was fetched again", err)
		}
	})
}

func TestWorkIQMailFolderInventoryBoundUsesActualStoredRows(t *testing.T) {
	_, run := workIQStagedMailRun(t)
	db := openRaw(t, run.options.DBPath)
	if _, err := db.Exec(`with recursive n(x) as (values(1) union all select x+1 from n where x<100001)
insert into mail_folders(account,folder_key,name) select ?,1000000+x,printf('limit-%06d',x) from n`, run.account); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`with recursive n(x) as (values(1) union all select x+1 from n where x<100001)
insert into workiq_folders(account_id,graph_id,folder_key,inventory_attempt,root_listed,child_count)
select ?,printf('limit-%06d',x),1000000+x,?,1,0 from n`, run.account, run.attempt); err != nil {
		t.Fatal(err)
	}
	if rows, err := run.folders(context.Background(), ""); err == nil || rows != nil {
		t.Fatal("source inventory exceeded its actual folder bound")
	}
}

func TestWorkIQMailQueueBoundDoesNotAllocateOrGuessDuplicateOwnership(t *testing.T) {
	queue := make([]workiq.Folder, 100000)
	if _, err := appendWorkIQMailQueue(queue, []workiq.Folder{{ID: "one-more"}}, map[string]bool{}); err == nil {
		t.Fatal("child discovery exceeded the shared account inventory bound")
	}
	if _, err := appendWorkIQMailQueue(append(queue, workiq.Folder{}), nil, map[string]bool{}); err == nil {
		t.Fatal("already invalid inventory became admissible")
	}
}

func TestWorkIQMailCleanupAndThrottleCannotRecordPositiveCompletion(t *testing.T) {
	for _, mode := range []string{"cleanup", "throttle", "unknown-clock"} {
		t.Run(mode, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			originalClose, originalNow := workIQClose, workIQNow
			t.Cleanup(func() { workIQClose, workIQNow = originalClose, originalNow })
			failure := errors.New("synthetic cleanup failure")
			switch mode {
			case "cleanup":
				t.Setenv("WORKIQ_ACQUISITION_MODE", "permanent")
				calls := 0
				workIQClose = func(client *workiq.Client) error {
					err := originalClose(client)
					calls++
					if calls == 2 {
						return errors.Join(err, failure)
					}
					return err
				}
			case "throttle":
				t.Setenv("WORKIQ_ACQUISITION_MODE", "throttled")
			case "unknown-clock":
				workIQNow = func() time.Time { return time.Time{} }
			}
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
			if err == nil || count(t, db, `select count(*) from mail_messages`) != 0 {
				t.Fatal("cleanup, unknown header deadline, or unknown attempt clock admitted source data")
			}
			if mode == "cleanup" && !errors.Is(err, failure) {
				t.Fatal("cleanup failure lost its cause")
			}
			if mode == "throttle" {
				var coded *errs.Coded
				if !errors.As(err, &coded) || coded.Code != "workiq_retry_later" {
					t.Fatal("unobservable Retry-After was guessed or retried early")
				}
			}
			for _, account := range report.Accounts {
				if account.Status != "failed" {
					t.Fatal("failed source reported a positive outcome")
				}
			}
		})
	}
}
