package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/hxstore/hxbuild"
	"github.com/ourostack/m365crawl/internal/outlookcal"
	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/store"
)

const outlookFixture = "../../testdata/outlook-fixture"

// outlookRoot is a profiles directory with one profile, "Main", holding the named fixture store.
func outlookRoot(t *testing.T, store string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Main"), 0o750); err != nil {
		t.Fatal(err)
	}
	putOutlookStore(t, root, store)
	return root
}

func putOutlookStore(t *testing.T, root, store string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outlookFixture, store)) //nolint:gosec // a committed fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

func outlookOpts(db, root string) Options {
	return Options{Root: fixtureRoot, DBPath: db, OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1}
}

func count(t *testing.T, db, q string) int {
	t.Helper()
	var n int
	if err := openRaw(t, db).QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sourceKeyed(t *testing.T, r Report, key string) SourceReport {
	t.Helper()
	for _, s := range r.Sources {
		if s.Source == key {
			return s
		}
	}
	t.Fatalf("no source %s in %+v", key, r.Sources)
	return SourceReport{}
}

// The fixture's Teams and Outlook stores fill both sources' rows; a second sync reads nothing; a
// raised mapper version reads the unchanged store again; Outlook is off unless it is switched on.
func TestSyncOutlookFixture(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")

	off := Options{Root: fixtureRoot, DBPath: db}
	if r, _ := run(t, off); len(r.Sources) == 0 || count(t, db, `select count(*) from calendar_source_events where source='outlook'`) != 0 {
		t.Fatal("Outlook ran without being switched on")
	}

	r, _ := run(t, outlookOpts(db, root))
	o := sourceKeyed(t, r, "outlook|Main")
	if o.Status != StatusOK || o.Counts.Calendar.Events.Inserted != 10 || o.Accounts[0] != "outlook/Main" {
		t.Fatalf("%+v %+v", o, *o.Counts)
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='teams'`); n == 0 {
		t.Fatal("no Teams rows")
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is null`); n != 10 {
		t.Fatalf("%d Outlook rows", n)
	}
	if count(t, db, `select count(*) from calendar_covered_days where source='outlook'`) == 0 || count(t, db, `select count(*) from sync_runs where source='outlook|Main'`) != 1 {
		t.Fatal("covered days or the run row are missing")
	}

	if r, _ := run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("second sync: %+v", r.Sources)
	}

	old := outlookMapperVersion
	outlookMapperVersion = old + 1
	t.Cleanup(func() { outlookMapperVersion = old })
	if r, _ := run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusOK || sourceKeyed(t, r, "outlook|Main").Counts.Calendar.Events.Updated != 10 {
		t.Fatalf("a bumped mapper version did not re-derive: %+v", sourceKeyed(t, r, "outlook|Main"))
	}
}

func TestSyncOutlookMinReadInterval(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	now := time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC)
	old := outlookNow
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = old })
	o := outlookOpts(db, root)
	o.OutlookMinReadInterval = 0 // the default, five minutes
	run(t, o)
	now = now.Add(time.Minute)
	r, _ := run(t, o)
	if s := sourceKeyed(t, r, "outlook|Main"); s.Status != StatusSkippedInterval || s.NextReadAfter == nil || !s.NextReadAfter.Equal(now.Add(4*time.Minute)) || r.Status != StatusUnchanged {
		t.Fatalf("%+v %s", s, r.Status)
	}
	// The skip is remembered for `calendar sources`, and the next sync that looks at the store forgets it.
	if count(t, db, `select count(*) from meta where key='outlook_skipped:Main'`) != 1 {
		t.Fatal("the skip was not recorded")
	}
	now = now.Add(OutlookMinReadInterval)
	if r, _ = run(t, o); sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("%+v", r.Sources)
	}
	if count(t, db, `select count(*) from meta where key='outlook_skipped:Main'`) != 0 {
		t.Fatal("the skip was not cleared")
	}
}

