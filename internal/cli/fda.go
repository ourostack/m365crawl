package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ResponsibleApp names the app macOS will check for Full Disk Access: the first .app bundle in
// this process's parent chain, else the terminal named by TERM_PROGRAM, else a generic phrase.
func ResponsibleApp() string {
	return responsibleApp(psLookup, os.Getpid(), os.Getenv("TERM_PROGRAM"))
}

// runPS is a test seam for the ps program.
var runPS = psOutput

// psOutput returns the raw "ppid command" line for pid.
func psOutput(pid int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // G204: fixed program, pid is an integer
}

// psLookup returns a process's parent id and full command line.
func psLookup(pid int) (ppid int, command string, err error) {
	out, err := runPS(pid)
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return 0, "", fmt.Errorf("no process %d", pid)
	}
	ppid, err = strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", err
	}
	_, command, _ = strings.Cut(strings.TrimSpace(string(out)), " ")
	return ppid, strings.TrimSpace(command), nil
}

var termPrograms = map[string]string{
	"Apple_Terminal": "Terminal",
	"iTerm.app":      "iTerm",
	"vscode":         "Visual Studio Code",
	"ghostty":        "Ghostty",
}

func responsibleApp(lookup func(pid int) (int, string, error), start int, termProgram string) string {
	seen := map[int]bool{}
	for pid, steps := start, 0; pid > 1 && !seen[pid] && steps < 64; steps++ {
		seen[pid] = true
		ppid, cmd, err := lookup(pid)
		if err != nil {
			break
		}
		if before, _, ok := strings.Cut(cmd, ".app/"); ok {
			if i := strings.LastIndex(before, "/"); i >= 0 {
				before = before[i+1:]
			}
			if before != "" {
				return before
			}
		}
		pid = ppid
	}
	if termProgram != "" {
		if name, ok := termPrograms[termProgram]; ok {
			return name
		}
		return termProgram
	}
	return "the app that runs m365crawl (your terminal app; under launchd, the m365crawl program itself)"
}

// fdaFix is the remedy for no_full_disk_access, naming the app that needs the grant.
func fdaFix() string {
	return fmt.Sprintf("Open System Settings > Privacy & Security > Full Disk Access, turn it on for %s, then quit and reopen that app and run the command again.", ResponsibleApp())
}
