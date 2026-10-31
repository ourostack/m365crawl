//go:build windows

package browser

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A job member that is exiting or being created while Close reads the job's process list is
// counted in NumberOfAssignedProcesses but missing from the list, or the reverse. That is not a
// list too large for the buffer, and Close must not fail with browser_completion_too_large
// because of it (#309). This drives the real Close of a fake browser and shifts only the count.
func TestCloseWhileTheJobsMembershipChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shift func(*completionJobPIDList)
	}{
		{"a member exiting", func(info *completionJobPIDList) { info.assigned++ }},
		{"a member being created", func(info *completionJobPIDList) {
			if info.assigned > 0 {
				info.assigned--
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _, err := launchFake(t, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			old := completionQueryJob
			t.Cleanup(func() { completionQueryJob = old })
			shifted := 0
			completionQueryJob = func(job windows.Handle, class int32, ptr unsafe.Pointer, size uint32, returned *uint32) error {
				err := old(job, class, ptr, size, returned)
				if err == nil && class == 3 {
					tc.shift((*completionJobPIDList)(ptr))
					shifted++
				}
				return err
			}
			if err := b.Close(); err != nil || shifted == 0 {
				t.Fatalf("Close = %v after %d shifted job lists", err, shifted)
			}
		})
	}
}

func TestCompletionNativeJobListCountChangeIsNotTruncation(t *testing.T) {
	old := completionQueryJob
	t.Cleanup(func() { completionQueryJob = old })
	for _, tc := range []struct {
		assigned, returned uint32
		want               int
		tooLarge           bool
	}{
		{2, 1, 1, false},      // a member exiting, not listed
		{1, 2, 2, false},      // a member listed before it was counted
		{4097, 4096, 0, true}, // more members than the buffer holds
		{4096, 4097, 0, true}, // a list longer than the buffer
		{4096, 4096, 4096, false},
	} {
		completionQueryJob = func(_ windows.Handle, _ int32, ptr unsafe.Pointer, _ uint32, _ *uint32) error {
			info := (*completionJobPIDList)(ptr)
			info.assigned, info.returned = tc.assigned, tc.returned
			for i := range info.pids {
				info.pids[i] = uintptr(i + 42)
			}
			return nil
		}
		pids, err := jobPIDs(7, 4096)
		if tc.tooLarge {
			if err == nil || err.Error() != "browser_completion_too_large" {
				t.Errorf("%d/%d: %v", tc.assigned, tc.returned, err)
			}
			continue
		}
		if err != nil || len(pids) != tc.want || pids[0] != 42 {
			t.Errorf("%d/%d: %d pids, %v", tc.assigned, tc.returned, len(pids), err)
		}
	}
}
