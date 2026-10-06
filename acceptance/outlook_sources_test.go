//go:build acceptance

package acceptance

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/syncer"
)

// (1, continued) The Outlook row of `calendar sources` after the syncs: what the command would say
// about the store. unknown_layouts must be empty, the census must have been taken, and the last
// check must be recorded. Only counts, field names and statuses are logged.
func TestRealOutlookSourcesRow(t *testing.T) {
	outlookSyncs(t)
	st := openArchive(t, calendarArchive(t).db)
	got, err := st.CalendarSources(context.Background(), store.CalendarSourcesFilter{Now: time.Now().UTC(), ReadInterval: syncer.OutlookMinReadInterval})
	if err != nil {
		t.Fatal("reading calendar sources failed")
	}
	rows := 0
	for _, r := range got.Rows {
		if r.Outlook == nil {
			continue
		}
		rows++
		o := r.Outlook
		t.Logf("outlook row: status %s, link %s, events live %d, unknown_layouts %d, census_as_of set %v, last_checked_at set %v, last_read_at set %v, failure %v",
			o.Status, r.Link, r.EventsLive, len(o.UnknownLayouts), !o.CensusAsOf.IsZero(), !o.LastCheckedAt.IsZero(), !o.LastReadAt.IsZero(), o.Failure != nil)
		if o.BlocksRatio != nil {
			t.Logf("  invalid block ratio of the last good read: %.3f%%; unmapped value counts: %s", 100**o.BlocksRatio, countsLine(o.UnmappedValues))
		}
		if o.Status != store.SourceOK {
			t.Errorf("outlook status is %s, want %s", o.Status, store.SourceOK)
		}
		if len(o.UnknownLayouts) != 0 {
			t.Errorf("unknown_layouts holds %d pairs, want none", len(o.UnknownLayouts))
		}
		if o.CensusAsOf.IsZero() || o.LastCheckedAt.IsZero() {
			t.Errorf("census_as_of or last_checked_at is not set after a read and a check")
		}
	}
	if rows == 0 {
		t.Error("calendar sources has no Outlook row after the Outlook syncs")
	}
}
