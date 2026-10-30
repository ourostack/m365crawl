package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// Per-item disk defaults for whole-account WorkIQ mail sync, from the live run on a real mailbox:
// staging 15,502 items grew the archive by 2.2 GB (about 142 KB an item, 2.5 times their 57 KB of
// page JSON). Admission writes each item again as canonical mail, search text and its scrubbed
// server copy, estimated at twice its page JSON. A run's own measurements only raise them.
const (
	WorkIQAcquireBytesPerItem = 150 << 10
	WorkIQAdmitBytesPerItem   = 120 << 10
	workIQPageFloor           = 16 << 20
	workIQTransactionReserve  = 256 << 20
	workIQHeadroom            = 1 << 30
	// WorkIQMailItemsPerPage and WorkIQMailPagesPerMinute are the live delta page size and pace.
	WorkIQMailItemsPerPage   = 10
	WorkIQMailPagesPerMinute = 31
)

// WorkIQMailDisk is what one Outlook account's whole-account WorkIQ mail sync still has to write.
// Staged items stay on disk until the whole account is admitted, so every item not yet admitted,
// fetched or not, keeps its admission reserve.
type WorkIQMailDisk struct {
	InventoryItems      int64 // items the known folder inventory reports
	StagedItems         int64 // items in staged, not yet admitted message pages
	StagedBytes         int64 // their page JSON
	LargestPage         int64 // the largest staged page
	AcquireBytesPerItem int64 // archive growth per staged item measured by a run; 0 when unmeasured
}

// Remaining is the items still to fetch.
func (d WorkIQMailDisk) Remaining() int64 { return max(0, d.InventoryItems-d.StagedItems) }

// Pages is the delta pages still to fetch at the live page size.
func (d WorkIQMailDisk) Pages() int64 {
	return (d.Remaining() + WorkIQMailItemsPerPage - 1) / WorkIQMailItemsPerPage
}

// Minutes is the time those pages take at the live pace.
func (d WorkIQMailDisk) Minutes() int64 {
	return (d.Pages() + WorkIQMailPagesPerMinute - 1) / WorkIQMailPagesPerMinute
}

// Need is the free space the rest of the sync needs: acquisition of the items still to fetch,
// admission of every item not yet admitted, a quarter more on both, the largest write transaction
// twice over plus a transaction reserve, and a gigabyte of headroom.
func (d WorkIQMailDisk) Need() int64 {
	acquire := max(WorkIQAcquireBytesPerItem, d.AcquireBytesPerItem)
	admit := int64(WorkIQAdmitBytesPerItem)
	if d.StagedItems > 0 {
		admit = max(admit, 2*d.StagedBytes/d.StagedItems)
	}
	remaining := d.Remaining()
	unadmitted := max(d.InventoryItems, d.StagedItems)
	return (remaining*acquire+unadmitted*admit)*5/4 + 2*max(workIQPageFloor, d.LargestPage) + workIQTransactionReserve + workIQHeadroom
}

// Counts is the estimate as attempt counts, so a finished run keeps it.
func (d WorkIQMailDisk) Counts() map[string]int {
	return map[string]int{"disk_inventory_items": int(d.InventoryItems), "disk_staged_items": int(d.StagedItems),
		"disk_staged_bytes": int(d.StagedBytes), "disk_largest_page_bytes": int(d.LargestPage),
		"disk_acquire_bytes_per_item": int(d.AcquireBytesPerItem)}
}

// WorkIQMailDiskFacts reads the account's known folder inventory and its staged, not yet admitted
// message pages. It parses every such page, so a sync calls it once and then counts its own pages.
func (s *Store) WorkIQMailDiskFacts(ctx context.Context, account string) (WorkIQMailDisk, error) {
	var d WorkIQMailDisk
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return d, err
	}
	err = s.db.QueryRowContext(ctx, `select coalesce(sum(total_items),0) from workiq_folders where account_id=?`, account).Scan(&d.InventoryItems)
	if err == nil {
		err = s.db.QueryRowContext(ctx, `select coalesce(sum(json_array_length(p.data_json,'$.value')),0),
coalesce(sum(length(cast(p.data_json as blob))),0), coalesce(max(length(cast(p.data_json as blob))),0)
from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
where f.family='mail' and f.account_id=? and f.retired=0 and f.resource like '%/messages/delta' and p.state='staged'`,
			account).Scan(&d.StagedItems, &d.StagedBytes, &d.LargestPage)
	}
	return d, err
}

// WorkIQMailDiskEstimate is the estimate the account's latest finished whole-account mail sync kept.
func (s *Store) WorkIQMailDiskEstimate(ctx context.Context, account string) (WorkIQMailDisk, bool, error) {
	var d WorkIQMailDisk
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return d, false, err
	}
	var raw string
	var counts map[string]int64
	err = s.db.QueryRowContext(ctx, `select counts_json from workiq_attempts where family='mail' and kind='sync' and account_id=?
and status<>'open' and (json_valid(counts_json)=0 or json_type(counts_json,'$.disk_inventory_items') is not null)
order by id desc limit 1`, account).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return d, false, nil
	case err == nil:
		err = json.Unmarshal([]byte(raw), &counts)
	}
	if err != nil {
		return d, false, err
	}
	return WorkIQMailDisk{InventoryItems: counts["disk_inventory_items"], StagedItems: counts["disk_staged_items"],
		StagedBytes: counts["disk_staged_bytes"], LargestPage: counts["disk_largest_page_bytes"],
		AcquireBytesPerItem: counts["disk_acquire_bytes_per_item"]}, true, nil
}
