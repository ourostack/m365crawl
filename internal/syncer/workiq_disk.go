package syncer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

// Test seams: free space on the archive's volume and the archive's size with its write-ahead log.
var (
	workIQFreeBytes    = store.FreeBytes
	workIQArchiveBytes = func(db string) int64 {
		var n int64
		for _, path := range []string{db, db + "-wal"} {
			if info, err := os.Stat(path); err == nil {
				n += info.Size()
			}
		}
		return n
	}
)

// workIQMeasureAfter is the items a run stages before its own archive growth per item counts;
// smaller samples measure page allocation more than mail.
const workIQMeasureAfter = 100

// workIQMailDiskRun is one mail sync's running disk estimate: the archive's facts read once, then
// updated from every page the run stages.
type workIQMailDiskRun struct {
	store.WorkIQMailDisk
	settled     map[string]bool // message resources whose cursor is admitted: they fetch only changes
	baseBytes   int64
	stagedItems int64
}

// workIQInventoryItems is the items of the folders listed so far whose cursor is not yet admitted.
func workIQInventoryItems(folders []workiq.Folder, settled map[string]bool) int64 {
	var n int64
	for _, folder := range folders {
		if folder.TotalCount != nil && !settled[store.WorkIQMailMessagesResource(folder.ID)] {
			n += *folder.TotalCount
		}
	}
	return n
}

// checkDisk runs before every message page: it stops the sync, position kept, when the archive's
// volume could no longer hold the rest of the fetch and the admission of every item not yet
// admitted. The first check of a run therefore refuses before any message page is fetched.
func (r *workIQMailRun) checkDisk(ctx context.Context) error {
	if r.disk == nil {
		var facts store.WorkIQMailDisk
		var settled map[string]bool
		if err := workIQRead(ctx, r.options.DBPath, func(s *store.Store) (err error) {
			facts, settled, err = s.WorkIQMailDiskFacts(ctx, r.account)
			return err
		}); err != nil {
			return err
		}
		r.disk = &workIQMailDiskRun{WorkIQMailDisk: facts, settled: settled, baseBytes: workIQArchiveBytes(r.options.DBPath)}
	}
	// The folders listed so far this run, or the inventory an earlier run kept, whichever is larger.
	r.disk.InventoryItems = max(r.disk.InventoryItems, workIQInventoryItems(r.queue, r.disk.settled))
	free, err := workIQFreeBytes(r.options.DBPath)
	if err != nil {
		return err
	}
	need := r.disk.Need()
	r.recordDisk(need, free)
	if free < need {
		return workIQDiskFailure(r.disk.WorkIQMailDisk, need, free)
	}
	return nil
}

// noteStaged moves a staged page's items from future to staged admission and keeps the highest
// archive growth per item the run has measured.
func (r *workIQMailRun) noteStaged(resource string, page workiq.Page) {
	var collection struct {
		Value []json.RawMessage `json:"value"`
	}
	_ = json.Unmarshal(page.Data, &collection) // staging already validated the page
	items := int64(len(collection.Value))
	r.disk.StagedItems += items
	if !r.disk.settled[resource] {
		r.disk.FetchedItems += items
	}
	r.disk.StagedBytes += int64(len(page.JSON))
	r.disk.LargestPage = max(r.disk.LargestPage, int64(len(page.JSON)))
	r.disk.stagedItems += items
	if items > 0 && r.disk.stagedItems >= workIQMeasureAfter {
		growth := (workIQArchiveBytes(r.options.DBPath) - r.disk.baseBytes) / r.disk.stagedItems
		r.disk.AcquireBytesPerItem = max(r.disk.AcquireBytesPerItem, growth)
	}
	for key, n := range r.disk.Counts() {
		r.report.Counts[key] = n
	}
}

func (r *workIQMailRun) recordDisk(need, free int64) {
	for key, n := range r.disk.Counts() {
		r.report.Counts[key] = n
	}
	r.report.Counts["disk_needed_bytes"] = int(need)
	r.report.Counts["disk_free_bytes"] = int(free)
}

func workIQDiskFailure(d store.WorkIQMailDisk, need, free int64) error {
	return &errs.Coded{Code: "workiq_disk_space", Exit: errs.ExitEnvironment,
		Message: "the explicit WorkIQ source could not complete (workiq_disk_space)",
		Fix: "Whole-account WorkIQ mail sync stopped before fetching more messages: " + WorkIQMailDiskCost(d, need, free) +
			" Its position is kept. Free space or move the archive, then run the same workiq sync again, or fetch only a date range with workiq sync --source mail --since and --until."}
}

// WorkIQMailDiskCost says what the rest of an account's whole-account mail sync needs.
func WorkIQMailDiskCost(d store.WorkIQMailDisk, need, free int64) string {
	return fmt.Sprintf("about %d more items in about %d pages (roughly %s at about %d pages a minute) need about %s free on the archive's disk, including admitting every fetched item, and %s is free.",
		d.Remaining(), d.Pages(), workIQDuration(d.Minutes()), store.WorkIQMailPagesPerMinute, WorkIQSize(need), WorkIQSize(free))
}

// WorkIQMailDiskPerItem is the free space whole-account mail sync needs per mailbox item before any
// measurement: fetching plus admitting it, a quarter more on both.
func WorkIQMailDiskPerItem() string {
	return fmt.Sprintf("%d KB", ((store.WorkIQAcquireBytesPerItem+store.WorkIQAdmitBytesPerItem)*5/4/1024+9)/10*10)
}

// WorkIQSize is a byte count in decimal gigabytes, as the disk estimate states it.
func WorkIQSize(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/1e9)
}

func workIQDuration(minutes int64) string {
	if minutes < 120 {
		return fmt.Sprintf("%d minutes", minutes)
	}
	return fmt.Sprintf("%d hours", (minutes+59)/60)
}

// workIQDiagnostic is the redacted WorkIQ child diagnostic a run's error carries, if any.
func workIQDiagnostic(err error) *workiq.Diagnostic {
	if d, ok := workiq.DiagnosticOf(err); ok {
		return &d
	}
	return nil
}
