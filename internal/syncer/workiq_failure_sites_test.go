package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func noWorkIQWait(t *testing.T) {
	t.Helper()
	original := workIQWait
	t.Cleanup(func() { workIQWait = original })
	workIQWait = func(context.Context, time.Duration) error { return nil }
}

func workIQCode(err error) string {
	var coded *errs.Coded
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

// Live: Graph's /me/chats listing repeats chats on later pages, mostly unchanged and once moved by new
// activity. Discovery keeps one entry per chat and never fails the run for it.
func TestWorkIQChatDiscoveryKeepsOneEntryPerRepeatedChat(t *testing.T) {
	for _, tc := range []struct{ mode, gap string }{
		{"duplicate", ""}, {"repeat-newer", ""}, {"repeat-older", ""}, {"repeat-conflict", "workiq_chat_identity_conflict"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", tc.mode)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage"})
			if err != nil || report.Counts["chats_repeated"] != 1 || tc.gap != "" && report.Gaps[tc.gap] != 1 ||
				tc.gap == "" && report.Gaps["workiq_chat_identity_conflict"] != 0 {
				t.Fatalf("repeated chat: %v %+v %+v", err, report.Counts, report.Gaps)
			}
		})
	}
}

// Live: a WorkIQ child lost during the account-pinned identity check was not reopened and left no
// result. It is reopened like a lost page; when it keeps failing the run stops resumably, with its
// attempt and diagnostic recorded.
func TestWorkIQPinnedIdentityCheckReopensALostSession(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	noWorkIQWait(t)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	t.Setenv("WORKIQ_ACQUISITION_ME_DIE", filepath.Join(t.TempDir(), "died"))
	options := WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal("a lost identity check was not reopened", err)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME_DIE", "always")
	report, err := RunWorkIQ(context.Background(), options)
	if workIQCode(err) != "workiq_retry_later" || !report.Resumable || len(report.Accounts) != 1 || report.Accounts[0].Diagnostic == nil ||
		report.Accounts[0].Diagnostic.Exit != "exit status 3" || report.Accounts[0].Diagnostic.Stderr != "fatal: connection lost for [redacted]" {
		t.Fatalf("lost identity checks: %v %+v", err, report)
	}
	if count(t, db, `select count(*) from workiq_attempts where error_code='workiq_retry_later' and finished_at is not null`) != 1 ||
		count(t, db, `select count(*) from meta where key like 'workiq_diagnostic:%'`) != 1 {
		t.Fatal("the stopped identity check left no closed attempt or diagnostic")
	}
}

// Live: chat coverage ended workiq_failed after 833 pages with nothing kept. An unexpected tool
// error mid-traversal is retried like a server error and then stops resumably; a Graph 4xx stays a
// permanent failure. Both keep a safe descriptor (status, Graph error code, allowlisted text).
func TestWorkIQServerFailuresMidTraversalKeepADescriptor(t *testing.T) {
	for _, shape := range []string{"tool-error", "graph-404"} {
		t.Run(shape, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			noWorkIQWait(t)
			t.Setenv("WORKIQ_ACQUISITION_FAIL_FROM", "12")
			t.Setenv("WORKIQ_ACQUISITION_FAIL_COUNTER", filepath.Join(t.TempDir(), "count"))
			t.Setenv("WORKIQ_ACQUISITION_FAIL_SHAPE", shape)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
			want, resumable, server := "workiq_retry_later", true, "tool error: Unhandled server failure for [redacted]"
			if shape == "graph-404" {
				want, resumable, server = "workiq_failed", false, "status 404; code itemNotFound; The [redacted] was not found"
			}
			if workIQCode(err) != want || report.Resumable != resumable || len(report.Accounts) != 1 || report.Accounts[0].Diagnostic == nil ||
				report.Accounts[0].Diagnostic.Server != server {
				t.Fatalf("%s: %v resumable %v %+v", shape, err, report.Resumable, report.Accounts)
			}
			if count(t, db, `select count(*) from meta where key like 'workiq_diagnostic:%' and value like '%Quarterly%'`) != 0 ||
				count(t, db, `select count(*) from meta where key like 'workiq_diagnostic:%'`) != 1 {
				t.Fatal("the descriptor was not kept, or kept content")
			}
		})
	}
}

