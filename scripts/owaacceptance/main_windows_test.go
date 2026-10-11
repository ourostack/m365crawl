package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestOwnedRunCleansEachExitAndOnlyItsProcesses(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			for cycle := 0; cycle < 2; cycle++ {
				control := t.TempDir()
				unrelated := exec.Command(exe, "-test.run=^TestSupervisorChild$")
				unrelated.Env = append(os.Environ(), "OWA_SUPERVISOR_HELPER=unrelated")
				if err := unrelated.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = unrelated.Process.Kill()
					_ = unrelated.Wait()
				}()
				w, err := newWorkspace(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				dir := w.dir
				env := append(os.Environ(), "OWA_SUPERVISOR_HELPER="+mode, "OWA_SUPERVISOR_CONTROL="+control)
				exit, err := w.run(exe, []string{"-test.run=^TestSupervisorChild$"}, env, 2*time.Second)
				if mode == "success" && (err != nil || exit != 0) {
					t.Fatalf("normal run failed: exit=%d err=%v", exit, err)
				}
				if mode == "failure" && (err != nil || exit != 7) {
					t.Fatalf("failed run lost exit: exit=%d err=%v", exit, err)
				}
				if mode == "timeout" && !errors.Is(err, errTimeout) {
					t.Fatalf("timeout not reported: %v", err)
				}
				if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("owned payload retained")
				}
				for _, name := range []string{"leader", "descendant"} {
					// #nosec G304 -- These fixed receipt names are under this test's exact t.TempDir.
					data, err := os.ReadFile(filepath.Join(control, name))
					if err != nil {
						t.Fatal("child did not exercise payload lifetime")
					}
					pid, err := strconv.Atoi(string(data))
					if err != nil || pid <= 0 || int64(pid) > 0xffffffff {
						t.Fatal(err)
					}
					if processAlive(t, pid) {
						t.Fatalf("owned %s survived", name)
					}
				}
				if !processAlive(t, unrelated.Process.Pid) {
					t.Fatal("unrelated process terminated")
				}
			}
		})
	}
}

