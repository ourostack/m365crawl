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

	"github.com/ourostack/m365crawl/internal/syncer"
)

// The Outlook phases' own cost. The shared syncs run in one process, and a process's peak RSS only
// rises: the Teams-only sync has set it before the Outlook sync starts, and on macOS the pages Go
// frees stay resident and are reused, so a combined run says nothing about what Outlook adds, and
// anything the Teams pass of a later sync does is charged to Outlook. So the Outlook read is also
// run alone, twice, each time in a fresh process on a copy of the archive that holds the Teams
// data: the first run reads the store, the second finds it unchanged and reads nothing. The
// difference between their peaks is what reading and committing the Outlook store costs.

const (
	childDBEnv   = "M365CRAWL_ACCEPTANCE_CHILD_DB"
	childRootEnv = "M365CRAWL_ACCEPTANCE_CHILD_OUTLOOK_ROOT"
	childMarker  = "M365CRAWL-CHILD-RSS"
)

// TestChildOutlookSync is the child process of childPeaks: it runs only when childDBEnv is set. It
// syncs the Outlook store alone (no Teams root) into the archive and prints its process peak RSS
// before and after, in bytes, and the error code if the sync failed.
func TestChildOutlookSync(t *testing.T) {
	db, root := os.Getenv(childDBEnv), os.Getenv(childRootEnv)
	if db == "" || root == "" {
		t.Skip("runs only as the child process of the Outlook cost check")
	}
	before := processUsage()
	_, _, err := syncer.Run(context.Background(), syncer.Options{
		Root: filepath.Join(t.TempDir(), "no-teams"), DBPath: db,
		OutlookEnabled: true, OutlookRoot: root, OutlookMinReadInterval: -1,
	})
	code := "none"
	if err != nil {
		code = errCode(err)
	}
	after := processUsage()
	fmt.Printf("%s %d %d %s %v\n", childMarker, before.peakRSS, after.peakRSS, code, before.ok && after.ok)
}

// childPeaks runs the child once and returns its peak RSS before and after the sync.
func childPeaks(db, root string) (before, after int64, ok bool, failure string) {
	cmd := osexec.Command(os.Args[0], "-test.run=^TestChildOutlookSync$", "-test.count=1") //nolint:gosec // this test binary
	cmd.Env = append(os.Environ(), childDBEnv+"="+db, childRootEnv+"="+root)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, 0, false, "the child process failed"
	}
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != childMarker {
			continue
		}
		before, _ = strconv.ParseInt(f[1], 10, 64)
		after, _ = strconv.ParseInt(f[2], 10, 64)
		if f[3] != "none" {
			return before, after, false, "the child's sync failed with code " + f[3]
		}
		return before, after, f[4] == "true", ""
	}
	return 0, 0, false, "the child printed no result"
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
	baseline int64 // peak RSS of a fresh process that finds it unchanged
	failure  string
}

func (c ownCost) over() int64 { return max(c.first-c.baseline, 0) }

// measureOwnCost runs the two children on a copy of the archive taken before any Outlook read.
func measureOwnCost(t *testing.T, archive, root string) ownCost {
	t.Helper()
	db := filepath.Join(calendarScratch(t), "own-cost.db")
	if err := copyArchive(archive, db); err != nil {
		return ownCost{failure: "the archive could not be copied for the child processes"}
	}
	_, first, ok1, fail := childPeaks(db, root)
	if fail != "" {
		return ownCost{failure: fail}
	}
	_, base, ok2, fail := childPeaks(db, root)
	if fail != "" {
		return ownCost{failure: fail}
	}
	return ownCost{ok: ok1 && ok2, first: first, baseline: base}
}