// Every failure site records a closed attempt, a result entry and a diagnostic: a server failure is
// injected from each fetch of a successful run onwards, for mail sync, chat sync and chat coverage.
func TestWorkIQEveryFetchFailureRecordsItsAttemptAndResult(t *testing.T) {
	noWorkIQWait(t)
	for _, tc := range []struct {
		name    string
		options func(db, account string) WorkIQOptions
	}{
		{"mail-sync", func(db, account string) WorkIQOptions {
			return WorkIQOptions{DBPath: db, Source: "mail", Account: account}
		}},
		{"chats-sync", func(db, _ string) WorkIQOptions { return WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1} }},
		{"chats-coverage", func(db, _ string) WorkIQOptions {
			return WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage", MaxChats: 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetches := 0
			for from := 1; ; from++ {
				db, account := workIQAcquisitionArchive(t)
				workIQAcquisitionStub(t)
				seedWorkIQChatOwner(t, db)
				t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
				counter := filepath.Join(t.TempDir(), "count")
				t.Setenv("WORKIQ_ACQUISITION_FAIL_COUNTER", counter)
				t.Setenv("WORKIQ_ACQUISITION_FAIL_FROM", "1000000")
				if from == 1 {
					if _, err := RunWorkIQ(context.Background(), tc.options(db, account)); err != nil {
						t.Fatal(err)
					}
					info, err := os.Stat(counter)
					if err != nil {
						t.Fatal(err)
					}
					fetches = int(info.Size())
					db, account = workIQAcquisitionArchive(t)
					seedWorkIQChatOwner(t, db)
					counter = filepath.Join(t.TempDir(), "count")
					t.Setenv("WORKIQ_ACQUISITION_FAIL_COUNTER", counter)
				}
				if from > fetches {
					return
				}
				t.Setenv("WORKIQ_ACQUISITION_FAIL_FROM", strconv.Itoa(from))
				report, err := RunWorkIQ(context.Background(), tc.options(db, account))
				if err == nil || len(report.Accounts) == 0 ||
					count(t, db, `select count(*) from workiq_attempts where status='open'`) != 0 ||
					count(t, db, `select count(*) from workiq_attempts where error_code<>'' and finished_at is not null`) == 0 ||
					count(t, db, `select count(*) from meta where key like 'workiq_diagnostic:%'`) == 0 {
					t.Fatalf("failure from fetch %d of %d left no result, closed attempt or diagnostic: %v %+v", from, fetches, err, report.Accounts)
				}
				if tc.name == "chats-coverage" {
					continue
				}
				// Every later fetch keeps failing, so reruns stop at the identity check: by the third
				// run the same stop repeats and the result no longer invites another rerun.
				for range 2 {
					report, err = RunWorkIQ(context.Background(), tc.options(db, account))
				}
				if workIQCode(err) != "workiq_stuck" || report.Resumable {
					t.Fatalf("a failure repeating from fetch %d of %d still invited a rerun: %v resumable %v", from, fetches, err, report.Resumable)
				}
			}
		})
	}
}

// The identity check stops at once when cleanup fails after a refused /me, or when its backoff
// before reopening a lost session is canceled.
func TestWorkIQIdentityCheckStopsOnCleanupFailureOrCanceledBackoff(t *testing.T) {
	workIQAcquisitionStub(t)
	ctx := context.Background()
	t.Setenv("WORKIQ_ACQUISITION_ME", "fail")
	broken := errors.New("synthetic close failure")
	original := workIQClose
	t.Cleanup(func() { workIQClose = original })
	workIQClose = func(client *workiq.Client) error { return errors.Join(original(client), broken) }
	if _, _, err := workIQOpenUser(ctx, workiq.Invocation{}, ""); !errors.Is(err, broken) || workIQCode(err) != "workiq_failed" {
		t.Fatal("a cleanup failure after a refused identity check was retried or lost", err)
	}
	workIQClose = original
	t.Setenv("WORKIQ_ACQUISITION_ME", "")
	t.Setenv("WORKIQ_ACQUISITION_ME_DIE", "always")
	originalWait := workIQWait
	t.Cleanup(func() { workIQWait = originalWait })
	workIQWait = func(context.Context, time.Duration) error { return context.Canceled }
	if _, _, err := workIQOpenUser(ctx, workiq.Invocation{}, "test.user@example.invalid"); !errors.Is(err, context.Canceled) {
		t.Fatal("a canceled backoff did not stop the identity check", err)
	}
}

