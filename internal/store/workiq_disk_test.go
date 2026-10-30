package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The estimate reserves admission for every item not yet admitted, fetched or not, so a run that
// would fit acquisition alone is refused before it strands staged pages at final admission.
func TestWorkIQMailDiskNeedReservesFutureAdmission(t *testing.T) {
	fresh := WorkIQMailDisk{InventoryItems: 1000}
	wal := int64(2*16<<20 + 256<<20 + 1<<30)
	acquireOnly := 1000 * int64(WorkIQAcquireBytesPerItem) * 5 / 4
	if fresh.Need() != (1000*int64(WorkIQAcquireBytesPerItem)+1000*int64(WorkIQAdmitBytesPerItem))*5/4+wal || fresh.Need() <= acquireOnly+wal {
		t.Fatal("the estimate left out admission of items not yet fetched", fresh.Need())
	}
	if fresh.Remaining() != 1000 || fresh.Pages() != 100 || fresh.Minutes() != 4 {
		t.Fatal("remaining work was misestimated", fresh.Remaining(), fresh.Pages(), fresh.Minutes())
	}
	// Acquiring 400 items moves them from future to staged admission: only their acquisition, now
	// spent on disk, leaves the reserve, and the admission reserve never shrinks before admission.
	staged := WorkIQMailDisk{InventoryItems: 1000, FetchedItems: 400, StagedItems: 400, StagedBytes: 400 * 1024, LargestPage: 1 << 20}
	if fresh.Need()-staged.Need() != 400*int64(WorkIQAcquireBytesPerItem)*5/4 {
		t.Fatal("staging changed the admission reserve", fresh.Need()-staged.Need())
	}
	// Measured growth only ever raises the per-item figures above the defaults.
	heavy := WorkIQMailDisk{InventoryItems: 10, FetchedItems: 10, StagedItems: 10, StagedBytes: 10 * 100 << 10, LargestPage: 64 << 20, AcquireBytesPerItem: 1 << 20}
	if heavy.Need() != (10*200<<10)*5/4+2*64<<20+256<<20+1<<30 {
		t.Fatal("measured staged size or page size was ignored", heavy.Need())
	}
	if (WorkIQMailDisk{InventoryItems: 5, FetchedItems: 9, StagedItems: 9}).Remaining() != 0 || (WorkIQMailDisk{InventoryItems: 5, FetchedItems: 1, StagedItems: 1, AcquireBytesPerItem: 1 << 20}).Need() !=
		(4*(1<<20)+5*int64(WorkIQAdmitBytesPerItem))*5/4+wal {
		t.Fatal("staged items above the inventory or measured acquisition growth were mishandled")
	}
	// Changes staged on admitted folders need admission room but do not shorten the first fetch.
	if changes := (WorkIQMailDisk{InventoryItems: 10, StagedItems: 4}); changes.Remaining() != 10 ||
		changes.Need() != (10*int64(WorkIQAcquireBytesPerItem)+14*int64(WorkIQAdmitBytesPerItem))*5/4+wal {
		t.Fatal("incremental changes were mistaken for fetched inventory", changes.Remaining(), changes.Need())
	}
}

