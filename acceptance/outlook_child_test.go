//go:build acceptance

package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// The Outlook phases' own cost. The shared syncs run in one process, and a process's peak RSS only
// rises: the Teams-only sync has set it before the Outlook sync starts, and on macOS the pages Go
// frees stay resident and are reused, so a combined run says nothing about what Outlook adds, and
// anything the Teams pass of a later sync does is charged to Outlook. So the Outlook read is also
// run alone in a fresh process on a copy of the archive that holds the Teams data, and compared
// with a fresh process that does everything but the Outlook read: it opens the same archive and
// runs a sync with Outlook off and no Teams root. The difference between their peaks is what
// reading and committing the Outlook store costs. (A second Outlook sync is no baseline: the live
// store changes, and a sync with no minimum interval reads it again.)

const (
	childDBEnv   = "M365CRAWL_ACCEPTANCE_CHILD_DB"
	childRootEnv = "M365CRAWL_ACCEPTANCE_CHILD_OUTLOOK_ROOT"
	childModeEnv = "M365CRAWL_ACCEPTANCE_CHILD_MODE" // "baseline": Outlook off
	childMarker  = "M365CRAWL-CHILD-RSS"
)

// TestChildOutlookSync is the child process of childPeaks: it runs only when childDBEnv is set. It
// syncs the Outlook store alone (no Teams root) into the archive and prints its process peak RSS
// before and after, in bytes, and the error code if the sync failed. In baseline mode Outlook is
// off: the sync opens the archive, brings it up to date and finds no source, which is its error.
func TestChildOutlookSync(t *testing.T) {
	db, root := os.Getenv(childDBEnv), os.Getenv(childRootEnv)
	if db == "" || root == "" {
		t.Skip("runs only as the child process of the Outlook cost check")
	}
	before := processUsage()
	_, _, err := syncer.Run(context.Background(), syncer.Options{
		Root: filepath.Join(t.TempDir(), "no-teams"), DBPath: db,
		OutlookEnabled: os.Getenv(childModeEnv) != "baseline", OutlookRoot: root, OutlookMinReadInterval: -1,
	})
	code := "none"
	if err != nil {
		code = errCode(err)
		if os.Getenv(childModeEnv) == "baseline" && code == errs.CodeTeamsNotInstalled {
			code = "none"
		}
	}
	after := processUsage()
	fmt.Printf("%s %d %d %s %v\n", childMarker, before.peakRSS, after.peakRSS, code, before.ok && after.ok)
}

// childPeaks runs the child once and returns its peak RSS before and after the sync.
func childPeaks(db, root, mode string) (before, after int64, ok bool, failure string) {
	cmd := osexec.Command(os.Args[0], "-test.run=^TestChildOutlookSync$", "-test.count=1") //nolint:gosec // this test binary
	cmd.Env = append(os.Environ(), childDBEnv+"="+db, childRootEnv+"="+root, childModeEnv+"="+mode)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return parseChildPeaks(out.Bytes(), err)
}

func parseChildPeaks(out []byte, runErr error) (before, after int64, ok bool, failure string) {
	if runErr != nil {
		return 0, 0, false, "the child process failed"
	}
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != childMarker {
			continue
		}
		if found || len(f) != 5 {
			return 0, 0, false, "the child printed an invalid resource result"
		}
		found = true
		b, beforeErr := strconv.ParseInt(f[1], 10, 64)
		a, afterErr := strconv.ParseInt(f[2], 10, 64)
		if beforeErr != nil || afterErr != nil || b < 0 || a < 0 || (f[4] != "true" && f[4] != "false") {
			return 0, 0, false, "the child printed invalid resource metrics"
		}
		if f[3] != "none" {
			return 0, 0, false, "the child's sync failed with code " + f[3]
		}
		if f[4] == "false" {
			continue
		}
		if b == 0 || a < b {
			return 0, 0, false, "the child printed invalid resource peaks"
		}
		before, after, ok = b, a, true
	}
	if !found {
		return 0, 0, false, "the child printed no result"
	}
	return before, after, ok, ""
}

// copyArchive copies the archive database and its write-ahead files to dst.
func copyArchive(src, dst string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		in, err := os.Open(src + suffix) //nolint:gosec // the test's own scratch archive
		if err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			return err
		}
		out, err := os.Create(dst + suffix) //nolint:gosec // the test's own scratch directory
		if err == nil {
			_, err = io.Copy(out, in)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		_ = in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ownCost is the Outlook read's own peak RSS increase.
type ownCost struct {
	ok       bool
	first    int64 // peak RSS of a fresh process that reads the Outlook store
	baseline int64 // peak RSS of a fresh process that only opens the archive
	failure  string
}

func (c ownCost) over() int64 { return max(c.first-c.baseline, 0) }

// measureOwnCost runs the two children, each on its own copy of the archive taken before any
// Outlook read.
func measureOwnCost(t *testing.T, archive, root string) ownCost {
	t.Helper()
	dir := calendarScratch(t)
	db, baseDB := filepath.Join(dir, "own-cost.db"), filepath.Join(dir, "own-cost-baseline.db")
	if copyArchive(archive, db) != nil || copyArchive(archive, baseDB) != nil {
		return ownCost{failure: "the archive could not be copied for the child processes"}
	}
	_, first, ok1, fail := childPeaks(db, root, "outlook")
	if fail != "" {
		return ownCost{failure: fail}
	}
	_, base, ok2, fail := childPeaks(baseDB, root, "baseline")
	if fail != "" {
		return ownCost{failure: fail}
	}
	return ownCost{ok: ok1 && ok2, first: first, baseline: base}
}