// What a read saw beyond the events is kept with it: the census of the store, numbers only.
func TestOutlookReadCensus(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	run(t, outlookOpts(db, outlookRoot(t, "HxStore.hxd")))
	if count(t, db, `select count(*) from meta where key='outlook_read:outlook/Main'`) != 1 {
		t.Fatal("the census was not kept")
	}
	res := outlookcal.Result{UnknownLayouts: []outlookcal.PairCount{{Class: 0x6b, Tag: 0x456, Count: 2}}}
	res.Stats.BlocksFound, res.Stats.Rejected = 10, map[string]int{"crc": 1, "len": 2}
	res.Notes.ShowAsUnmapped, res.Notes.ResponseUnmapped, res.Notes.EventTypeUnknown = 4, 5, 6
	got := outlookRead(res)
	if got.BlocksFound != 10 || got.BlocksInvalid != 3 || len(got.UnknownLayouts) != 1 || got.UnknownLayouts[0].Tag != 0x456 ||
		got.UnmappedValues["show_as"] != 4 || got.UnmappedValues["response"] != 5 || got.UnmappedValues["event_type"] != 6 {
		t.Fatalf("%+v", got)
	}
	if got := outlookRead(outlookcal.Result{}); got.UnmappedValues != nil || got.UnknownLayouts != nil {
		t.Fatalf("%+v", got)
	}
}

// A store that cannot be read fails Outlook alone: Teams commits, the run is partial, and nothing
// of Outlook is kept. Fixing the store afterwards reads it.
func TestSyncOutlookFailureIsIsolated(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "store-version-j.hxd")
	rep, _, err := Run(context.Background(), outlookOpts(db, root))
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodePartialSync || rep.Status != StatusPartial {
		t.Fatalf("%v %s", err, rep.Status)
	}
	if s := sourceKeyed(t, rep, "outlook|Main"); s.Status != StatusFailed || s.Error == nil || s.Error.Code != "outlook_store_version" {
		t.Fatalf("%+v", s)
	}
	if count(t, db, `select count(*) from calendar_source_events where source='outlook'`) != 0 || count(t, db, `select count(*) from calendar_source_events where source='teams'`) == 0 {
		t.Fatal("rows are not isolated")
	}
	putOutlookStore(t, root, "HxStore.hxd")
	if r, _ := run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
		t.Fatalf("%+v", r.Sources)
	}
}

// With Teams not installed Outlook runs alone; a Teams account filter leaves Outlook out; a root
// that does not exist fails the Outlook source and not the run's Teams side.
func TestSyncOutlookAlone(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := outlookOpts(db, root)
	o.Root = t.TempDir()
	if r, _ := run(t, o); r.Status != StatusOK || len(r.Sources) != 1 {
		t.Fatalf("%s %+v", r.Status, r.Sources)
	}
	o.OutlookRoot = filepath.Join(root, "missing")
	if _, _, err := Run(context.Background(), o); err == nil {
		t.Fatal("a missing Outlook root with no Teams is not an error")
	}
}

// outlookRunErr runs a sync with Outlook only and returns the Outlook source's failure code.
func outlookFailure(t *testing.T, o Options) string {
	t.Helper()
	rep, _, err := Run(context.Background(), o)
	if err == nil {
		t.Fatal("no error")
	}
	return sourceKeyed(t, rep, "outlook|Main").Error.Code
}

