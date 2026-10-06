//go:build acceptance && !windows

package acceptance

import (
	"runtime"
	"syscall"
	"time"
)

// usage is this process's resource use so far: the peak resident set size (a high-water mark that
// never falls) and the CPU time spent. ok is false when the platform cannot say.
type usage struct {
	peakRSS int64
	cpu     time.Duration
	ok      bool
}

func processUsage() usage {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) != nil {
		return usage{}
	}
	rss := int64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024 // kilobytes everywhere but macOS
	}
	cpu := time.Duration(r.Utime.Nano()) + time.Duration(r.Stime.Nano())
	return usage{peakRSS: rss, cpu: cpu, ok: true}
}
