package cli

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/openclaw/crawlkit/output"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/syncer"
)

// A read whose refresh finds another m365crawl run holding the archive lock waits for that run, up
// to lockWait in all, polling every lockPoll. Agents run reads in parallel, so a read never fails
// because another run is refreshing the archive: once the wait is spent it answers from the archive
// as it is and says so. One budget covers every refresh of a read.
var (
	lockWait = 2 * time.Minute
	lockPoll = 250 * time.Millisecond
)

// lockWaitTestEnv shortens lockWait for the end-to-end tests, which cannot set the variable.
const lockWaitTestEnv = "M365CRAWL_TEST_LOCK_WAIT"

// The values of implicit_sync for a read whose implicit sync met another run holding the lock.
const (
	// implicitSyncWaited: another run synced the archive while this read waited, so this read
	// answers from that sync and does not sync again.
	implicitSyncWaited = "waited_for_other_sync"
	// implicitSyncOtherInProgress: another run was still syncing when this read's wait ran out, so
	// this read answers from the archive as it was.
	implicitSyncOtherInProgress = "other_sync_in_progress"
)

// lockDeadline is when this read stops waiting for other runs. It is set by the first wait.
func (rt *runtime) lockDeadline() time.Time {
	if rt.lockUntil.IsZero() {
		wait := lockWait
		if d, err := time.ParseDuration(os.Getenv(lockWaitTestEnv)); err == nil && d >= 0 {
			wait = d
		}
		rt.lockBudget, rt.lockUntil = wait, time.Now().Add(wait)
	}
	return rt.lockUntil
}

// pause sleeps one poll interval of the read's wait. It returns false, without sleeping, once the
// wait is spent, and the context's error when the read is cancelled.
func (rt *runtime) pause() (bool, error) {
	left := time.Until(rt.lockDeadline())
	if left <= 0 {
		return false, nil
	}
	t := time.NewTimer(min(lockPoll, left))
	defer t.Stop()
	select {
	case <-t.C:
		return true, nil
	case <-rt.ctx.Done():
		return false, rt.ctx.Err()
	}
}

// tryLock takes the archive lock if no other run holds it, runs inLock (when not nil) while
// holding it, and lets it go. free is false when another run still holds the lock.
func (rt *runtime) tryLock(inLock func() error) (free bool, err error) {
	release, err := store.AcquireLock(rt.dbPath)
	if isLocked(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer release()
	if inLock != nil {
		err = inLock()
	}
	return true, err
}

// printLockWait tells stderr the read is waiting for another run, as printSyncNotice does: a plain
// line in text mode, one JSON line otherwise.
func (rt *runtime) printLockWait() {
	wait := time.Until(rt.lockDeadline())
	if rt.format == output.Text {
		_, _ = fmt.Fprintln(rt.stderr, "m365crawl: waiting for another m365crawl run to finish with the archive (up to "+waitText(wait)+")")
		return
	}
	rt.writeJSONLine(struct {
		Notice         string `json:"notice"`
		MaxWaitSeconds int64  `json:"max_wait_seconds"`
	}{"waiting_for_other_sync", int64(math.Round(wait.Seconds()))})
}

// syncAfterOtherRun runs when the implicit sync found another m365crawl run holding the archive
// lock. It waits for that run. When the run leaves a complete sync newer than prev (or one younger
// than --max-age), the read uses it and does not sync again: implicit_sync is waited_for_other_sync.
// When the run leaves no such sync (it failed, or it was a fetch), the read runs its own sync and
// ran is true. When the wait is spent first, implicit_sync is other_sync_in_progress and the read
// answers from the archive as it is. err is cancellation or an archive failure, never `locked`.
func (rt *runtime) syncAfterOtherRun(prev time.Time, o syncer.Options) (rep syncer.Report, syncErr error, ran bool, err error) {
	rt.printLockWait()
	for {
		more, err := rt.pause()
		if err != nil {
			return syncer.Report{}, nil, false, err
		}
		if !more {
			rt.implicitSync = implicitSyncOtherInProgress
			return syncer.Report{}, nil, false, nil
		}
		var done bool
		free, err := rt.tryLock(func() error {
			last, pending, err := rt.lastSuccess()
			done = err == nil && !pending && !last.IsZero() && (last.After(prev) || rt.now().Sub(last) <= rt.maxAge)
			return err
		})
		if err != nil {
			return syncer.Report{}, nil, false, err
		}
		if !free {
			continue
		}
		if done {
			rt.implicitSync = implicitSyncWaited
			return syncer.Report{}, nil, false, nil
		}
		rep, _, syncErr = runSync(rt.ctx, o)
		if !isLocked(syncErr) {
			return rep, syncErr, true, nil
		}
	}
}

// besideOtherRun runs a source refresh. When the refresh finds another m365crawl run holding the
// archive lock, it waits for that run and runs the refresh again, which then refreshes only what
// that run left due. Once the read's wait is spent it gives up with `locked`, which the caller
// reports as the refresh error beside the archived sources.
func (rt *runtime) besideOtherRun(refresh func() error) error {
	err := refresh()
	for isLocked(err) {
		more, perr := rt.pause()
		if perr != nil {
			return perr
		}
		if !more {
			return errs.Locked(fmt.Sprintf("another m365crawl run still held the archive lock after this read waited %s, so these sources were not refreshed", waitText(rt.lockBudget)))
		}
		free, perr := rt.tryLock(nil)
		if perr != nil {
			return perr
		}
		if free {
			err = refresh()
		}
	}
	return err
}

// waitText is a wait for messages: whole seconds, or milliseconds below a second.
func waitText(d time.Duration) string {
	if d >= time.Second {
		return compactDuration(d.Round(time.Second))
	}
	return d.Round(time.Millisecond).String()
}

// otherSyncNotice is the result notice of a read whose wait for another run ran out.
func (rt *runtime) otherSyncNotice() string {
	return "Another m365crawl run was still syncing the archive after this read waited " + waitText(rt.lockBudget) +
		" for it, so this read did not sync and answers from the archive as it was; newer items may be missing. Run the read again once that run finishes."
}
