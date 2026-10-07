//go:build acceptance

package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// profileEnv names a directory; when set, the first Outlook sync is profiled into it. The files
// hold function names, file names and sizes only (no store content), so they are safe to keep.
//
//	cpu-first-outlook-sync.pprof   CPU profile of the first Outlook sync
//	memory-trace-first-outlook-sync.txt  a line every 100 ms: resident size and the runtime's accounts
//	heap-at-outlook-peak.pprof     heap profile taken when the live heap was largest during it
//	                               (sample indexes inuse_space and alloc_space are both in the file:
//	                               go tool pprof -sample_index=inuse_space heap-at-outlook-peak.pprof)
const profileEnv = "M365CRAWL_ACCEPTANCE_PROFILE"

// profileSampleEvery is how often the live heap is read to find the peak.
const profileSampleEvery = 20 * time.Millisecond

// traceEvery is how often the memory trace takes a line.
const traceEvery = 100 * time.Millisecond

// memTrace is the timeline of the first Outlook sync: counts and sizes only. Each line is the time
// since the sync began, the process's resident size now (from ps) and the runtime's own accounts, in
// MiB; a "phase" line marks the progress line the sync wrote at that time. The resident size less
// the runtime's Sys-HeapReleased is memory the Go runtime does not account for (SQLite's, the C
// library's, file mappings).
type memTrace struct {
	mu    sync.Mutex
	start time.Time
	lines []string
}

func (m *memTrace) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.start.IsZero() {
		return len(p), nil
	}
	m.lines = append(m.lines, fmt.Sprintf("%7.2fs phase %s", time.Since(m.start).Seconds(), strings.TrimSpace(string(p))))
	return len(p), nil
}

// sample appends one line of the runtime's accounts and the resident size.
func (m *memTrace) sample() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss := int64(-1)
	if out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output(); err == nil { //nolint:gosec // fixed command
		if kb, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); perr == nil {
			rss = kb >> 10
		}
	}
	mib := func(b uint64) int64 { return int64(b >> 20) } //nolint:gosec // a shift of a byte count
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.start.IsZero() {
		return
	}
	m.lines = append(m.lines, fmt.Sprintf("%7.2fs rss=%d heapAlloc=%d heapInuse=%d heapIdleNotReleased=%d heapReleased=%d stack=%d mspan=%d gcSys=%d otherSys=%d sys=%d unaccounted=%d",
		time.Since(m.start).Seconds(), rss, mib(ms.HeapAlloc), mib(ms.HeapInuse), mib(ms.HeapIdle-ms.HeapReleased), mib(ms.HeapReleased), mib(ms.StackInuse), mib(ms.MSpanInuse), mib(ms.GCSys), mib(ms.OtherSys), mib(ms.Sys), rss-mib(ms.Sys-ms.HeapReleased)))
}

var trace memTrace

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

	trace.mu.Lock()
	trace.start = time.Now()
	trace.mu.Unlock()
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
		lastTrace := time.Time{}
		for {
			select {
			case <-quit:
				return
			case <-tick.C:
				snapshot()
				if time.Since(lastTrace) >= traceEvery {
					lastTrace = time.Now()
					trace.sample()
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-done
		snapshot()
		trace.sample()
		trace.mu.Lock()
		tracePath := filepath.Join(dir, "memory-trace-first-outlook-sync.txt")
		if err := os.WriteFile(tracePath, []byte(strings.Join(trace.lines, "\n")+"\n"), 0o600); err != nil { //nolint:gosec // the operator's own scratch directory
			t.Logf("profile: cannot write the memory trace: %v", err)
		}
		trace.start = time.Time{}
		trace.mu.Unlock()
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