// Each way an Outlook source can fail comes back as that source's coded failure; the Teams side is
// untouched, and nothing of Outlook is kept.
func TestSyncOutlookFailures(t *testing.T) {
	isolateTmp(t)
	root := outlookRoot(t, "HxStore.hxd")
	broken := func(name, ddl string) {
		t.Run(name, func(t *testing.T) {
			db := archiveWith(t, ddl)
			if code := outlookFailure(t, outlookOpts(db, root)); code != errs.CodeDBError {
				t.Fatal(code)
			}
		})
	}
	abort := func(table, when string) string {
		return `create trigger boom before insert on ` + table + ` when ` + when + ` begin select raise(abort, 'injected'); end;`
	}
	broken("last attempt", abort("meta", `new.key like 'outlook_last_attempt:%'`))
	broken("commit", abort("calendar_source_events", `new.source='outlook'`))

	t.Run("state", func(t *testing.T) {
		db := newDB(t)
		run(t, outlookOpts(db, root))
		if _, err := openRaw(t, db).Exec(`update sync_runs set omissions_json='{' where source='outlook|Main'`); err != nil {
			t.Fatal(err)
		}
		if code := outlookFailure(t, outlookOpts(db, root)); code != errs.CodeDBError {
			t.Fatal(code)
		}
	})
	t.Run("unchanged run record", func(t *testing.T) {
		db := newDB(t)
		run(t, outlookOpts(db, root))
		raw := openRaw(t, db)
		if _, err := raw.Exec(abort("sync_runs", `new.status='unchanged' and new.source like 'outlook|%'`)); err != nil {
			t.Fatal(err)
		}
		if code := outlookFailure(t, outlookOpts(db, root)); code != errs.CodeDBError {
			t.Fatal(code)
		}
	})
	seam := func(name string, set func(), want string) {
		t.Run(name, func(t *testing.T) {
			set()
			if code := outlookFailure(t, outlookOpts(newDB(t), root)); code != want {
				t.Fatalf("%s, want %s", code, want)
			}
		})
	}
	restore := func() func() {
		d, s, n := outlookDiscover, outlookSnapshot, outlookNow
		return func() { outlookDiscover, outlookSnapshot, outlookNow = d, s, n }
	}()
	t.Cleanup(restore)
	seam("no store file", func() {
		outlookDiscover = func(string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
			return []outlookdesktop.Profile{{Name: "Main", StorePath: filepath.Join(root, "gone", "HxStore.hxd")}}, nil, nil, nil
		}
	}, errs.CodeInternal)
	seam("copy fails", func() {
		restore()
		outlookSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
			return outlookdesktop.Info{}, func() {}, errs.SnapshotInconsistent("busy")
		}
	}, errs.CodeSnapshotInconsistent)
	seam("copy missing", func() {
		outlookSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
			return outlookdesktop.Info{Path: filepath.Join(root, "gone")}, func() {}, nil
		}
	}, errs.CodeInternal)
	seam("copy unreadable", func() { // a directory opens and cannot be read
		outlookSnapshot = func(context.Context, string) (outlookdesktop.Info, func(), error) {
			return outlookdesktop.Info{Path: root, Size: 1 << 20}, func() {}, nil
		}
	}, errs.CodeInternal)
	seam("panic", func() {
		restore()
		outlookNow = func() time.Time { panic("boom") }
	}, errs.CodeInternal)
}

func TestSyncOutlookDefaultRootFailure(t *testing.T) {
	isolateTmp(t)
	old := outlookDefaultRoot
	outlookDefaultRoot = func() (string, error) { return "", errs.Internal(errors.New("no home")) }
	t.Cleanup(func() { outlookDefaultRoot = old })
	rep, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: newDB(t), OutlookEnabled: true})
	if err == nil || sourceKeyed(t, rep, "outlook").Error.Code != errs.CodeInternal || rep.Status != StatusPartial {
		t.Fatalf("%v %+v", err, rep.Sources)
	}
}

func TestOutlookOmissions(t *testing.T) {
	res := outlookcal.Result{Losses: []outlookcal.Loss{{Code: outlookcal.CodeEventUnmapped, Count: 2}, {Code: outlookcal.CodeBlocksDamaged, Count: 5}}}
	res.Notes.AllDayUnaligned = 3
	cal := store.CalendarResult{Omissions: map[string]int{store.OmitCalendarRefused: 1}}
	got := outlookOmissions(res, cal)
	want := map[string]int{store.OmitCalendarUnmapped: 2, outlookcal.CodeBlocksDamaged: 5, store.OmitCalendarRefused: 1, store.OmitCalendarAllDayUnaligned: 3}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%v", got)
		}
	}
	if outlookOmissions(outlookcal.Result{}, store.CalendarResult{}) != nil {
		t.Fatal("no losses is nil")
	}
}

