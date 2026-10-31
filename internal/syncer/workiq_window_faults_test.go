package syncer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// windowScenario is a mailbox whose window run meets every step: a listing folder, a round that
// restarts, and pages that clear.
func windowScenario(t *testing.T) {
	t.Helper()
	messages := windowMessages()
	events := []mbEvent{{At: 33, Op: "move", mbMessage: mbMessage{ID: "child-0"}, To: "inbox-id", NewID: "moved-0"}}
	writeMailbox(t, mbScenario{Messages: messages, Events: events})
}

type windowArchiveSites struct{ reads, writes []int }

// discoverWindowArchiveSites runs one window and returns, for each distinct place in the code that
// reads or writes the archive, the number of its first call, so a sweep fails each place once.
func discoverWindowArchiveSites(t *testing.T) windowArchiveSites {
	t.Helper()
	originalRead, originalWrite := workIQRead, workIQWrite
	defer func() { workIQRead, workIQWrite = originalRead, originalWrite }()
	var out windowArchiveSites
	seen := map[string]bool{}
	reads, writes := 0, 0
	first := func(kind string) bool {
		key := kind
		for depth := 2; depth <= 4; depth++ {
			_, file, line, _ := runtime.Caller(depth)
			key += fmt.Sprintf(" %s:%d", file, line)
		}
		if seen[key] {
			return false
		}
		seen[key] = true
		return true
	}
	workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
		if reads++; first("read") {
			out.reads = append(out.reads, reads)
		}
		return originalRead(ctx, path, op)
	}
	workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
		if writes++; first("write") {
			out.writes = append(out.writes, writes)
		}
		return originalWrite(ctx, path, op)
	}
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	windowScenario(t)
	if _, err := runWindow(t, db, account, ""); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWorkIQMailWindowEveryArchiveBoundaryFailsClosed(t *testing.T) {
	originalRead, originalWrite := workIQRead, workIQWrite
	t.Cleanup(func() { workIQRead, workIQWrite = originalRead, originalWrite })
	sites := discoverWindowArchiveSites(t)
	for _, boundary := range []struct {
		name  string
		sites []int
	}{{"read", sites.reads}, {"write", sites.writes}} {
		for _, at := range boundary.sites {
			t.Run(fmt.Sprintf("%s-%d", boundary.name, at), func(t *testing.T) {
				db, account := workIQAcquisitionArchive(t)
				workIQAcquisitionStub(t)
				windowScenario(t)
				failure := errors.New("synthetic archive boundary failure")
				calls := 0
				workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
					if boundary.name == "read" {
						if calls++; calls == at {
							return failure
						}
					}
					return originalRead(ctx, path, op)
				}
				workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
					if boundary.name == "write" {
						if calls++; calls == at {
							return failure
						}
					}
					return originalWrite(ctx, path, op)
				}
				report, err := runWindow(t, db, account, "")
				workIQRead, workIQWrite = originalRead, originalWrite
				if calls < at || !errors.Is(err, failure) || report.Resumable {
					t.Fatalf("an archive boundary failure was lost or invited a rerun: %v", err)
				}
				for _, a := range report.Accounts {
					if a.Status == "ok" || a.Status == "ok_with_omissions" {
						t.Fatal("a failed window reported a positive outcome")
					}
				}
			})
		}
	}
}

func TestWorkIQMailWindowEveryFetchFailureStopsResumably(t *testing.T) {
	noWorkIQWait(t)
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	windowScenario(t)
	if _, err := runWindow(t, db, account, ""); err != nil {
		t.Fatal(err)
	}
	fetches := 0
	f, err := os.Open(transcript) //nolint:gosec // G304: the test's own transcript.
	if err != nil {
		t.Fatal(err)
	}
	for scanner := bufio.NewScanner(f); scanner.Scan(); {
		fetches++
	}
	_ = f.Close()
	for from := 2; from <= fetches; from += 4 {
		t.Run(strconv.Itoa(from), func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			windowScenario(t)
			t.Setenv("WORKIQ_ACQUISITION_FAIL_FROM", strconv.Itoa(from))
			t.Setenv("WORKIQ_ACQUISITION_FAIL_COUNTER", filepath.Join(t.TempDir(), "counter"))
			report, err := runWindow(t, db, account, "")
			if err == nil || count(t, db, `select count(*) from workiq_window_coverage`) != 0 {
				t.Fatalf("a failing fetch was proven: %v", err)
			}
			if code := workIQCode(err); code == "workiq_retry_later" && !report.Resumable {
				t.Fatal("a transient stop was not resumable")
			}
		})
	}
}

