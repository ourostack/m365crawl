//go:build acceptance

package acceptance

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"testing"
	"time"
)

// profileEnv names a directory; when set, the first Outlook sync is profiled into it. The files
// hold function names, file names and sizes only (no store content), so they are safe to keep.
//
//	cpu-first-outlook-sync.pprof   CPU profile of the first Outlook sync
//	heap-at-outlook-peak.pprof     heap profile taken when the live heap was largest during it
//	                               (sample indexes inuse_space and alloc_space are both in the file:
//	                               go tool pprof -sample_index=inuse_space heap-at-outlook-peak.pprof)
const profileEnv = "M365CRAWL_ACCEPTANCE_PROFILE"

// profileSampleEvery is how often the live heap is read to find the peak.
const profileSampleEvery = 20 * time.Millisecond

// startProfile begins profiling when profileEnv is set and returns the function that stops it and
// writes the files; with the variable unset it does nothing. Failures are logged, never fatal.
func startProfile(t *testing.T) (stop func()) {
	dir := os.Getenv(profileEnv)
	if dir == "" {
		return func() {}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // the operator's own scratch directory
		t.Logf("profile: cannot create the profile directory: %v", err)
		return func() {}
	}
	runtime.MemProfileRate = 32 << 10                                             // finer than the default, for the allocation sites of a short run
	cpuFile, err := os.Create(filepath.Join(dir, "cpu-first-outlook-sync.pprof")) //nolint:gosec // the operator's own scratch directory
	if err == nil {
		err = pprof.StartCPUProfile(cpuFile)
	}
	cpuOn := err == nil
	if !cpuOn {
		t.Logf("profile: no CPU profile: %v", err)
	}

	var (
		mu      sync.Mutex
		peak    uint64
		peakBuf []byte
		quit    = make(chan struct{})
		done    = make(chan struct{})
	)
	snapshot := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		mu.Lock()
		defer mu.Unlock()
		if ms.HeapInuse <= peak {
			return
		}
		var buf bytes.Buffer
		if pprof.Lookup("heap").WriteTo(&buf, 0) == nil {
			peak, peakBuf = ms.HeapInuse, buf.Bytes()
		}
	}
	go func() {
		defer close(done)
		tick := time.NewTicker(profileSampleEvery)
		defer tick.Stop()
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				snapshot()
			}
		}
	}()
	return func() {
		close(quit)
		<-done
		snapshot()
		if cpuOn {
			pprof.StopCPUProfile()
			_ = cpuFile.Close()
		}
		mu.Lock()
		defer mu.Unlock()
		if peakBuf == nil {
			t.Log("profile: no heap profile was taken")
			return
		}
		path := filepath.Join(dir, "heap-at-outlook-peak.pprof")
		if err := os.WriteFile(path, peakBuf, 0o600); err != nil { //nolint:gosec // the operator's own scratch directory
			t.Logf("profile: cannot write the heap profile: %v", err)
			return
		}
		t.Logf("profile: wrote the CPU and heap profiles of the first Outlook sync (live heap peak %.1f MiB) to the directory named by %s", float64(peak)/(1<<20), profileEnv)
	}
}