// A run that reads nothing still reports what the last read could not use.
func TestSyncOutlookUnchangedKeepsOmissions(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	run(t, outlookOpts(db, root))
	if _, err := openRaw(t, db).Exec(`update sync_runs set omissions_json='{"calendar_unmapped":2}' where source='outlook|Main'`); err != nil {
		t.Fatal(err)
	}
	r, _ := run(t, outlookOpts(db, root))
	if s := sourceKeyed(t, r, "outlook|Main"); s.Status != StatusOmissions || s.Omissions["calendar_unmapped"] != 2 || r.Status != StatusOmissions {
		t.Fatalf("%+v %s", s, r.Status)
	}
}

// Damaged blocks are a counted loss: the events that read are kept, the run is ok_with_omissions.
func TestSyncOutlookDamagedBlocksAreALoss(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	r, _ := run(t, outlookOpts(db, outlookRoot(t, "store-damaged-blocks.hxd")))
	if s := sourceKeyed(t, r, "outlook|Main"); s.Status != StatusOmissions || s.Omissions[outlookcal.CodeBlocksDamaged] == 0 {
		t.Fatalf("%+v", s)
	}
}

func fakeClock(t *testing.T, at time.Time) *time.Time {
	t.Helper()
	now := at
	old := outlookNow
	outlookNow = func() time.Time { return now }
	t.Cleanup(func() { outlookNow = old })
	return &now
}

func defaultInterval(db, root string) Options {
	o := outlookOpts(db, root)
	o.OutlookMinReadInterval = 0
	return o
}

