//go:build acceptance

package acceptance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/outlookdesktop"
	"github.com/ourostack/teamscrawl/internal/syncer"
)

// The syncs of the Outlook checks. They run once, in a fixed order, and every Outlook check reads
// their results: the Teams-only sync of calendarArchive (the baseline), the first Outlook sync with
// the explicit account link, an unchanged sync, and a forced Outlook-only re-read. The Outlook
// store is only ever opened by the sync's own copy; this file lists the profile directory before and
// after and stats the original, and writes nothing there.

// errCode names a failure by its error code, never by its message (a message may carry a path).
func errCode(err error) string {
	var c *errs.Coded
	if errors.As(err, &c) {
		return c.Code
	}
	return "unclassified"
}

// syncMeasure is one sync of the Outlook run and what it cost.
type syncMeasure struct {
	rep        syncer.Report
	wall, cpu  time.Duration
	peakBefore int64 // process peak RSS before and after the sync
	peakAfter  int64
	ok         bool // usage was available
}

// outlookStatuses lists the status of each Outlook source of a report.
func (m syncMeasure) outlookStatuses() map[string]int {
	out := map[string]int{}
	for _, s := range m.rep.Sources {
		if strings.HasPrefix(s.Source, "outlook") {
			out[s.Status]++
		}
	}
	return out
}

// fileFacts is what the safety check compares of one file before and after.
type fileFacts struct {
	exists bool
	info   os.FileInfo
}

func factsOf(path string) fileFacts {
	fi, err := os.Stat(path)
	return fileFacts{exists: err == nil, info: fi}
}

// rowState is an Outlook event row as the append-only check compares it: first_seen_at by key.
type rowState map[string]string

type outlookRunData struct {
	root    string
	profile outlookdesktop.Profile
	account string // the Teams account the profile is linked to

	teams                syncMeasure // the Teams-only baseline (calendarArchive), wall and peak only
	first, again, forced syncMeasure
	forcedErr            string // error code of the forced re-read, empty when it ran
	rowsFirst, rowsAgain rowState
	rowsForced           rowState

	rootBefore, rootAfter       []string
	profileBefore, profileAfter []string
	lockBefore, lockAfter       fileFacts
	storeBefore, storeAfter     fileFacts
	snapsBefore, snapsAfter     int
}

var (
	outlookOnce sync.Once
	outlookRun  outlookRunData
	outlookErr  string
)

const snapshotDirPrefix = "teamscrawl-snapshot-"

func namesIn(dir string) []string {
	ents, _ := os.ReadDir(dir)
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func countSnapshotDirs() int {
	n := 0
	for _, name := range namesIn(os.TempDir()) {
		if strings.HasPrefix(name, snapshotDirPrefix) {
			n++
		}
	}
	return n
}

// pickAccount is the Teams account the Outlook profile is linked to: accountEnv, or the account
// with the most live events.
func pickAccount(t *testing.T, db string) string {
	t.Helper()
	if a := os.Getenv(accountEnv); a != "" {
		return a
	}
	st := openArchive(t, db)
	rows := rowsOf(t, st, `select account_id, count(*) from calendar_source_events where source='teams' and removed_at is null group by account_id order by 2 desc, 1`)
	if len(rows) == 0 {
		t.Fatalf("the archive holds no live Teams events, so there is no Teams account to link the Outlook profile to (set %s)", accountEnv)
	}
	if len(rows) > 1 {
		t.Logf("%d Teams accounts have events; linking the one with the most (set %s to choose)", len(rows), accountEnv)
	}
	return asString(rows[0][0])
}

// measure runs one sync. A failure comes back as a message (error code only) so the shared run can
// record it for every later check instead of ending the first one's goroutine.
func measure(o syncer.Options) (syncMeasure, string) {
	before := processUsage()
	start := time.Now()
	rep, _, err := syncer.Run(context.Background(), o)
	m := syncMeasure{rep: rep, wall: time.Since(start)}
	after := processUsage()
	m.ok = before.ok && after.ok
	m.cpu, m.peakBefore, m.peakAfter = after.cpu-before.cpu, before.peakRSS, after.peakRSS
	if err != nil {
		return m, "sync failed with code " + errCode(err) + " (status " + rep.Status + ")"
	}
	return m, ""
}

func outlookRows(t *testing.T, db string) rowState {
	t.Helper()
	st := openArchive(t, db)
	out := rowState{}
	for _, r := range rowsOf(t, st, `select account_id||'|'||event_key, first_seen_at from calendar_source_events where source='outlook'`) {
		out[asString(r[0])] = asString(r[1])
	}
	return out
}

// outlookSyncs runs the syncs once and returns what they measured. It must be the first thing an
// Outlook check does, so the peak RSS it records is not raised by a check that read the store.
func outlookSyncs(t *testing.T) outlookRunData {
	t.Helper()
	root := requireOutlook(t)
	teams := calendarArchive(t)
	outlookOnce.Do(func() {
		finished := false
		defer func() {
			if !finished && outlookErr == "" {
				outlookErr = "the shared Outlook syncs did not finish (see the first failing Outlook check)"
			}
		}()
		d := &outlookRun
		d.root = root
		d.profile = outlookStore(t, root)
		d.account = pickAccount(t, teams.db)
		d.teams = syncMeasure{rep: teams.rep, wall: teams.wall, peakBefore: teams.before.peakRSS, peakAfter: teams.after.peakRSS, ok: teams.after.ok}

		d.rootBefore, d.profileBefore = namesIn(root), namesIn(d.profile.Dir)
		d.lockBefore = factsOf(filepath.Join(d.profile.Dir, "HxStore.lock"))
		d.storeBefore = factsOf(d.profile.StorePath)
		d.snapsBefore = countSnapshotDirs()

		opts := syncer.Options{
			Root: calendarTeamsRoot(), DBPath: teams.db,
			OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
			OutlookLink: d.account, OutlookLinkProfile: d.profile.Name,
		}
		if d.first, outlookErr = measure(opts); outlookErr != "" {
			return
		}
		d.rowsFirst = outlookRows(t, teams.db)
		if d.again, outlookErr = measure(opts); outlookErr != "" {
			return
		}
		d.rowsAgain = outlookRows(t, teams.db)

		// A forced read of the Outlook store alone (no Teams root, so Teams does not run) gives the
		// append-only check a real second read even when the store has not changed.
		forced := syncer.Options{
			Root: filepath.Join(calendarScratch(t), "no-teams"), DBPath: teams.db, FullRead: true,
			OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
		}
		before := processUsage()
		start := time.Now()
		rep, _, err := syncer.Run(context.Background(), forced)
		d.forced = syncMeasure{rep: rep, wall: time.Since(start), peakBefore: before.peakRSS, peakAfter: processUsage().peakRSS}
		if err != nil {
			d.forcedErr = errCode(err)
		} else {
			d.rowsForced = outlookRows(t, teams.db)
		}

		d.rootAfter, d.profileAfter = namesIn(root), namesIn(d.profile.Dir)
		d.lockAfter = factsOf(filepath.Join(d.profile.Dir, "HxStore.lock"))
		d.storeAfter = factsOf(d.profile.StorePath)
		d.snapsAfter = countSnapshotDirs()
		finished = true
	})
	if outlookErr != "" {
		t.Fatal(outlookErr)
	}
	return outlookRun
}

// addedRemoved is the names in after that are not in before, and the other way round.
func addedRemoved(before, after []string) (added, removed []string) {
	for _, n := range after {
		if !slices.Contains(before, n) {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !slices.Contains(after, n) {
			removed = append(removed, n)
		}
	}
	return added, removed
}
