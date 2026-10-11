package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/scripts/owaacceptance/protocol"
	"golang.org/x/sys/windows"
)

func TestStartupFailureReturnsBeforeWaitingOnSuspendedLeader(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test_executable_failed")
	}
	for _, stage := range []string{"before-resume", "resume", "open", "assign", "assign-live", "assign-unknown-wait", "record"} {
		for _, refusal := range []string{"job_birth_barrier_failed", "job_process_inventory_failed", "job_process_handle_failed", "none"} {
			t.Run(stage+"/"+refusal, func(t *testing.T) {
				oldOpen, oldAssign, oldKill := startupOpenProcess, startupAssignJob, startupKillProcess
				oldBefore, oldResume, oldStop := startupBeforeResume, startupResumeLeader, startupStopJob
				oldTerminate, oldWait := startupTerminate, startupWait
				t.Cleanup(func() {
					startupOpenProcess, startupAssignJob, startupKillProcess = oldOpen, oldAssign, oldKill
					startupBeforeResume, startupResumeLeader, startupStopJob = oldBefore, oldResume, oldStop
					startupTerminate, startupWait = oldTerminate, oldWait
				})
				var observer windows.Handle
				observed := make(chan windows.Handle, 1)
				startupOpenProcess = func(access uint32, inherit bool, pid uint32) (windows.Handle, error) {
					h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
					if err != nil {
						return 0, err
					}
					observed <- h
					if stage == "open" {
						return 0, windows.ERROR_ACCESS_DENIED
					}
					return oldOpen(access, inherit, pid)
				}
				t.Cleanup(func() {
					if observer != 0 {
						if err := windows.CloseHandle(observer); err != nil {
							t.Error("observer_close_failed")
						}
					}
				})
				failure := errors.New("synthetic_startup_refused")
				startupBeforeResume = func(q *qualifiedRun, pid uint32) error {
					if stage == "before-resume" {
						return failure
					}
					return oldBefore(q, pid)
				}
				startupResumeLeader = func(uint32) error { return failure }
				startupStopJob = func(job windows.Handle, deadline time.Time) error {
					if refusal == "none" {
						return oldStop(job, deadline)
					}
					return errors.New(refusal)
				}
				if strings.HasPrefix(stage, "assign") {
					startupAssignJob = func(windows.Handle, windows.Handle) error { return windows.ERROR_ACCESS_DENIED }
				}
				if stage == "open" || stage == "assign" {
					startupKillProcess = func(*os.Process) error { return windows.ERROR_ACCESS_DENIED }
				}
				if stage == "assign-live" || stage == "assign-unknown-wait" {
					startupTerminate = func(windows.Handle, uint32) error { return windows.ERROR_ACCESS_DENIED }
				}
				if stage == "assign-unknown-wait" {
					startupWait = func(windows.Handle, uint32) (uint32, error) { return windows.WAIT_FAILED, windows.ERROR_ACCESS_DENIED }
				}
				w, q, err := newQualifiedRun(exe, testDigest(t, exe), qualifiedFixtureRoot(t), t.TempDir(), filepath.Join(t.TempDir(), "receipt"))
				if err != nil {
					t.Fatal("workspace_create_failed")
				}
				t.Cleanup(func() {
					if err := errors.Join(q.closePins(), q.binary.Close()); err != nil {
						t.Error("qualification_pin_close_failed")
					}
				})
				dir := w.dir
				if stage == "record" {
					if err := protocol.WriteProtectedNew(filepath.Join(q.receipt, "started.json"), []byte("synthetic-existing")); err != nil {
						t.Fatal("existing_startup_record_failed")
					}
				}
				returned := make(chan error, 1)
				go func() {
					_, err := w.run(exe, []string{"-test.run=^TestSupervisorChild$"}, append(os.Environ(), "OWA_SUPERVISOR_HELPER=unrelated"), time.Second)
					returned <- err
				}()
				select {
				case observer = <-observed:
				case <-time.After(3 * time.Second):
					t.Fatal("owned_observer_missing")
				}
				var result error
				hung := false
				select {
				case result = <-returned:
				case <-time.After(3 * time.Second):
					hung = true
				}
				// Rescue only this held exact child handle, even when the old implementation waits forever.
				if observer == 0 {
					t.Fatal("owned_observer_missing")
				}
				if err := windows.TerminateProcess(observer, 1); err != nil {
					state, waitErr := windows.WaitForSingleObject(observer, 0)
					if waitErr != nil || state != windows.WAIT_OBJECT_0 {
						t.Fatal("owned_rescue_termination_failed")
					}
				}
				if state, err := windows.WaitForSingleObject(observer, 1000); err != nil || state != windows.WAIT_OBJECT_0 {
					t.Fatal("owned_rescue_signal_failed")
				}
				if hung {
					select {
					case <-returned:
					case <-time.After(time.Second):
						t.Fatal("owned_run_did_not_return_after_rescue")
					}
					t.Fatal("startup_wait_exceeded_shared_budget")
				}
				if result == nil {
					t.Fatal("startup_failure_became_success")
				}
				data, err := protocol.ReadProtected(filepath.Join(q.receipt, "started.json"))
				var started struct {
					PID           uint32           `json:"pid"`
					Child         protocol.Process `json:"child"`
					IdentityKnown bool             `json:"identity_known"`
				}
				if stage != "record" && (err != nil || json.Unmarshal(data, &started) != nil || !started.IdentityKnown || started.PID == 0 || started.Child.PID != started.PID || started.Child.Created == 0) {
					t.Fatal("started_generation_recovery_record_missing")
				}
				if stage != "record" {
					var created, exited, kernel, user windows.Filetime
					if err := windows.GetProcessTimes(observer, &created, &exited, &kernel, &user); err != nil || started.Child.Created != uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) {
						t.Fatal("started_generation_does_not_match_held_child")
					}
				}
				if stage == "before-resume" || stage == "resume" {
					if !errors.Is(result, failure) || refusal != "none" && !strings.Contains(result.Error(), refusal) {
						t.Fatal("startup_original_or_stop_failure_lost")
					}
				}
				if strings.Contains(result.Error(), "cleanup_deferred_consumers_unverified") {
					if _, err := os.Stat(dir); err != nil {
						t.Fatal("unproved_workspace_not_retained")
					}
				}
				if q.finish(-1, result) == nil {
					t.Fatal("startup_refusal_qualified")
				}
				closeData, err := protocol.ReadProtected(filepath.Join(q.receipt, protocol.CloseoutName))
				closeout, decodeErr := protocol.DecodeCloseout(closeData)
				if err != nil || decodeErr != nil || closeout.Qualified {
					t.Fatal("startup_closeout_invalid")
				}
				if strings.Contains(result.Error(), "cleanup_deferred_consumers_unverified") && (closeout.ConsumersReleased || closeout.RootState != "retained_owned") {
					t.Fatal("unproved_startup_closeout_lost_retention")
				}
				// The exact rescued leader is signalled; no other process was started in this suspended test.
				if _, err := os.Stat(dir); err == nil {
					if err := w.cleanup(); err != nil {
						t.Fatal("rescued_workspace_cleanup_failed")
					}
				}
				if _, err := os.Stat(filepath.Join(dir, "unused")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("rescued_root_not_absent")
				}
			})
		}
	}
}