func TestWorkIQMailDiskFactsCountKnownInventoryAndUnadmittedPages(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if d, settled := must2Disk(s.WorkIQMailDiskFacts(ctx, mailAcct)); d != (WorkIQMailDisk{}) || len(settled) != 0 {
		t.Fatal("an archive without WorkIQ reported mail to fetch", d)
	}
	attempt := seedWorkIQMailAttempt(t, s)
	for _, statement := range []string{
		`insert into workiq_folders(account_id,graph_id,folder_key,inventory_attempt,total_items) values('` + mailAcct + `','a',1,1,700),('` + mailAcct + `','b',2,1,300),('` + mailAcct + `','c',3,1,null),('` + mailAcct + `','e e',5,1,5000),('outlook/Other','d',4,1,9000)`,
		`insert into workiq_frontiers(id,family,account_id,resource,qualification) values
(1,'mail','` + mailAcct + `','/me/mailFolders/a/messages/delta','q'),(2,'mail','` + mailAcct + `','/me/mailFolders/b/messages/delta','q'),
(3,'mail','` + mailAcct + `','/me/mailFolders?includeHiddenFolders=true','q'),(4,'mail','outlook/Other','/me/mailFolders/d/messages/delta','q')`,
		`insert into workiq_frontiers(id,family,account_id,resource,qualification,retired,generation) values(5,'mail','` + mailAcct + `','/me/mailFolders/a/messages/delta','q',1,1)`,
		// Folder "e e" was fetched and admitted: its next pages carry only changes.
		`insert into workiq_frontiers(id,family,account_id,resource,qualification,baseline) values(6,'mail','` + mailAcct + `','/me/mailFolders/e%20e/messages/delta','q',0)`,
		`insert into workiq_pages(attempt_id,frontier_id,expected_revision,data_json,observed_at,state) values
(?,1,0,'{"value":[1,2,3]}','',	'staged'),(?,1,1,'{"value":[1,2,3,4,5,6,7,8,9,10]}','','staged'),(?,2,0,'{"value":[1]}','','admitted'),
(?,3,0,'{"value":[1,2]}','','staged'),(?,4,0,'{"value":[1,2]}','','staged'),(?,5,0,'{"value":[1,2]}','','staged'),(?,6,0,'{"value":[1,2]}','','staged')`,
	} {
		args := []any{}
		for range strings.Count(statement, "?") {
			args = append(args, attempt)
		}
		must(s.db.ExecContext(ctx, statement, args...))
	}
	d, settled := must2Disk(s.WorkIQMailDiskFacts(ctx, mailAcct))
	pages := int64(len(`{"value":[1,2,3]}`) + len(`{"value":[1,2,3,4,5,6,7,8,9,10]}`) + len(`{"value":[1,2]}`))
	if d.InventoryItems != 1000 || d.FetchedItems != 13 || d.StagedItems != 15 || d.StagedBytes != pages ||
		d.LargestPage != int64(len(`{"value":[1,2,3,4,5,6,7,8,9,10]}`)) || len(settled) != 1 || !settled["/me/mailFolders/e%20e/messages/delta"] {
		t.Fatalf("disk facts counted the wrong inventory or pages: %+v %v", d, settled)
	}
	must0(s.Close())
	if _, _, err := s.WorkIQMailDiskFacts(ctx, mailAcct); err == nil {
		t.Fatal("a closed archive answered")
	}
}

func must2Disk(d WorkIQMailDisk, settled map[string]bool, err error) (WorkIQMailDisk, map[string]bool) {
	must0(err)
	return d, settled
}

// A mail sync's completion keeps its estimate, so a read's route can state the cost without
// recomputing it from staged pages.
func TestWorkIQMailDiskEstimateComesFromTheLatestFinishedSync(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if _, ok := must2(s.WorkIQMailDiskEstimate(ctx, mailAcct)); ok {
		t.Fatal("an archive without WorkIQ had an estimate")
	}
	attempt := seedWorkIQMailAttempt(t, s)
	if _, ok := must2(s.WorkIQMailDiskEstimate(ctx, mailAcct)); ok {
		t.Fatal("an open attempt had an estimate")
	}
	want := WorkIQMailDisk{InventoryItems: 366000, FetchedItems: 15502, StagedItems: 15502, StagedBytes: 877 << 20, LargestPage: 10 << 20, AcquireBytesPerItem: 142 << 10}
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "failed", ErrorCode: "workiq_disk_space", FinishedAt: mailT0.Add(time.Hour),
		Counts: want.Counts(), Gaps: map[string]int{"workiq_disk_space": 1}}))
	later := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(2*time.Hour)))
	must0(s.CompleteWorkIQAttempt(ctx, later, WorkIQSummary{Status: "failed", ErrorCode: "workiq_failed", FinishedAt: mailT0.Add(3 * time.Hour)}))
	probe := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "status"}, mailT0.Add(4*time.Hour)))
	must0(s.CompleteWorkIQAttempt(ctx, probe, WorkIQSummary{Status: "failed", FinishedAt: mailT0.Add(5 * time.Hour), Counts: map[string]int{"disk_inventory_items": 1}}))
	got, ok := must2(s.WorkIQMailDiskEstimate(ctx, mailAcct))
	if !ok || got != want {
		t.Fatalf("estimate %+v %v, want %+v", got, ok, want)
	}
	if _, ok := must2(s.WorkIQMailDiskEstimate(ctx, "outlook/Other")); ok {
		t.Fatal("one account's estimate answered for another")
	}
	must(s.db.ExecContext(ctx, `update workiq_attempts set counts_json='{' where id=?`, attempt))
	if _, _, err := s.WorkIQMailDiskEstimate(ctx, mailAcct); err == nil {
		t.Fatal("a malformed saved estimate was accepted")
	}
	must0(s.Close())
	if _, _, err := s.WorkIQMailDiskEstimate(ctx, mailAcct); err == nil {
		t.Fatal("a closed archive answered")
	}
}

func TestWorkIQFreeBytesReadsTheArchiveVolume(t *testing.T) {
	db := filepath.Join(t.TempDir(), "archive.db")
	if free, err := FreeBytes(db); err != nil || free <= 0 {
		t.Fatal("free space of the archive's volume was not read", free, err)
	}
	if _, err := FreeBytes(filepath.Join(t.TempDir(), "missing", "archive.db")); err == nil {
		t.Fatal("a missing archive directory reported free space")
	}
}