// A skip after a failed read is that failure: the source stays failed and the run partial, until
// a read succeeds.
func TestSyncOutlookSkipAfterFailureReportsIt(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "store-version-j.hxd")
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	for i := 0; i < 2; i++ {
		rep, _, err := Run(context.Background(), defaultInterval(db, root))
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != errs.CodePartialSync || rep.Status != StatusPartial {
			t.Fatalf("run %d: %v %s", i, err, rep.Status)
		}
		if s := sourceKeyed(t, rep, "outlook|Main"); s.Status != StatusFailed || s.Error.Code != "outlook_store_version" {
			t.Fatalf("run %d: %+v", i, s)
		}
		*now = now.Add(time.Minute) // the second run is a skip, and still the failure
	}
	putOutlookStore(t, root, "HxStore.hxd")
	*now = now.Add(OutlookMinReadInterval)
	if r, _ := run(t, defaultInterval(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
		t.Fatalf("%+v", r.Sources)
	}
	*now = now.Add(time.Minute)
	if r, _ := run(t, defaultInterval(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusSkippedInterval {
		t.Fatalf("a good read ends the failure: %+v", r.Sources)
	}
}

// A skip reports the losses of the last read, as an unchanged run does.
func TestSyncOutlookSkipKeepsOmissions(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "store-damaged-blocks.hxd")
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	run(t, defaultInterval(db, root))
	*now = now.Add(time.Minute)
	r, _ := run(t, defaultInterval(db, root))
	if s := sourceKeyed(t, r, "outlook|Main"); s.Status != StatusSkippedInterval || s.Omissions[outlookcal.CodeBlocksDamaged] == 0 || r.Status != StatusOmissions {
		t.Fatalf("%+v %s", s, r.Status)
	}
}

// A last attempt in the future is a clock that moved back: it does not hold the source off.
func TestSyncOutlookClockMovedBack(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	run(t, defaultInterval(db, root))
	*now = now.AddDate(-1, 0, 0)
	if r, _ := run(t, defaultInterval(db, root)); sourceKeyed(t, r, "outlook|Main").Status != StatusUnchanged {
		t.Fatalf("%+v", r.Sources)
	}
}

// Nothing to read is a failure that says where profiles are looked for.
func TestSyncOutlookNoProfiles(t *testing.T) {
	isolateTmp(t)
	fail := func(root string) *SourceError {
		rep, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: newDB(t), OutlookEnabled: true, OutlookRoot: root})
		var coded *errs.Coded
		if !errors.As(err, &coded) || coded.Code != errs.CodePartialSync || rep.Status != StatusPartial {
			t.Fatalf("%v %s", err, rep.Status)
		}
		return sourceKeyed(t, rep, "outlook").Error
	}
	empty := t.TempDir()
	if e := fail(empty); e.Code != CodeNoOutlookProfiles || !strings.Contains(e.Message, empty) {
		t.Fatal(e)
	}
	if e := fail(empty); !strings.Contains(e.Fix, "<root>/<profile>/HxStore.hxd") {
		t.Fatalf("fix: %q", e.Fix)
	}
	// The store file directly in the root: the fix says to pass the parent directory.
	direct := t.TempDir()
	putStoreAt(t, filepath.Join(direct, "HxStore.hxd"))
	if e := fail(direct); e.Code != CodeNoOutlookProfiles || !strings.Contains(e.Fix, "directly in "+direct) || !strings.Contains(e.Fix, "parent") {
		t.Fatalf("fix: %q", e.Fix)
	}
	// The JSON of the report carries the fix on the source's error, as the CLI prints it.
	for root, want := range map[string]string{empty: "<root>/<profile>/HxStore.hxd", direct: "parent"} {
		got := sourceErrorJSON(t, root)
		if got["code"] != CodeNoOutlookProfiles || got["message"] == "" || !strings.Contains(got["fix"], want) || len(got) != 3 {
			t.Fatalf("error object: %v", got)
		}
	}
	// A classic-only profile is a note and not a profile.
	classic := t.TempDir()
	if err := os.MkdirAll(filepath.Join(classic, "Old", "Data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(classic, "Old", "Data", "Outlook.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rep, _, _ := Run(context.Background(), Options{Root: fixtureRoot, DBPath: newDB(t), OutlookEnabled: true, OutlookRoot: classic})
	if len(rep.OutlookClassicOnly) != 1 || rep.OutlookClassicOnly[0] != "Old" || sourceKeyed(t, rep, "outlook").Error.Code != CodeNoOutlookProfiles {
		t.Fatalf("%+v %+v", rep.OutlookClassicOnly, rep.Sources)
	}
}

func putStoreAt(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(outlookFixture, "HxStore.hxd")) //nolint:gosec // a committed fixture
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

// A profile that cannot be examined is its own failed source; the others still read.
func TestSyncOutlookSkippedProfiles(t *testing.T) {
	isolateTmp(t)
	root := outlookRoot(t, "HxStore.hxd")
	old := outlookDiscover
	t.Cleanup(func() { outlookDiscover = old })
	outlookDiscover = func(r string) ([]outlookdesktop.Profile, []string, []outlookdesktop.SkippedProfile, error) {
		ps, c, _, err := old(r)
		return ps, c, []outlookdesktop.SkippedProfile{
			{Name: "Locked", Dir: "/x/Locked", Reason: "no_full_disk_access", Err: os.ErrPermission},
			{Name: "Odd", Dir: "/x/Odd", Reason: "unreadable", Err: os.ErrInvalid},
		}, err
	}
	rep, _, err := Run(context.Background(), outlookOpts(newDB(t), root))
	if err == nil || rep.Status != StatusPartial {
		t.Fatalf("%v %s", err, rep.Status)
	}
	if sourceKeyed(t, rep, "outlook|Locked").Error.Code != errs.CodeNoFullDiskAccess || sourceKeyed(t, rep, "outlook|Odd").Error.Code != CodeOutlookProfileUnreadable ||
		sourceKeyed(t, rep, "outlook|Main").Status != StatusOK {
		t.Fatalf("%+v", rep.Sources)
	}
}

// sourceErrorJSON runs a sync with Outlook at root and returns the JSON object of the "outlook"
// source's error, the way the report prints it.
func sourceErrorJSON(t *testing.T, root string) map[string]string {
	t.Helper()
	rep, _, _ := Run(context.Background(), Options{Root: fixtureRoot, DBPath: newDB(t), OutlookEnabled: true, OutlookRoot: root})
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var shape struct {
		Sources []struct {
			Source string
			Error  map[string]string
		}
	}
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatal(err)
	}
	for _, src := range shape.Sources {
		if src.Source == "outlook" {
			return src.Error
		}
	}
	t.Fatal("no outlook source")
	return nil
}