// A failure is recorded only against one archived account of its family, and a failure to read the
// archive's accounts is kept with the cause.
func TestWorkIQFailureRecordingNeedsOneArchivedAccount(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	ctx := context.Background()
	cause := workIQFailure("workiq_protocol")
	report := WorkIQReport{Enabled: true, Gaps: map[string]int{}}
	if err := recordWorkIQFailure(ctx, WorkIQOptions{DBPath: db, Source: "chats"}, &report, "chats", cause); !errors.Is(err, cause) || len(report.Accounts) != 0 {
		t.Fatal("a failure without one archived account was recorded against a guessed account", err, report.Accounts)
	}
	broken := errors.New("synthetic read failure")
	original := workIQRead
	t.Cleanup(func() { workIQRead = original })
	workIQRead = func(context.Context, string, func(*store.Store) error) error { return broken }
	if err := recordWorkIQFailure(ctx, WorkIQOptions{DBPath: db, Source: "mail"}, &report, "mail", cause); !errors.Is(err, broken) ||
		!errors.Is(err, cause) || len(report.Accounts) != 0 {
		t.Fatal("an unreadable account list lost the cause or the read failure", err, report.Accounts)
	}
}

// Live: an unexplained failure tied to one page ended every run as resumable workiq_retry_later, so
// an agent following the result reran forever. When a run stops at the same unchanged position for
// the same reason as the run before it, the result is workiq_stuck: not resumable, with the
// diagnostic. A stop after a run that got further is an ordinary resumable stop again.
func TestWorkIQRepeatedUnexplainedStopAtOnePositionIsStuck(t *testing.T) {
	noWorkIQWait(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name, path string
		options    func(db, account string) WorkIQOptions
	}{
		{"mail", "folder-inbox/messages", func(db, account string) WorkIQOptions {
			return WorkIQOptions{DBPath: db, Source: "mail", Account: account}
		}},
		{"chats", "/chats/chat/messages", func(db, _ string) WorkIQOptions { return WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			t.Setenv("WORKIQ_ACQUISITION_FAIL_PATH", tc.path)
			options := tc.options(db, account)
			if report, err := RunWorkIQ(ctx, options); workIQCode(err) != "workiq_retry_later" || !report.Resumable {
				t.Fatalf("the first stop was not resumable: %v %v", err, report.Resumable)
			}
			for run := 2; run <= 3; run++ {
				report, err := RunWorkIQ(ctx, options)
				if workIQCode(err) != "workiq_stuck" || report.Resumable || len(report.Accounts) == 0 || report.Accounts[0].Diagnostic == nil ||
					report.Accounts[0].Diagnostic.Server != "tool error: Unhandled server failure for [redacted]" {
					t.Fatalf("run %d repeated the stop without reporting it stuck: %v resumable %v %+v", run, err, report.Resumable, report.Accounts)
				}
			}
			if count(t, db, `select count(*) from workiq_attempts where error_code='workiq_stuck' and finished_at is not null`) != 2 {
				t.Fatal("the stuck runs did not close their attempts with workiq_stuck")
			}
			t.Setenv("WORKIQ_ACQUISITION_FAIL_PATH", "")
			if _, err := RunWorkIQ(ctx, options); err != nil {
				t.Fatal("the run did not finish once the failure cleared", err)
			}
			t.Setenv("WORKIQ_ACQUISITION_FAIL_PATH", tc.path)
			if report, err := RunWorkIQ(ctx, options); workIQCode(err) != "workiq_retry_later" || !report.Resumable {
				t.Fatalf("a stop after a run that finished was called stuck: %v %v", err, report.Resumable)
			}
		})
	}
}

// When the archive cannot say where the previous run stopped, a stop stays the ordinary resumable
// one rather than being guessed stuck.
func TestWorkIQStuckNeedsTheArchiveToSay(t *testing.T) {
	cause := &workIQStop{error: workIQFailure("workiq_retry_later"), at: "same place"}
	original := workIQRead
	t.Cleanup(func() { workIQRead = original })
	workIQRead = func(context.Context, string, func(*store.Store) error) error {
		return errors.New("synthetic read failure")
	}
	err := workIQStuck(context.Background(), WorkIQOptions{DBPath: "unread", Kind: "sync"}, "mail", "outlook/Main", cause)
	if !errors.Is(err, cause) || workIQCode(err) != "workiq_retry_later" {
		t.Fatal("an unreadable archive turned a resumable stop into another outcome", err)
	}
}
