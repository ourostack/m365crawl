package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func setWorkIQFreeBytes(t *testing.T, free func(string) (int64, error)) {
	t.Helper()
	old := workIQFreeBytes
	t.Cleanup(func() { workIQFreeBytes = old })
	workIQFreeBytes = free
}

func stagedWorkIQMessagePages(t *testing.T, db string) int {
	t.Helper()
	return count(t, db, `select count(*) from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id where f.resource like '%/messages/delta'`)
}

// A run whose disk would hold acquisition but not the admission of everything it fetches is
// refused before its first message page, so it never strands staged pages at final admission.
func TestWorkIQMailSyncRefusesUpFrontWhenAdmissionWouldNotFit(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	full := store.WorkIQMailDisk{InventoryItems: 6}.Need()
	acquisitionOnly := int64(6*store.WorkIQAcquireBytesPerItem*5/4 + 2*16<<20 + 256<<20 + 1<<30)
	free := full - 1
	if free <= acquisitionOnly {
		t.Fatal("fixture does not separate acquisition from admission")
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return free, nil })
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_disk_space" || report.Resumable || report.Gaps["workiq_disk_space"] != 1 {
		t.Fatal("a sync that could not admit what it fetched was not refused", err, report.Resumable)
	}
	if !strings.Contains(coded.Fix, "--since and --until") || !strings.Contains(coded.Fix, "6 more items") || !strings.Contains(coded.Fix, "position is kept") {
		t.Fatal("the refusal did not say why, what is kept, or where range backfill is tracked:", coded.Fix)
	}
	if stagedWorkIQMessagePages(t, db) != 0 || report.Counts["disk_inventory_items"] != 6 || report.Counts["disk_needed_bytes"] != int(full) ||
		report.Counts["disk_free_bytes"] != int(free) {
		t.Fatalf("message pages were fetched or the estimate was not reported: %+v", report.Counts)
	}
	if count(t, db, `select count(*) from workiq_attempts where error_code='workiq_disk_space' and json_extract(counts_json,'$.disk_inventory_items')=6`) != 1 {
		t.Fatal("the stored attempt lost the refusal code or its estimate")
	}
}

// Free space falling mid-run stops the run before the next page, with its position kept; a later
// run with room continues from there and finishes.
func TestWorkIQMailSyncStopsWhenFreeSpaceFallsAndContinuesLater(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	checks := 0
	setWorkIQFreeBytes(t, func(string) (int64, error) {
		checks++
		if checks > 3 {
			return 0, nil
		}
		return 1 << 50, nil
	})
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	first, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_disk_space" || first.Resumable || stagedWorkIQMessagePages(t, db) != 3 {
		t.Fatal("falling free space did not stop the run before the next page", err, stagedWorkIQMessagePages(t, db))
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 1 << 50, nil })
	second, err := RunWorkIQ(context.Background(), options)
	if err != nil || count(t, db, `select count(*) from mail_messages`) != 6 || count(t, db, `select count(*) from workiq_frontiers where retired=1`) != 0 {
		t.Fatal("the next run did not continue from the kept position", err, second.Counts)
	}
}

func TestWorkIQMailSyncFailsClosedWhenFreeSpaceIsUnknown(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	broken := errors.New("synthetic free-space failure")
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 0, broken })
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account}); !errors.Is(err, broken) ||
		stagedWorkIQMessagePages(t, db) != 0 {
		t.Fatal("a sync fetched messages without knowing the free space", err)
	}
}

// Archive growth a run measures only raises the per-item estimate.
func TestWorkIQMailSyncMeasuresArchiveGrowthPerItem(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "200")
	old := workIQArchiveBytes
	t.Cleanup(func() { workIQArchiveBytes = old })
	calls := int64(0)
	workIQArchiveBytes = func(string) int64 {
		calls++
		return calls * 400 << 20 // 400 MiB between the first check and the staged page
	}
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	if err != nil || report.Counts["disk_acquire_bytes_per_item"] != 2<<20 || report.Counts["disk_staged_items"] != 200 || report.Counts["disk_fetched_items"] != 200 {
		t.Fatal("measured growth was not kept as the per-item figure", err, report.Counts)
	}
	if got := old(db); got <= 0 {
		t.Fatal("the archive's size was not read", got)
	}
}