// A store of valid events holds many objects of classes that are not calendar; none of them is an
// unknown layout.
func TestSyncOutlookHealthyStoreHasNoUnknownLayouts(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	run(t, outlookOpts(db, outlookRoot(t, "HxStore.hxd")))
	if n := count(t, db, `select count(*) from meta where key='outlook_read:outlook/Main' and value like '%unknown_layouts%'`); n != 0 {
		t.Fatal("a healthy store lists unknown layouts")
	}
}

func marker(t *testing.T, db, key string) int {
	t.Helper()
	return count(t, db, `select count(*) from meta where key='`+key+`'`)
}

// The skip marker lives until a sync looks at the store, and the failure until a read succeeds.
func TestSyncOutlookMarkerLifecycle(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "store-version-j.hxd")
	now := fakeClock(t, time.Date(2031, 3, 5, 9, 0, 0, 0, time.UTC))
	_, _, _ = Run(context.Background(), defaultInterval(db, root))
	if marker(t, db, "outlook_failure:outlook/Main") != 1 || marker(t, db, "outlook_checked:Main") != 1 {
		t.Fatal("a failed read is remembered, and the store was looked at")
	}
	// A good store inside the interval is not read: the failure stays, and no skip is recorded.
	putOutlookStore(t, root, "HxStore.hxd")
	*now = now.Add(time.Minute)
	_, _, _ = Run(context.Background(), defaultInterval(db, root))
	if marker(t, db, "outlook_failure:outlook/Main") != 1 || marker(t, db, "outlook_skipped:Main") != 0 {
		t.Fatal("a skip after a failure keeps the failure and is not a plain skip")
	}
	// After the interval the good store is read: the failure goes.
	*now = now.Add(OutlookMinReadInterval)
	run(t, defaultInterval(db, root))
	if marker(t, db, "outlook_failure:outlook/Main") != 0 {
		t.Fatal("a good read must clear the failure")
	}
	// Now a skip is recorded, and a full read inside the interval is still a skip.
	*now = now.Add(time.Minute)
	o := defaultInterval(db, root)
	o.FullRead = true
	run(t, o)
	if marker(t, db, "outlook_skipped:Main") != 1 {
		t.Fatal("a full read inside the interval is skipped and says so")
	}
	// A full read after the interval looks at the store and reads it again: the skip is forgotten.
	*now = now.Add(OutlookMinReadInterval)
	r, _ := run(t, o)
	if marker(t, db, "outlook_skipped:Main") != 0 || sourceKeyed(t, r, "outlook|Main").Status != StatusOK {
		t.Fatalf("%+v", r.Sources)
	}
}