func TestWorkIQMailWindowRefusesMalformedAnswers(t *testing.T) {
	for _, tc := range []struct{ broken, code string }{
		{"count", "workiq_protocol"},
		{"folder", "workiq_protocol"},
		{"collection", "workiq_protocol"},
		{"child_parent", "workiq_folder_inventory_ambiguous"},
		{"next_scope", "workiq_cursor_invalid"},
		{"many_folders", "workiq_folder_limit"},
		{"dup_child", "workiq_folder_inventory_ambiguous"},
		{"bad_child", "workiq_protocol"},
		{"root_next_scope", "workiq_cursor_invalid"},
	} {
		t.Run(tc.broken, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			sc := mbScenario{Messages: windowMessages()}
			writeMailbox(t, sc)
			raw, err := os.ReadFile(os.Getenv("WORKIQ_MAILBOX")) //nolint:gosec // G304: the test's own scenario file.
			if err != nil {
				t.Fatal(err)
			}
			patched := strings.Replace(string(raw), `"state":`, `"break":"`+tc.broken+`","state":`, 1)
			if err := os.WriteFile(os.Getenv("WORKIQ_MAILBOX"), []byte(patched), 0o600); err != nil { //nolint:gosec // G703: the test's own scenario file.
				t.Fatal(err)
			}
			report, err := runWindow(t, db, account, "")
			if workIQCode(err) != tc.code || report.Resumable || count(t, db, `select count(*) from mail_messages`) != 0 {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestWorkIQMailWindowStopsAtASecondExpiry(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages(), Expire: []map[string]any{{"folder": "inbox-id", "at": 30}, {"folder": "inbox-id", "at": 31}}})
	report, err := runWindow(t, db, account, "")
	if err == nil || report.Gaps["workiq_delta_expired"] != 1 || report.Window.Covered {
		t.Fatalf("a second expiry was not a gap: %v %v", report.Gaps, err)
	}
	// A second expiry while following a restarted folder's changes stops the run too.
	db, account = workIQAcquisitionArchive(t)
	writeMailbox(t, mbScenario{Messages: windowMessages(), Expire: []map[string]any{{"folder": "inbox-id", "at": 31}, {"folder": "inbox-id", "at": 40}}})
	report, err = runWindow(t, db, account, "")
	if err == nil || report.Gaps["workiq_delta_expired"] != 1 || report.Window.Covered {
		t.Fatalf("a second expiry in a round was not a gap: %v %v", report.Gaps, err)
	}
	// A listing's own position is restarted too when its skip token expires.
	db, account = workIQAcquisitionArchive(t)
	writeMailbox(t, mbScenario{Messages: windowMessages(), Expire: []map[string]any{{"folder": "inbox-id", "at": 1}}})
	report, err = runWindow(t, db, account, "proof")
	if err != nil || !report.Window.Covered {
		t.Fatalf("an expired baseline cursor was not restarted: %v", err)
	}
}

func TestWorkIQMailWindowStartupFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("disabled", func(t *testing.T) {
		db, account := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		s, err := store.Open(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SetWorkIQEnabled(ctx, false); err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
		if _, err := runWindow(t, db, account, ""); workIQCode(err) != "workiq_disabled" {
			t.Fatal(err)
		}
	})
	for _, me := range []string{"fail", "pinned-mismatch"} {
		t.Run(me, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			writeMailbox(t, mbScenario{Messages: windowMessages()})
			t.Setenv("WORKIQ_ACQUISITION_ME", me)
			report, err := runWindow(t, db, account, "")
			if err == nil || count(t, db, `select count(*) from workiq_attempts where kind='mail_window' and status='failed'`) != 1 || report.Resumable {
				t.Fatalf("a startup failure left no failed window attempt: %v", err)
			}
		})
	}
	t.Run("unknown account", func(t *testing.T) {
		db, _ := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		if _, err := runWindow(t, db, "outlook/Unknown", ""); err == nil {
			t.Fatal("an unknown account was synced")
		}
	})
	t.Run("scope held", func(t *testing.T) {
		db, account := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		release, err := store.AcquireLock(workIQRunLockPath(db, "mail", account, "mail_window"))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if _, err := runWindow(t, db, account, ""); workIQCode(err) != "locked" {
			t.Fatalf("a held window scope was not locked: %v", err)
		}
		// A whole-account sync of the same account is another scope.
		writeMailbox(t, mbScenario{Messages: windowMessages()})
		if _, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "mail", Account: account}); workIQCode(err) == "locked" {
			t.Fatal("a window's lock blocked whole-account sync")
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		db, account := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		writeMailbox(t, mbScenario{Messages: windowMessages()})
		old := workIQClose
		t.Cleanup(func() { workIQClose = old })
		workIQClose = func(c *workiq.Client) error { return errors.Join(old(c), errors.New("synthetic cleanup failure")) }
		report, err := runWindow(t, db, account, "")
		if err == nil || report.Resumable || report.Accounts[0].Status != "failed" {
			t.Fatalf("a cleanup failure was a positive outcome: %v", err)
		}
	})
}

