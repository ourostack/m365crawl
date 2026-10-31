package syncer

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
)

func TestWorkIQCoverageCountsNeverBecomeRetainedSourceData(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account, Kind: "coverage"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Mail) != 7 || report.Kind != "coverage" {
		t.Fatal("counts-only traversal did not return every root/child folder total")
	}
	for _, row := range report.Mail {
		if row.Account != account || row.Basis != "folder_total" || row.ServerTotal == nil || row.Overlap != nil {
			t.Fatal("folder totals invented a window or a known current overlap")
		}
	}
	assertWorkIQProbeHasNoData(t, db)
	if count(t, db, `select count(*) from workiq_attempts where kind='coverage' and status='ok' and all_folders=0`) != 1 {
		t.Fatal("coverage did not persist its distinct noncovering outcome")
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "messages/delta") {
		t.Fatal("mail folder totals fetched message bodies or data cursors")
	}
}

func TestWorkIQCoveragePagesWholeChatWindowWithoutRetainingBodies(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage",
		Since: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), MaxChats: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chats) != 1 || report.Chats[0].Conversation != "chat" || report.Chats[0].ServerCount != 2 ||
		report.Chats[0].ArchiveCount != 0 || !report.Chats[0].Complete || report.Gaps["workiq_selection_capped"] != 1 {
		t.Fatal("chat counts stopped at an old first page or lost scope/cap")
	}
	assertWorkIQProbeHasNoData(t, db)
	if count(t, db, `select count(*) from conversations`) != 1 {
		t.Fatal("coverage retained a server-only conversation")
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "$select") || strings.Contains(string(raw), "$expand") {
		t.Fatal("counts guessed an unqualified endpoint option")
	}
}

func TestWorkIQExplicitDisabledStatusProbesWithoutEnablingOrWriting(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	ctx := context.Background()
	s, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWorkIQEnabled(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := RunWorkIQ(ctx, WorkIQOptions{DBPath: db, Source: "mail", Account: account, Kind: "status"})
	if err != nil || report.Enabled || report.Kind != "status" || len(report.Probes) != 1 ||
		report.Probes[0].PathClass != "mail_folders" || report.Probes[0].State != "allowed" {
		t.Fatal("explicit status could not probe disabled source without enabling it", err)
	}
	assertWorkIQProbeHasNoData(t, db)
	if count(t, db, `select count(*) from workiq_attempts`) != 0 || count(t, db, `select count(*) from workiq_accounts`) != 0 ||
		count(t, db, `select count(*) from meta where key='workiq_enabled' and value='0'`) != 1 {
		t.Fatal("disabled status mutated opt-in, binding or outcomes")
	}
	raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), `"tool":"fetch"`) != 3 {
		t.Fatal("status fetched more than /me bootstrap, pinned /me and one bounded path probe")
	}
	public, err := json.Marshal(report)
	if err != nil || strings.Contains(string(public), "test.user@") || strings.Contains(string(public), "$skiptoken") {
		t.Fatal("status disclosed hidden identity or cursor", err)
	}
}

func assertWorkIQProbeHasNoData(t *testing.T, db string) {
	t.Helper()
	for _, table := range []string{"workiq_pages", "workiq_frontiers", "workiq_censuses", "workiq_census_members",
		"workiq_mail_receipts", "workiq_rows", "workiq_folders", "mail_messages", "messages"} {
		if count(t, db, "select count(*) from "+table) != 0 {
			t.Fatalf("counts/probe retained source data in %s", table)
		}

	}
}

func TestWorkIQCoverageAcceptsRootParentAndHandlesCrossPageRepeats(t *testing.T) {
	t.Run("root is not parentless", func(t *testing.T) {
		db, account := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		t.Setenv("WORKIQ_PROBE_CASE", "root-parent")
		report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account, Kind: "coverage"})
		if err != nil || len(report.Mail) != 7 {
			t.Fatal("folder root's actual parent was mistaken for a foreign child", err)
		}
		assertWorkIQProbeHasNoData(t, db)
	})
	t.Run("duplicate across pages", func(t *testing.T) {
		db, _ := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		seedWorkIQChatOwner(t, db)
		t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
		t.Setenv("WORKIQ_PROBE_CASE", "duplicate-message")
		report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage", MaxChats: 1})
		if err != nil || len(report.Chats) != 1 || report.Chats[0].Complete ||
			report.Gaps["workiq_message_changed_during_traversal"] != 1 || report.Chats[0].ServerCount != 0 {
			t.Fatal("a conflicting repeat aborted coverage, was double-counted or left the chat complete", err, report.Chats)
		}
		assertWorkIQProbeHasNoData(t, db)
	})
	t.Run("exact repeat across pages", func(t *testing.T) {
		db, _ := workIQAcquisitionArchive(t)
		workIQAcquisitionStub(t)
		seedWorkIQChatOwner(t, db)
		t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
		t.Setenv("WORKIQ_PROBE_CASE", "exact-repeat-message")
		report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage", MaxChats: 1})
		if err != nil || len(report.Chats) != 1 || !report.Chats[0].Complete || report.Counts["duplicates_skipped"] != 1 ||
			report.Chats[0].ServerCount != 2 {
			t.Fatal("Graph's exact repeat aborted coverage or was counted twice", err, report.Chats, report.Counts)
		}
		assertWorkIQProbeHasNoData(t, db)
	})
}