// syntheticStore writes a one-block store of plain future events named by n (their ids and times
// follow n) into the profile "Main" of root.
func syntheticStore(t *testing.T, root string, ns ...int) {
	t.Helper()
	var objs []*hxbuild.Object
	for _, n := range ns {
		start := time.Date(2031, 3, 1+n, 9, 0, 0, 0, time.UTC)
		objs = append(objs, hxbuild.NewEvent(hxbuild.EventSpec{
			ID: hxbuild.GlobalObjectID(0, 0, 0, fmt.Sprintf("GONE-TEST-%04d", n)), SeriesKey: 0xf1c7_0000_0000_0000 | uint64(n), //nolint:gosec // small counter
			DetailKey: uint32(2000 + n), LastModified: start.Add(-time.Hour), Start: start, End: start.Add(time.Hour), //nolint:gosec // small counter
			ZoneID: 2, ZoneName: "Fixture Standard Time", ShowAs: 2, Subject: fmt.Sprintf("Synthetic %d", n), SubjectBare: fmt.Sprintf("Synthetic %d", n),
			OrganizerName: "Fixture Organizer", OrganizerAddr: "fixture.organizer@example.invalid", AreaOneSize: 813,
		}))
	}
	b := hxbuild.New(hxbuild.Options{})
	b.BlockCodec(hxbuild.FramedPayload(hxbuild.Head(15), objs...), hxbuild.CodecLiteral)
	if err := os.WriteFile(filepath.Join(root, "Main", "HxStore.hxd"), b.Bytes(), 0o600); err != nil { //nolint:gosec // a test temp dir
		t.Fatal(err)
	}
}

// touchStore gives the profile's store a modification time of its own, so each read is of a distinct
// copy whatever the file system's timestamp resolution.
func touchStore(t *testing.T, root string, n int) {
	t.Helper()
	at := time.Date(2026, 10, 1, 12, n, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(root, "Main", "HxStore.hxd"), at, at); err != nil {
		t.Fatal(err)
	}
}

// An event the store held and no longer holds is marked gone (and keeps its data) by the second read that misses it;
// the run counts it; an event still held stays live. Outlook writes no tombstone, so absence is the signal.
func TestSyncOutlookMarksAnEventTheStoreDroppedGone(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	syntheticStore(t, root, 1, 2, 3)
	touchStore(t, root, 3)
	if r, _ := run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Counts.Calendar.Events.Inserted != 3 {
		t.Fatalf("%+v", r.Sources)
	}
	syntheticStore(t, root, 1, 3)
	touchStore(t, root, 1)
	r, _ := run(t, outlookOpts(db, root))
	if g := sourceKeyed(t, r, "outlook|Main").Counts.Calendar.Gone; g != 0 {
		t.Fatalf("one miss marked %d events gone", g)
	}
	touchStore(t, root, 2)
	r, _ = run(t, outlookOpts(db, root))
	o := sourceKeyed(t, r, "outlook|Main")
	if o.Status != StatusOK || o.Counts.Calendar.Gone != 1 || r.Calendar.Gone != 1 {
		t.Fatalf("%+v %+v", o, *o.Counts)
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is not null and subject='Synthetic 2'`); n != 1 {
		t.Fatalf("%d rows of the dropped event are marked gone", n)
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is null`); n != 2 {
		t.Fatalf("%d live Outlook rows, want 2", n)
	}
	syntheticStore(t, root, 1, 2, 3)
	touchStore(t, root, 3)
	if r, _ := run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Counts.Calendar.Gone != 0 ||
		count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is null`) != 3 {
		t.Fatalf("an event that returned stayed gone: %+v", r.Sources)
	}
}

// A read that lost blocks may have missed live events, so it marks nothing.
func TestSyncOutlookDamagedReadMarksNothingGone(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	run(t, outlookOpts(db, root))
	live := count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is null`)
	putOutlookStore(t, root, "store-damaged-blocks.hxd")
	touchStore(t, root, 1)
	r, _ := run(t, outlookOpts(db, root))
	if s := sourceKeyed(t, r, "outlook|Main"); s.Omissions[outlookcal.CodeBlocksDamaged] == 0 || s.Counts.Calendar.Gone != 0 {
		t.Fatalf("%+v", s)
	}
	touchStore(t, root, 2)
	if r, _ = run(t, outlookOpts(db, root)); sourceKeyed(t, r, "outlook|Main").Counts.Calendar.Gone != 0 {
		t.Fatalf("a second damaged read marked events gone: %+v", sourceKeyed(t, r, "outlook|Main"))
	}
	if n := count(t, db, `select count(*) from calendar_source_events where source='outlook' and removed_at is null`); n < live {
		t.Fatalf("live Outlook rows went from %d to %d on a damaged read", live, n)
	}
}
