//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Environment variables of the calendar and Outlook checks (see AGENTS.md, "Real-cache acceptance
// tests"). Teams and Outlook have their own roots so the checks can run against the fixtures.
const (
	// teamsRootEnv is the Teams EBWebView directory; unset means the default location.
	teamsRootEnv = "M365CRAWL_TEAMS_ROOT"
	// outlookRootEnv is a directory laid out as <root>/<profile>/HxStore.hxd; unset skips the
	// Outlook checks.
	outlookRootEnv = "M365CRAWL_OUTLOOK_ROOT"
	// accountEnv picks the Teams account (<tenantId>/<userId>) the Outlook profile is linked to
	// when the archive holds several; unset means the account with the most live events.
	accountEnv = "M365CRAWL_ACCOUNT"
	// outlookProfileEnv picks the Outlook profile when the root holds several; unset means the
	// only one, or the one with the largest store.
	outlookProfileEnv = "M365CRAWL_OUTLOOK_PROFILE"
	// watchEnv turns on the ten-minute watch simulation of the cost check.
	watchEnv = "M365CRAWL_ACCEPTANCE_WATCH"
)

// calendarThresholds is the one place for every number the calendar and Outlook checks compare
// against. A measured value that differs from a plan's expectation is adjusted here, in one edit.
// Each field says whether a check fails on it or only reports against it.
var calendarThresholds = struct {
	// Teams (reporting only unless stated): the spike's counts on the work Mac's cache.
	SpikeJoinLinks, SpikeWithAttendees, SpikeWithBody, SpikeStructuredRooms int
	// ExpectAllDay, ExpectCancelled and ExpectDeclined are 0 until a real one exists: a different
	// count is logged as a new shape to record, it does not fail.
	ExpectAllDay, ExpectCancelled, ExpectDeclined int

	// Outlook container and guard counts (fail).
	// InvalidBlockRatioMax is the most blocks found that may fail a check (the spike saw about 0.2%).
	InvalidBlockRatioMax float64
	// ClassCountTolerance is how far an object count per class may be from the spike's (25%). The
	// spike's counts come from one copy of a mailbox that keeps changing: on the 2026-10-07 copy the
	// three classes were +0.7%, +5.8% and -12.3% from it while every other check passed. The check
	// is there to catch a reader that misparses a new Outlook build, which collapses or multiplies a
	// class, not ordinary growth or cache eviction.
	ClassCountTolerance float64
	// SpikeClassCounts are the spike's object counts for the classes the plan records; SpikeObjects
	// is its total. The plan names "the top five classes" but records only these three.
	SpikeClassCounts map[uint16]int
	// ClassCountRanges are classes checked against an inclusive range instead of the spike's count
	// (fail); they are left out of the tolerance check on SpikeClassCounts.
	ClassCountRanges map[uint16][2]int
	// Class55PerHeaderRange bounds the class 0x55 count per class 0x4f header object (fail, when
	// the store holds any header).
	Class55PerHeaderRange [2]float64
	SpikeObjects          int
	// UnwalkedFractionWant and UnwalkedFractionBand: the share of payload bytes that is not an
	// object (unwalked bytes plus recognized framing) is 9%, give or take the band.
	UnwalkedFractionWant, UnwalkedFractionBand float64

	// Outlook twin rate (fail): the share of the Teams account's live non-master events with an
	// Outlook event under the same key, and of its series with an Outlook series. The spike
	// measured 148 of 148 and 63 of 63; the counts are reported against SpikeTwinEvents and
	// SpikeTwinSeries.
	TwinRateMin, TwinSeriesRateMin   float64
	SpikeTwinEvents, SpikeTwinSeries int
	// ScheduleWindow is the last-modified distance inside which a schedule disagreement of two
	// copies is counted as one that a newer edit does not explain (reporting only).
	ScheduleWindow time.Duration

	// Version rule (fail): event ids found only in bytes the object walk does not cover.
	UnwalkedOnlyIDsMax int
	// EventClassPrefix is the 16-byte class id (32 hex characters) every Exchange global object id
	// starts with; the unwalked-bytes search looks for ids with it. It is checked against the walked
	// ids first, so a wrong value fails loudly instead of finding nothing.
	EventClassPrefix string

	// Cost budgets (fail): the plan's targets, assumed until this run measures them.
	StoreSizeMax    int64         // store file, bytes
	SnapshotWallMax time.Duration // copy plus scan
	RSSOverTeamsMax int64         // whole-run peak RSS over the Teams-only peak, bytes
	// WatchWindow and WatchSample size the ten-minute watch simulation (opt-in, reporting only).
	WatchWindow, WatchSample time.Duration
}{
	SpikeJoinLinks: 126, SpikeWithAttendees: 70, SpikeWithBody: 14, SpikeStructuredRooms: 7,
	ExpectAllDay: 0, ExpectCancelled: 0, ExpectDeclined: 0,

	InvalidBlockRatioMax: 0.005,
	ClassCountTolerance:  0.25,
	SpikeClassCounts:     map[uint16]int{0x71: 118767, 0x55: 49272, 0x6b: 8589},
	// Class 0x55 holds recipients and attendees, so it grows with the mailbox: a real store now
	// holds about 68,000 to 72,000 against the spike's 49,272. It is checked as a range and as a
	// ratio to the mail header class 0x4f (about 21 per header on that store), not as a fixed count.
	ClassCountRanges:      map[uint16][2]int{0x55: {45000, 110000}},
	Class55PerHeaderRange: [2]float64{8, 45},
	SpikeObjects:          264887,
	UnwalkedFractionWant:  0.09, UnwalkedFractionBand: 0.03,

	TwinRateMin: 1.0, TwinSeriesRateMin: 1.0,
	SpikeTwinEvents: 148, SpikeTwinSeries: 63,
	ScheduleWindow: 5 * time.Second,

	UnwalkedOnlyIDsMax: 0,
	EventClassPrefix:   "040000008200E00074C5B7101A82E008",

	StoreSizeMax:    100 << 20,
	SnapshotWallMax: 8 * time.Second,
	RSSOverTeamsMax: 150 << 20,
	WatchWindow:     10 * time.Minute, WatchSample: 15 * time.Second,
}

