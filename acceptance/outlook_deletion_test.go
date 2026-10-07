//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ourostack/teamscrawl/internal/syncer"
)

// TestRealOutlookDeletionProbe pins what the deletion experiment found (docs/outlook-store.md,
// "Deleted events"): Outlook leaves a deleted event in its store, unchanged, until it compacts, and then
// drops it, and a sync marks the event gone only after it has dropped it. It needs copies of one real store
// taken around a deletion of test appointments, each laid out as <root>/<profile>/HxStore.hxd:
//
//	TEAMSCRAWL_OUTLOOK_PROBE_BEFORE   a copy taken before the deletion
//	TEAMSCRAWL_OUTLOOK_PROBE_LINGER   optional: a copy taken after the deletion, before compaction
//	TEAMSCRAWL_OUTLOOK_PROBE_AFTER    a copy taken after compaction
//	TEAMSCRAWL_OUTLOOK_PROBE_IDS      comma-separated distinguishing suffixes of the lower-case ids of the
//	                                  deleted events (the ids are the experiment's own appointments)
//
// It skips unless the first, third and fourth are set. The archive is a scratch one; the copies are
// only read. It logs counts only. The population check is the second half: no event other than the
// probes may be marked gone by the pass from the first copy to the last.
func TestRealOutlookDeletionProbe(t *testing.T) {
	requireReal(t)
	before, linger, after := os.Getenv("TEAMSCRAWL_OUTLOOK_PROBE_BEFORE"), os.Getenv("TEAMSCRAWL_OUTLOOK_PROBE_LINGER"), os.Getenv("TEAMSCRAWL_OUTLOOK_PROBE_AFTER")
	var ids []string
	for _, id := range strings.Split(os.Getenv("TEAMSCRAWL_OUTLOOK_PROBE_IDS"), ",") {
		if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
			ids = append(ids, id)
		}
	}
	if before == "" || after == "" || len(ids) == 0 {
		t.Skip("set TEAMSCRAWL_OUTLOOK_PROBE_BEFORE, TEAMSCRAWL_OUTLOOK_PROBE_AFTER and TEAMSCRAWL_OUTLOOK_PROBE_IDS (and optionally TEAMSCRAWL_OUTLOOK_PROBE_LINGER)")
	}
	dir := calendarScratch(t)
	db := filepath.Join(dir, "probe.db")
	step := func(name, root string) int {
		t.Helper()
		rep, _, err := syncer.Run(context.Background(), syncer.Options{
			Root: filepath.Join(dir, "no-teams"), DBPath: db, FullRead: true,
			OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
		})
		if err != nil {
			t.Fatalf("%s: sync failed: %v", name, err)
		}
		t.Logf("%s: status %s, calendar events %+v, gone %d", name, rep.Status, rep.Calendar.Events, rep.Calendar.Gone)
		return rep.Calendar.Gone
	}
	probeRows := func(removed bool) int {
		st := openArchive(t, db)
		n := 0
		for _, id := range ids {
			q := "select count(*) from calendar_source_events where source='outlook' and removed_at is " + map[bool]string{true: "not null", false: "null"}[removed] + " and source_id like '%" + id + "'"
			n += asInt(rowsOf(t, st, q)[0][0])
		}
		return n
	}
	removedRows := func() int {
		return asInt(rowsOf(t, openArchive(t, db), "select count(*) from calendar_source_events where source='outlook' and removed_at is not null")[0][0])
	}

	if g := step("before", before); g != 0 {
		t.Errorf("the first read marked %d events gone", g)
	}
	if n := probeRows(false); n != len(ids) {
		t.Fatalf("%d of %d probe events are live after the copy taken before the deletion", n, len(ids))
	}
	if linger != "" {
		if g := step("linger", linger); g != 0 || probeRows(false) != len(ids) {
			t.Errorf("a copy taken after the deletion and before compaction marked %d gone, %d probes live of %d: Outlook has not dropped them yet, so the signal must not fire", g, probeRows(false), len(ids))
		}
	}
	g := step("after", after)
	if n := probeRows(true); n != len(ids) || g != len(ids) {
		t.Errorf("after compaction %d of %d probe events are marked gone and the run counted %d", n, len(ids), g)
	}
	if n := removedRows(); n != len(ids) {
		t.Errorf("%d events are marked gone, want exactly the %d probes", n, len(ids))
	}
	t.Logf("probes %d, marked gone %d, all rows marked gone %d", len(ids), probeRows(true), removedRows())
}