// The archive calls each window step makes inside its read or write fail on a closed archive, so
// every one of them reports its failure rather than an empty result.
func TestWorkIQMailWindowEveryStepInsideAnArchiveCallFailsClosed(t *testing.T) {
	originalRead, originalWrite := workIQRead, workIQWrite
	t.Cleanup(func() { workIQRead, workIQWrite = originalRead, originalWrite })
	sites := discoverWindowArchiveSites(t)
	for _, boundary := range []struct {
		name  string
		sites []int
	}{{"read", sites.reads}, {"write", sites.writes}} {
		for _, at := range boundary.sites {
			t.Run(fmt.Sprintf("%s-%d", boundary.name, at), func(t *testing.T) {
				db, account := workIQAcquisitionArchive(t)
				workIQAcquisitionStub(t)
				windowScenario(t)
				calls := 0
				workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
					if boundary.name == "read" {
						if calls++; calls == at {
							return originalRead(ctx, path, func(s *store.Store) error { _ = s.Close(); return op(s) })
						}
					}
					return originalRead(ctx, path, op)
				}
				workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
					if boundary.name == "write" {
						if calls++; calls == at {
							return originalWrite(ctx, path, func(writeCtx context.Context, s *store.Store) error { _ = s.Close(); return op(writeCtx, s) })
						}
					}
					return originalWrite(ctx, path, op)
				}
				_, err := runWindow(t, db, account, "")
				workIQRead, workIQWrite = originalRead, originalWrite
				if err == nil {
					t.Fatal("a step on a closed archive succeeded")
				}
			})
		}
	}
}

// A listing a previous run finished is retired before the next walk; that write failing fails the run.
func TestWorkIQMailWindowListingRestartFailureFailsTheRun(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	if _, err := runWindow(t, db, account, "fast"); err != nil {
		t.Fatal(err)
	}
	originalWrite := workIQWrite
	t.Cleanup(func() { workIQWrite = originalWrite })
	failure := errors.New("synthetic retire failure")
	retired := count(t, db, `select count(*) from workiq_frontiers where retired=1`)
	for at := 1; at <= 6; at++ {
		calls := 0
		workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
			if calls++; calls == at {
				return failure
			}
			return originalWrite(ctx, path, op)
		}
		if _, err := runWindow(t, db, account, "fast"); !errors.Is(err, failure) {
			t.Fatalf("write %d: %v", at, err)
		}
	}
	workIQWrite = originalWrite
	if count(t, db, `select count(*) from workiq_frontiers where retired=1`) <= retired {
		t.Fatal("no listing was retired")
	}
}