func TestWorkIQMailDiskCostText(t *testing.T) {
	d := store.WorkIQMailDisk{InventoryItems: 366000, FetchedItems: 15502, StagedItems: 15502}
	text := WorkIQMailDiskCost(d, d.Need(), 15e9)
	if !strings.Contains(text, "about 350498 more items in about 35050 pages (roughly 19 hours at about 31 pages a minute)") ||
		!strings.Contains(text, "and 15.0 GB is free") || WorkIQMailDiskPerItem() != "340 KB" {
		t.Fatal("cost text:", text, WorkIQMailDiskPerItem())
	}
	if small := WorkIQMailDiskCost(store.WorkIQMailDisk{InventoryItems: 60}, 0, 0); !strings.Contains(small, "roughly 1 minutes") {
		t.Fatal("short cost text:", small)
	}
}

// After a complete sync every folder's cursor is admitted, so a catch-up fetches only changes and
// needs only the page and transaction reserve, not room for the whole mailbox again.
func TestWorkIQMailIncrementalSyncAfterACompleteSyncIsNotRefused(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "200")
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	whole := store.WorkIQMailDisk{InventoryItems: 200}.Need()
	free := store.WorkIQMailDisk{}.Need() + 10<<20
	if free >= whole {
		t.Fatal("fixture does not separate a catch-up from a whole-mailbox fetch")
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return free, nil })
	report, err := RunWorkIQ(context.Background(), options)
	if err != nil || report.Counts["disk_inventory_items"] != 0 || report.Counts["disk_needed_bytes"] > int(free) {
		t.Fatal("a catch-up after a complete sync was charged for the whole mailbox", err, report.Counts)
	}
}

// A run that continues an earlier one counts the items that run already fetched, so it neither
// overstates the remaining fetch nor forgets their admission.
func TestWorkIQMailResumedRunCountsItemsFetchedEarlier(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	old := workIQMailPageLimit
	t.Cleanup(func() { workIQMailPageLimit = old })
	workIQMailPageLimit = 15
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(context.Background(), options); !workIQPageLimitCode(err) {
		t.Fatal("the first run did not stop at its page budget", err)
	}
	fetched := count(t, db, `select count(*) from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
where f.resource='/me/mailFolders/folder-inbox/messages/delta'`)
	if fetched == 0 || fetched >= 6 {
		t.Fatal("fixture did not stop inside the Inbox", fetched)
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return 0, nil })
	report, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_disk_space" || report.Counts["disk_fetched_items"] != fetched ||
		report.Counts["disk_staged_items"] != fetched || !strings.Contains(coded.Fix, fmt.Sprintf("about %d more items", 6-fetched)) {
		t.Fatalf("the resumed run miscounted earlier pages: %v %+v", err, report.Counts)
	}
}

// The whole folder tree, child folders included, is listed before any message page, so the first
// check sees the full inventory. Live, the Inbox's child folders raised it from 28,570 items to
// 366,180 only after 843 pages.
func TestWorkIQMailSyncListsEveryFolderBeforeTheFirstMessagePage(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	t.Setenv("WORKIQ_ACQUISITION_CHILD_TOTAL", "5000")
	free := store.WorkIQMailDisk{InventoryItems: 6}.Need() + 10<<20
	if free >= (store.WorkIQMailDisk{InventoryItems: 5006}).Need() {
		t.Fatal("fixture does not separate the top-level folders from the whole tree")
	}
	setWorkIQFreeBytes(t, func(string) (int64, error) { return free, nil })
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_disk_space" || report.Counts["disk_inventory_items"] != 5006 ||
		stagedWorkIQMessagePages(t, db) != 0 {
		t.Fatalf("message pages were fetched before the child folders were counted: %v %d %+v", err, stagedWorkIQMessagePages(t, db), report.Counts)
	}
}
