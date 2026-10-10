package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSealJobPreventsLateDescendantAssociation(t *testing.T) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := windows.TerminateJobObject(job, 1); err != nil {
			t.Error("job termination failed")
		}
		if err := windows.CloseHandle(job); err != nil {
			t.Error("job close failed")
		}
	})
	first := suspendedTestProcess(t)
	second := suspendedTestProcess(t)
	for _, process := range []windows.Handle{first, second} {
		if err := windows.AssignProcessToJobObject(job, process); err != nil {
			t.Fatal("initial association failed")
		}
	}
	if err := sealJob(job); err != nil {
		t.Fatal(err)
	}
	var accounting jobAccounting
	// #nosec G103 -- Fixed native accounting layout used to assert the real two-member boundary.
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
		t.Fatal("job accounting failed")
	}
	if accounting.active != 2 {
		t.Fatal("barrier unexpectedly terminated existing members")
	}
	late := suspendedTestProcess(t)
	if err := windows.AssignProcessToJobObject(job, late); err == nil {
		t.Fatal("late process entered the supposedly sealed handle inventory")
	}
}

func TestMemberHandleRefusesRecycledUnrelatedPID(t *testing.T) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.CloseHandle(job); err != nil {
			t.Error("job close failed")
		}
	}()
	unrelated, pid := suspendedTestProcessWithPID(t)
	handle, err := memberHandle(job, pid)
	if err == nil {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			t.Error("rejected handle close failed")
		}
		t.Fatal("unrelated PID accepted as an inventoried job member")
	}
	state, err := windows.WaitForSingleObject(unrelated, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("unrelated process was affected")
	}
}

func TestInventoryHoldsPostInventoryOwnership(t *testing.T) {
	for _, target := range []string{"root", "nested", "parent", "file"} {
		t.Run(target, func(t *testing.T) {
			parent := t.TempDir()
			w, err := newWorkspace(parent)
			if err != nil {
				t.Fatal(err)
			}
			nested := filepath.Join(w.dir, "sub")
			if err := os.Mkdir(nested, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(nested, "payload")
			if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "sentinel")
			if err := os.WriteFile(outside, []byte("unrelated-synthetic"), 0o600); err != nil {
				t.Fatal(err)
			}
			inventory, err := w.inventory()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := inventory.close(); err != nil {
					t.Error(err)
				}
				if err := w.cleanup(); err != nil {
					t.Error(err)
				}
			}()
			path := map[string]string{"root": w.dir, "nested": nested, "parent": parent, "file": file}[target]
			moved := path + "-replaced"
			if err := os.Rename(path, moved); err == nil {
				if err := os.Rename(moved, path); err != nil {
					t.Fatal("synthetic ownership restoration failed")
				}
				t.Fatal("post-inventory ownership replacement was not blocked")
			}
			if _, err := os.Stat(outside); err != nil {
				t.Fatal("unrelated sentinel affected")
			}
		})
	}
}

func TestSealedMemberCannotSpawnAtInventoryBoundary(t *testing.T) {
	w, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.cleanup(); err != nil {
			t.Error(err)
		}
	})
	control := t.TempDir()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stopJob(job); err != nil {
			t.Error(err)
		}
		if err := windows.CloseHandle(job); err != nil {
			t.Error("job close failed")
		}
	})
	env := workspaceEnv(append(os.Environ(), "OWA_SUPERVISOR_HELPER=late-parent", "OWA_SUPERVISOR_CONTROL="+control), w.dir)
	handle, pid := suspendedTestProcessEnv(t, env)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		t.Fatal("parent job association failed")
	}
	if err := resumeLeader(pid); err != nil {
		t.Fatal(err)
	}
	waitSyntheticReceipt(t, filepath.Join(control, "ready"))
	if err := sealJob(job); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "attempt"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := waitSyntheticReceipt(t, filepath.Join(control, "attempt-result"))
	if string(result) != "refused" {
		t.Fatal("job member started a late descendant after the inventory barrier")
	}
	if _, err := os.Stat(filepath.Join(control, "late-descendant")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late descendant executed payload code")
	}
}

func TestDisposalRefusesUninventoriedArrival(t *testing.T) {
	w, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := w.inventory()
	if err != nil {
		t.Fatal(err)
	}
	arrival := filepath.Join(w.dir, "late-file")
	if err := os.WriteFile(arrival, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := inventory.remove(); err == nil {
		t.Fatal("uninventoried arrival erased or ignored")
	}
	if _, err := os.Stat(arrival); err != nil {
		t.Fatal("uninventoried object was removed")
	}
	if err := w.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func waitSyntheticReceipt(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		// #nosec G304 G703 -- Caller supplies a fixed synthetic receipt path in its exact t.TempDir.
		data, err := os.ReadFile(path)
		if err == nil && len(data) != 0 {
			return data
		}
		if (err != nil && !errors.Is(err, os.ErrNotExist)) || time.Now().After(deadline) {
			t.Fatal("synthetic receipt missing")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func suspendedTestProcess(t *testing.T) windows.Handle {
	t.Helper()
	handle, _ := suspendedTestProcessWithPID(t)
	return handle
}

func suspendedTestProcessWithPID(t *testing.T) (windows.Handle, uint32) {
	return suspendedTestProcessEnv(t, append(os.Environ(), "OWA_SUPERVISOR_HELPER=unrelated"))
}

func suspendedTestProcessEnv(t *testing.T, env []string) (windows.Handle, uint32) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G204 -- Re-executes this test binary, suspended, with a fixed synthetic selector.
	cmd := exec.Command(exe, "-test.run=^TestSupervisorChild$")
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		t.Fatal("synthetic process start failed")
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if err := cmd.Wait(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Error("synthetic wait failed")
			}
		}
	})
	if cmd.Process.Pid <= 0 || int64(cmd.Process.Pid) > 0xffffffff {
		t.Fatal("synthetic PID invalid")
	}
	// #nosec G115 -- Fatal above rejects non-positive and out-of-range native PIDs.
	pid := uint32(cmd.Process.Pid)
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal("synthetic process handle failed")
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(handle); err != nil {
			t.Error("synthetic handle close failed")
		}
	})
	return handle, pid
}