// calendarTeamsRoot is the Teams root of the calendar checks.
func calendarTeamsRoot() string {
	if r := os.Getenv(teamsRootEnv); r != "" {
		return r
	}
	return teamsdesktop.DefaultRoot()
}

// requireOutlook skips unless the real-cache switch is on and an Outlook root is given.
func requireOutlook(t *testing.T) string {
	t.Helper()
	requireReal(t)
	root := os.Getenv(outlookRootEnv)
	if root == "" {
		t.Skip("set " + outlookRootEnv + " to a directory laid out as <root>/<profile>/HxStore.hxd to run the Outlook checks")
	}
	fi, err := os.Stat(root) //nolint:gosec // the root is the operator's own environment variable, read only
	if err != nil || !fi.IsDir() {
		t.Fatalf("%s is not a readable directory", outlookRootEnv)
	}
	return root
}

// --- scratch archives --------------------------------------------------------

var (
	calMu       sync.Mutex
	calCleanups []func()
)

// calendarScratch makes a private scratch directory that TestMain removes (flat: files, then the
// directory, never recursively).
func calendarScratch(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "m365crawl-acceptance-calendar-")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		setCurrentUserAndSystemOnly(t, dir)
	}
	calMu.Lock()
	calCleanups = append(calCleanups, func() { removeFlat(dir) })
	calMu.Unlock()
	return dir
}

func removeFlat(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	_ = os.Remove(dir)
}

// cleanupCalendar is called by TestMain.
func cleanupCalendar() {
	calMu.Lock()
	defer calMu.Unlock()
	for _, f := range calCleanups {
		f()
	}
}

// teamsRun is the shared Teams-only sync of the calendar checks.
type teamsRun struct {
	db   string
	rep  syncer.Report
	wall time.Duration
	// before and after are the process usage around the sync: the peak RSS after is the Teams-only
	// peak the Outlook budget is measured from. When before already equals it, earlier tests of
	// this process set the peak, and the budget is lenient (run make acceptance-calendar).
	before, after usage
}

var (
	teamsOnce sync.Once
	teamsShr  teamsRun
	teamsErr  error
)

// calendarArchive syncs the Teams root (Teams only, Outlook off) into a scratch archive once and
// shares it between the Teams calendar checks and the Outlook checks.
func calendarArchive(t *testing.T) teamsRun {
	t.Helper()
	requireReal(t)
	teamsOnce.Do(func() {
		dir := calendarScratch(t)
		teamsShr.db = filepath.Join(dir, "archive.db")
		teamsShr.before = processUsage()
		start := time.Now()
		teamsShr.rep, _, teamsErr = syncer.Run(context.Background(), syncer.Options{Root: calendarTeamsRoot(), DBPath: teamsShr.db})
		teamsShr.wall = time.Since(start)
		teamsShr.after = processUsage()
	})
	if teamsErr != nil {
		t.Fatalf("sync of the Teams root: %v", teamsErr)
	}
	return teamsShr
}

// --- reading the archive -----------------------------------------------------

func openArchive(t *testing.T, db string) *store.Store {
	t.Helper()
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// rowsOf runs a read query. The result may hold content: callers count and compare, never log it.
func rowsOf(t *testing.T, st *store.Store, q string) [][]any {
	t.Helper()
	_, rows, _, err := st.SQL(context.Background(), q, acceptanceRowLimit)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	return rows
}

func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return ""
}

func asInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// parseTime reads the archive's timestamps (RFC 3339 with optional fractions).
func parseTime(s string) (time.Time, bool) {
	ts, err := time.Parse(time.RFC3339Nano, s)
	return ts, err == nil
}

// --- Outlook profile ---------------------------------------------------------

// outlookStore is the profile the Outlook checks read: the one named by outlookProfileEnv, the
// only one there is, or the one with the largest store.
func outlookStore(t *testing.T, root string) outlookdesktop.Profile {
	t.Helper()
	profiles, _, _, err := outlookdesktop.Discover(root)
	if err != nil {
		t.Fatalf("discover Outlook profiles: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatalf("no Outlook profile under %s (want <root>/<profile>/HxStore.hxd)", outlookRootEnv)
	}
	if want := os.Getenv(outlookProfileEnv); want != "" {
		for _, p := range profiles {
			if p.Name == want {
				return p
			}
		}
		t.Fatalf("%s names no profile under the root (%d profiles)", outlookProfileEnv, len(profiles))
	}
	best, bestSize := profiles[0], int64(-1)
	for _, p := range profiles {
		if fi, err := os.Stat(p.StorePath); err == nil && fi.Size() > bestSize {
			best, bestSize = p, fi.Size()
		}
	}
	if len(profiles) > 1 {
		t.Logf("%d Outlook profiles; reading the one with the largest store (set %s to choose)", len(profiles), outlookProfileEnv)
	}
	return best
}