func TestWorkspaceCleanupRefusesReplacementAndReparse(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		w, err := newWorkspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		moved := w.dir + "-original"
		if err := os.Rename(w.dir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(w.dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := w.cleanup(); err == nil {
			t.Fatal("replacement was deleted as owned")
		}
		if err := os.Remove(w.dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, w.dir); err != nil {
			t.Fatal(err)
		}
		if err := w.cleanup(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("junction", func(t *testing.T) {
		w, err := newWorkspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "sentinel")
		if err := os.WriteFile(sentinel, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(w.dir, "junction")
		// #nosec G204 -- Fixed Windows junction command with test-owned absolute directories.
		if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("junction setup: %v %s", err, output)
		}
		if err := w.cleanup(); err == nil {
			t.Fatal("reparse cleanup accepted")
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Fatal("outside file touched")
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := w.cleanup(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWorkspaceACLAndInvalidLaunch(t *testing.T) {
	w, err := newWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(w.dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	text := sd.String()
	if text == "" || !privateACL(sd) {
		t.Fatal("workspace is not private before child start")
	}
	dir := w.dir
	if _, err := w.run(filepath.Join(dir, "missing.exe"), nil, os.Environ(), time.Second); err == nil {
		t.Fatal("missing executable accepted")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed launch retained workspace")
	}
}

func TestWorkspaceRefusesGitAndRelativeParents(t *testing.T) {
	for _, marker := range []string{"directory", "worktree-file"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			git := filepath.Join(root, ".git")
			if marker == "directory" {
				if err := os.Mkdir(git, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(git, []byte("synthetic-worktree-marker"), 0o600); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "nested")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			if w, err := newWorkspace(parent); err == nil {
				_ = w.cleanup()
				t.Fatal("Git ancestor accepted for private payload storage")
			}
		})
	}
	if _, err := newWorkspace("."); err == nil {
		t.Fatal("relative parent accepted")
	}
}

func TestWorkspaceEnvironmentReplacesCaseInsensitiveDefaults(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	env := workspaceEnv([]string{"tmp=unowned", "TEMP=unowned", "appdata=unowned", "LOCALAPPDATA=unowned", "HOME=unowned", "USERPROFILE=unowned", "TMPDIR=unowned", "GOWORK=unowned", "PATH=preserved"}, root)
	values := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatal("invalid environment entry")
		}
		key = strings.ToUpper(key)
		if _, found := values[key]; found {
			t.Fatal("duplicate environment key")
		}
		values[key] = value
	}
	for _, key := range []string{"TMP", "TEMP", "TMPDIR", "HOME", "USERPROFILE"} {
		if values[key] != root {
			t.Fatal("unowned default survived")
		}
	}
	if values["APPDATA"] != filepath.Join(root, "AppData", "Roaming") || values["LOCALAPPDATA"] != filepath.Join(root, "AppData", "Local") || values["GOWORK"] != "off" || values["PATH"] != "preserved" {
		t.Fatal("default environment isolation failed")
	}
}

func TestSupervisorChild(t *testing.T) {
	mode := os.Getenv("OWA_SUPERVISOR_HELPER")
	if mode == "" {
		return
	}
	if mode == "unrelated" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	control := os.Getenv("OWA_SUPERVISOR_CONTROL")
	root := os.Getenv("TEMP")
	if !filepath.IsAbs(control) || !filepath.IsAbs(root) || safeAncestors(control) != nil || safeAncestors(root) != nil || !strings.HasPrefix(filepath.Base(root), "owa-acceptance-") {
		os.Exit(28)
	}
	if mode == "descendant" || mode == "late-descendant" {
		receipt, payload := "descendant", "held-payload"
		if mode == "late-descendant" {
			receipt, payload = "late-descendant", "late-held-payload"
		}
		// #nosec G703 -- Parent-created synthetic control directory was checked above; fixed receipt name.
		if err := os.WriteFile(filepath.Join(control, receipt), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(20)
		}
		// #nosec G703 -- Synthetic child receives the supervisor's validated private TEMP; fixed file name.
		if err := os.WriteFile(filepath.Join(root, payload), []byte("synthetic-private"), 0o600); err != nil {
			os.Exit(21)
		}
		// #nosec G304 G703 -- Open only the synthetic payload just created in the validated owned TEMP.
		held, err := os.Open(filepath.Join(root, payload))
		if err != nil {
			os.Exit(22)
		}
		time.Sleep(time.Minute)
		if err := held.Close(); err != nil {
			os.Exit(29)
		}
		os.Exit(0)
	}
	// #nosec G703 -- Fixed PID receipt in the validated parent-created synthetic control directory.
	if err := os.WriteFile(filepath.Join(control, "leader"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(23)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(24)
	}
	// #nosec G204 -- Re-executes this test executable only with a fixed synthetic child test selector.
	child := exec.Command(exe, "-test.run=^TestSupervisorChild$")
	child.Env = append(os.Environ(), "OWA_SUPERVISOR_HELPER=descendant")
	if err := child.Start(); err != nil {
		os.Exit(25)
	}
	deadline := time.Now().Add(time.Second)
	for {
		// #nosec G703 -- Fixed synthetic file inside the validated supervisor-owned TEMP.
		if _, err := os.Stat(filepath.Join(root, "held-payload")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			os.Exit(26)
		}
		time.Sleep(10 * time.Millisecond)
	}
	switch mode {
	case "success":
		os.Exit(0)
	case "failure":
		os.Exit(7)
	case "timeout":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "late-parent":
		// #nosec G703 -- Fixed synthetic synchronization receipt under the validated control directory.
		if err := os.WriteFile(filepath.Join(control, "ready"), []byte("1"), 0o600); err != nil {
			os.Exit(30)
		}
		waitSyntheticReceipt(t, filepath.Join(control, "attempt"))
		// #nosec G204 -- Same synthetic test executable; parent asks for this attempt only after sealing the job.
		late := exec.Command(exe, "-test.run=^TestSupervisorChild$")
		late.Env = append(os.Environ(), "OWA_SUPERVISOR_HELPER=late-descendant")
		result := "refused"
		if err := late.Start(); err == nil {
			result = "allowed"
		}
		// #nosec G703 -- Fixed synthetic result receipt under the validated control directory.
		if err := os.WriteFile(filepath.Join(control, "attempt-result"), []byte(result), 0o600); err != nil {
			os.Exit(31)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	default:
		os.Exit(27)
	}
}

func processAlive(t *testing.T, pid int) (alive bool) {
	t.Helper()
	if pid <= 0 || int64(pid) > 0xffffffff {
		t.Fatal("invalid tracked PID")
	}
	// #nosec G115 -- Fatal above rejects every PID outside the positive uint32 range.
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false
	}
	if err != nil {
		t.Fatal("tracked process liveness unverified")
	}
	defer func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Error("tracked process handle close failed")
			alive = false
		}
	}()
	result, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		t.Fatal("tracked process wait failed")
	}
	return result == uint32(windows.WAIT_TIMEOUT)
}
