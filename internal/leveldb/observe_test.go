package leveldb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/syndtr/goleveldb/leveldb/util"
)

// Observe sees every record Keep rejects, with the right table-or-log origin and value length.
func TestLoadObserveSeesRejectedKeys(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	big := make([]byte, lazyMin+10)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Put([]byte("tab1"), []byte("v1"), nil))
	must(db.Put([]byte("tab2"), big, nil))
	must(db.CompactRange(util.Range{}))
	must(db.Put([]byte("log1"), []byte("abc"), nil))
	must(db.Delete([]byte("log2"), nil))
	must(db.Close())

	type seen struct {
		n         int
		fromTable bool
	}
	got := map[string]seen{}
	d := mustLoadWith(t, dir, LoadOptions{
		Keep:    func([]byte) bool { return false },
		Observe: func(k []byte, n int, ft bool) { got[string(k)] = seen{n, ft} },
	})
	want := map[string]seen{
		"tab1": {2, true}, "tab2": {len(big), true}, "log1": {3, false}, "log2": {0, false},
	}
	if len(got) != len(want) {
		t.Fatalf("observed %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: observed %v, want %v", k, got[k], w)
		}
	}
	if d.Stats().Keys != 0 || d.Stats().Skipped != 4 {
		t.Errorf("stats %+v: Keep should have rejected all 4", d.Stats())
	}
}

// Nil Observe is a plain load.
func TestLoadObserveNilIsPlain(t *testing.T) {
	dir := t.TempDir()
	db := openGL(t, dir)
	_ = db.Put([]byte("k"), []byte("v"), nil)
	_ = db.Close()
	d := mustLoadWith(t, dir, LoadOptions{})
	if d.Stats().Keys != 1 {
		t.Fatalf("load: %+v", d)
	}
}

// EntryOverhead matches the measured live heap per retained entry.
func TestEntryOverheadCalibrated(t *testing.T) {
	const n = 50000
	const fixtureEnv = "M365CRAWL_LEVELDB_CALIBRATION_FIXTURE"
	if os.Getenv(fixtureEnv) == "1" {
		dir, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		d := mustLoad(t, dir)
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(d)
		if d.Stats().Keys != n {
			t.Fatalf("keys %d", d.Stats().Keys)
		}
		const payload = n * (23 + 37)
		per := (float64(after.HeapAlloc) - float64(before.HeapAlloc) - payload) / n
		t.Logf("measured %.1f overhead bytes per entry; EntryOverhead = %d", per, EntryOverhead)
		// Under-estimating is the dangerous direction: allow no more than 5% below the measurement
		// and up to 50% above it.
		if lo, hi := per*0.95, per*1.5; float64(EntryOverhead) < lo || float64(EntryOverhead) > hi {
			t.Errorf("EntryOverhead = %d, measured %.1f per entry (allowed %.1f to %.1f)", EntryOverhead, per, lo, hi)
		}
		return
	}
	dir := t.TempDir()
	db := openGL(t, dir)
	for i := 0; i < n; i++ {
		// Odd lengths (23 and 37 bytes) so allocator size-class rounding is in the measurement.
		k := fmt.Sprintf("key-%019d", i)
		v := fmt.Sprintf("val-%033d", i)
		if err := db.Put([]byte(k), []byte(v), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	budget := 30 * time.Second
	if deadline, ok := t.Deadline(); ok {
		budget = min(budget, time.Until(deadline)-time.Second)
	}
	if budget <= 0 {
		t.Fatal("no time remains for isolated calibration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Fixture creation and earlier tests can retain heap across the measurement's first GC.
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEntryOverheadCalibrated$", "-test.v", "-test.timeout="+budget.String()) //nolint:gosec // G204: only this test executable, a literal selector and bounded timeout.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), fixtureEnv+"=1")
	output, err := cmd.CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatalf("isolated calibration failed: %v", err)
	}
}

func TestLazyMinIsTheThreshold(t *testing.T) {
	if LazyMin() != lazyMin {
		t.Errorf("LazyMin() = %d, lazyMin = %d", LazyMin(), lazyMin)
	}
}
