//go:build acceptance && windows

package acceptance

import (
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// usage is this process's resource use so far: the peak working set size in bytes and the
// cumulative CPU time spent. ok is false when Windows cannot read both measurements.
type usage struct {
	peakRSS int64
	cpu     time.Duration
	ok      bool
}

type processMemoryInfoFn func(windows.Handle, *processMemoryCountersEx, uint32) error

type processTimesFn func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error

type processMemoryCountersEx struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	psapi                    = windows.NewLazySystemDLL("psapi.dll")
	k32GetProcessMemoryInfo  = kernel32.NewProc("K32GetProcessMemoryInfo")
	getProcessMemoryInfoProc = psapi.NewProc("GetProcessMemoryInfo")
)

func processUsage() usage {
	return measureProcessUsage(windows.CurrentProcess(), readProcessMemoryInfo, windows.GetProcessTimes)
}

func measureProcessUsage(process windows.Handle, memoryInfo processMemoryInfoFn, processTimes processTimesFn) usage {
	var mem processMemoryCountersEx
	mem.Cb = uint32(unsafe.Sizeof(mem))
	if err := memoryInfo(process, &mem, mem.Cb); err != nil {
		return usage{}
	}
	var creation, exit, kernelTime, userTime windows.Filetime
	if err := processTimes(process, &creation, &exit, &kernelTime, &userTime); err != nil {
		return usage{}
	}
	return usage{
		peakRSS: int64(mem.PeakWorkingSetSize),
		cpu:     processCPUDuration(kernelTime, userTime),
		ok:      true,
	}
}

func readProcessMemoryInfo(process windows.Handle, mem *processMemoryCountersEx, size uint32) error {
	if err := k32GetProcessMemoryInfo.Find(); err == nil {
		return callProcessMemoryInfo(k32GetProcessMemoryInfo, process, mem, size)
	}
	return callProcessMemoryInfo(getProcessMemoryInfoProc, process, mem, size)
}

func callProcessMemoryInfo(proc *windows.LazyProc, process windows.Handle, mem *processMemoryCountersEx, size uint32) error {
	if err := proc.Find(); err != nil {
		return err
	}
	r1, _, e1 := proc.Call(uintptr(process), uintptr(unsafe.Pointer(mem)), uintptr(size))
	if r1 == 0 {
		if e1 == nil || e1 == windows.ERROR_SUCCESS {
			return syscall.EINVAL
		}
		return e1
	}
	return nil
}

func filetimeDuration(ft windows.Filetime) time.Duration {
	ticks := (uint64(ft.HighDateTime) << 32) | uint64(ft.LowDateTime)
	return time.Duration(ticks * 100)
}

func processCPUDuration(kernelTime, userTime windows.Filetime) time.Duration {
	return filetimeDuration(kernelTime) + filetimeDuration(userTime)
}

func TestProcessUsageReportsNativeMetrics(t *testing.T) {
	got := processUsage()
	if !got.ok {
		t.Fatal("expected native Windows process metrics to be available")
	}
	if got.peakRSS <= 0 {
		t.Fatalf("expected peak RSS in bytes to be positive, got %d", got.peakRSS)
	}
	if got.cpu < 0 {
		t.Fatalf("expected cumulative CPU time to be non-negative, got %s", got.cpu)
	}
}

func TestMeasureProcessUsageReturnsUnavailableOnQueryFailures(t *testing.T) {
	t.Run("memory query fails", func(t *testing.T) {
		timesCalled := false
		got := measureProcessUsage(
			windows.CurrentProcess(),
			func(windows.Handle, *processMemoryCountersEx, uint32) error { return windows.ERROR_ACCESS_DENIED },
			func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
				timesCalled = true
				return nil
			},
		)
		if got.ok {
			t.Fatal("expected usage to be unavailable when the memory query fails")
		}
		if timesCalled {
			t.Fatal("expected process times query to be skipped after a memory query failure")
		}
	})

	t.Run("process times query fails", func(t *testing.T) {
		got := measureProcessUsage(
			windows.CurrentProcess(),
			func(_ windows.Handle, mem *processMemoryCountersEx, _ uint32) error {
				mem.PeakWorkingSetSize = 4096
				return nil
			},
			func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
				return windows.ERROR_INVALID_HANDLE
			},
		)
		if got.ok {
			t.Fatal("expected usage to be unavailable when the process-times query fails")
		}
	})
}

func TestProcessCPUDurationUsesRaw100nsTicks(t *testing.T) {
	got := processCPUDuration(
		windows.Filetime{LowDateTime: 1},
		windows.Filetime{LowDateTime: 2},
	)
	want := 300 * time.Nanosecond
	if got != want {
		t.Fatalf("expected 3 raw FILETIME ticks to equal %s, got %s", want, got)
	}
}
