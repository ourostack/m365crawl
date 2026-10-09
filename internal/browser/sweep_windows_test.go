//go:build windows

package browser

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestToolhelpListReadsThisProcess(t *testing.T) {
	procs, err := toolhelpList()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.pid == os.Getpid() {
			if !slices.Equal(p.argv[1:], os.Args[1:]) {
				t.Fatalf("argv %q, want %q", p.argv, os.Args)
			}
			return
		}
	}
	t.Fatal("this process is not in the list")
}

func TestToolhelpListSkipsUnreadableProcesses(t *testing.T) {
	old := readArgs
	t.Cleanup(func() { readArgs = old })
	readArgs = func(uint32) ([]string, error) { return nil, errors.New("denied") }
	procs, err := toolhelpList()
	if err != nil || len(procs) != 0 {
		t.Fatalf("procs %v, %v", procs, err)
	}
}

func TestReadProcArgsOfAMissingProcess(t *testing.T) {
	if _, err := readProcArgs(0x7ffffff0); err == nil {
		t.Fatal("a process that does not exist has no command line")
	}
	if _, err := readProcArgs(0); err == nil { // the System Idle Process cannot be opened
		t.Fatal("the idle process has no command line to read")
	}
}

func TestOwnedByWindows(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "archive", "browser")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	want := longPath(profile)
	for argv, owned := range map[string]bool{
		"--user-data-dir=" + profile:                         true,
		"--user-data-dir=" + strings.ToUpper(profile):        true,
		"--user-data-dir=" + profile + `\`:                   true,
		"--user-data-dir=" + profile + "-other":              false,
		"--user-data-dir=" + filepath.Join(profile, "x"):     false,
		"--type=renderer|--user-data-dir=" + profile:         true,
		"--disk-cache-dir=" + profile:                        false,
		"--user-data-dir=" + filepath.Join(dir, "elsewhere"): false,
	} {
		if got := ownedBy(strings.Split(argv, "|"), want); got != owned {
			t.Errorf("%q: owned %v, want %v", argv, got, owned)
		}
	}
	// A short (8.3) name, when the volume makes one, is the same profile.
	buf := make([]uint16, windows.MAX_PATH)
	p, _ := windows.UTF16PtrFromString(profile)
	if n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf))); err == nil && n > 0 {
		if short := windows.UTF16ToString(buf); !ownedBy([]string{"--user-data-dir=" + short}, want) {
			t.Errorf("short name %s is not the profile", short)
		}
	}
}

func TestLongPathFallsBack(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "..", "gone")
	if got := longPath(missing); got != filepath.Clean(missing) {
		t.Fatalf("longPath(%q) = %q", missing, got)
	}
	if got := longPath("a\x00b"); got != "a\x00b" {
		t.Fatalf("a path with NUL = %q", got)
	}
}

func fakeProcs(t *testing.T, list func() ([]procInfo, error)) {
	t.Helper()
	old := listProcs
	t.Cleanup(func() { listProcs = old })
	listProcs = list
}

func TestSweepArgvWindowsListFails(t *testing.T) {
	fakeProcs(t, func() ([]procInfo, error) { return nil, errors.New("snapshot failed") })
	if err := sweepArgv(`C:\p`, true); err == nil {
		t.Fatal("a failed list is an error")
	}
	if !groupHasProfileProcs(`C:\p`) {
		t.Fatal("the job is always safe to end")
	}
}

func TestSweepArgvWindowsReportsSurvivor(t *testing.T) {
	oldKill, oldGrace := terminateProc, killGrace
	t.Cleanup(func() { terminateProc, killGrace = oldKill, oldGrace })
	killGrace = 50 * time.Millisecond
	var killed []uint32
	terminateProc = func(pid uint32) { killed = append(killed, pid) } // the process does not die
	fakeProcs(t, func() ([]procInfo, error) {
		return []procInfo{{pid: 4242, argv: []string{"edge", `--user-data-dir=C:\p`}},
			{pid: os.Getpid(), argv: []string{"self", `--user-data-dir=C:\p`}},
			{pid: 4343, argv: []string{"edge", `--user-data-dir=C:\q`}}}, nil
	})
	err := sweepArgv(`C:\p`, false)
	if err == nil || !strings.Contains(err.Error(), "1 browser processes survived") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(killed, []uint32{4242}) {
		t.Fatalf("killed %v", killed)
	}
}

func TestSweepArgvWindowsFinalListFails(t *testing.T) {
	oldKill, oldGrace := terminateProc, killGrace
	t.Cleanup(func() { terminateProc, killGrace = oldKill, oldGrace })
	killGrace = 20 * time.Millisecond
	terminateProc = func(uint32) {}
	calls := 0
	fakeProcs(t, func() ([]procInfo, error) {
		calls++
		if calls == 1 {
			return []procInfo{{pid: 4242, argv: []string{`--user-data-dir=C:\p`}}}, nil
		}
		return nil, errors.New("snapshot failed")
	})
	if err := sweepArgv(`C:\p`, true); err == nil {
		t.Fatal("a failed final list is an error")
	}
}

func TestTerminatePidOfAMissingProcess(t *testing.T) {
	terminatePid(0x7ffffff0) // nothing to open: nothing happens
}