func TestWorkIQMailWindowDiskFailuresAndUndatedMail(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	failure := errors.New("synthetic free-space failure")
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 0, failure })
	if _, err := runWindow(t, db, account, ""); !errors.Is(err, failure) {
		t.Fatalf("a free-space failure was ignored: %v", err)
	}
	// Only the check before admission falls short.
	checks := 0
	setWorkIQFreeBytes(t, func(string) (int64, error) { checks++; return 1 << 40, nil })
	db, account = workIQAcquisitionArchive(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	if _, err := runWindow(t, db, account, ""); err != nil {
		t.Fatal(err)
	}
	last, seen := checks, 0
	setWorkIQFreeBytes(t, func(string) (int64, error) {
		if seen++; seen == last {
			return 0, nil
		}
		return 1 << 40, nil
	})
	db, account = workIQAcquisitionArchive(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	report, err := runWindow(t, db, account, "")
	if workIQCode(err) != "workiq_disk_reserve" || !report.Resumable || count(t, db, `select count(*) from mail_messages`) != 0 {
		t.Fatalf("a short disk before admission admitted mail: %v", err)
	}
	// An undated item and one without a Message-ID: the folder is not proven, and gaps say why.
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 1 << 40, nil })
	db, account = workIQAcquisitionArchive(t)
	messages := append(windowMessages(), mbMessage{ID: "undated", Folder: "sent-id", Header: "<undated@example.test>", Modified: "2026-09-02T00:00:00Z"},
		mbMessage{ID: "headerless", Folder: "drafts-id", Received: "2026-09-03T00:00:00Z", Modified: "2026-09-03T00:00:00Z"})
	writeMailbox(t, mbScenario{Messages: messages})
	report, err = runWindow(t, db, account, "")
	if err != nil || report.Window.Covered || report.Gaps["workiq_clock_unknown"] != 1 || report.Gaps["workiq_mail_message_id_missing"] != 1 ||
		count(t, db, `select count(*) from workiq_window_coverage where folder_id='sent-id'`) != 0 ||
		count(t, db, `select count(*) from workiq_window_coverage where folder_id='drafts-id'`) != 1 {
		t.Fatalf("undated or headerless mail was misreported: %v %v", report.Gaps, err)
	}
}

// A listing that a stopped run fetched but never admitted is kept, so the resumed run admits it
// rather than fetching the same mail again and stranding the first copy.
func TestWorkIQMailWindowResumeAdmitsAKeptListing(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	checks := 0
	setWorkIQFreeBytes(t, func(string) (int64, error) { checks++; return 1 << 40, nil })
	probe, _ := workIQAcquisitionArchive(t)
	if _, err := runWindow(t, probe, account, "fast"); err != nil {
		t.Fatal(err)
	}
	last, seen := checks, 0
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	setWorkIQFreeBytes(t, func(string) (int64, error) {
		if seen++; seen == last {
			return 0, nil
		}
		return 1 << 40, nil
	})
	if _, err := runWindow(t, db, account, "fast"); workIQCode(err) != "workiq_disk_reserve" {
		t.Fatalf("the first run did not stop before admission: %v", err)
	}
	listed := count(t, db, `select count(*) from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where f.resource like '%inbox-id%' or f.resource like '%child-id%'`)
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 1 << 40, nil })
	if _, err := runWindow(t, db, account, "fast"); err != nil {
		t.Fatal(err)
	}
	// Folders with mail in the window keep their listing; empty ones are cheaply listed again.
	if count(t, db, `select count(*) from workiq_frontiers where retired=1 and (resource like '%inbox-id%' or resource like '%child-id%')`) != 0 ||
		count(t, db, `select count(*) from mail_messages`) != 5 || count(t, db, `select count(*) from workiq_pages where data_json<>''`) != 0 ||
		count(t, db, `select count(*) from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where f.resource like '%inbox-id%' or f.resource like '%child-id%'`) != listed {
		t.Fatal("a resumed listing was fetched again or left page bodies behind")
	}
}

func TestWorkIQMailWindowNeedsAnAccountWhenSeveralAreArchived(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	writeMailbox(t, mbScenario{Messages: windowMessages()})
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitMail(ctx, nativeBatch("outlook/Second", windowSince)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := runWindow(t, db, "", ""); err == nil {
		t.Fatal("a window chose one of several accounts by itself")
	}
}
