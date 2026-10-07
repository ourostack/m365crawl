package outlookdesktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

const (
	// snapshotPrefix is the same prefix the Teams snapshot uses, so
	// teamsdesktop.SweepStaleSnapshots also removes leftovers of a killed Outlook copy.
	snapshotPrefix = "m365crawl-snapshot-"

	// SnapshotAttempts is how many times Snapshot copies a store that keeps changing before it
	// gives up with a *StoreBusyError.
	SnapshotAttempts = 3

	// spaceHeadroom is kept free beyond the store's size, for the temp filesystem's own
	// bookkeeping and for the store growing between the size check and the end of the copy.
	spaceHeadroom = 64 << 20
)

// Test seams. Every access to Outlook's files goes through openFile and statSource, so a test
// can record the flags and paths. Nothing else touches the original.
var (
	openFile   = os.OpenFile
	removeFile = os.Remove
	createFile = func(path string) (io.WriteCloser, error) {
		return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // path is inside our private snapshot directory
	}
	statSource   = os.Stat
	sameFile     = os.SameFile
	statOpen     = func(f *os.File) (fs.FileInfo, error) { return f.Stat() }
	wait         = sleepCtx
	settle       = 250 * time.Millisecond
	retryWaits   = [SnapshotAttempts - 1]time.Duration{time.Second, 2 * time.Second}
	onAttempt    = func(int) {}
	afterCopy    = func(int) {}
	copyBufBytes = 1 << 20
)

// sleepCtx waits d, or returns the context's error as soon as ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// StoreBusyError is the typed "store busy" result: Outlook changed the container (size, modification
// time or file identity) during every one of Attempts copies, so no consistent copy was made.
// Callers can retry later. It unwraps to the coded snapshot_inconsistent error.
type StoreBusyError struct {
	Path     string
	Attempts int
	Last     string // what changed on the last attempt: sizes, times and counts only
}

func (e *StoreBusyError) Error() string {
	return fmt.Sprintf("outlookdesktop: the Outlook store changed during all %d copy attempts (%s)", e.Attempts, e.Last)
}

// Unwrap returns the coded error the CLI reports.
func (e *StoreBusyError) Unwrap() error {
	return errs.SnapshotInconsistent(fmt.Sprintf("the Outlook store changed during all %d copy attempts (%s)", e.Attempts, e.Last))
}

// InsufficientSpaceError reports that the temp filesystem cannot hold a copy of the store.
type InsufficientSpaceError struct {
	Dir   string
	Need  uint64 // bytes required, including headroom
	Avail uint64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("outlookdesktop: need %d bytes free in %s to copy the Outlook store, have %d", e.Need, e.Dir, e.Avail)
}

// Unwrap returns a coded error with a remedy.
func (e *InsufficientSpaceError) Unwrap() error {
	return &errs.Coded{Code: errs.CodeInternal, Exit: errs.ExitRuntime,
		Message: e.Error(),
		Fix:     "Free disk space on the volume that holds the temporary directory (or set TMPDIR to a volume with room), then run again."}
}

// Info describes a finished copy: sizes and times only.
type Info struct {
	Path     string    // the private copy of HxStore.hxd
	Size     int64     // bytes in the copy, equal to the source's size before and after
	ModTime  time.Time // the source's modification time, unchanged across the copy
	Attempts int       // copies made, 1 when the store was quiet
}

// Snapshot copies the Outlook store at storePath into a new private directory (mode 0700, the
// copy mode 0600, O_EXCL) and returns it. Only that one file is copied; HxStore.lock and every
// sibling file are never opened.
//
// Outlook rewrites the file in place while it runs, so the copy is accepted only when it is
// consistent: the source's size, modification time and identity must be the same before and
// after, and the bytes copied must equal that size. Otherwise the attempt is discarded and
// retried, up to SnapshotAttempts, then a *StoreBusyError is returned. Before each attempt the
// free space on the temp filesystem must cover the store plus headroom, else an
// *InsufficientSpaceError is returned.
//
// The returned cleanup is always non-nil and idempotent; the copy is also removed when ctx is
// cancelled, and on every error path before Snapshot returns.
func Snapshot(ctx context.Context, storePath string) (info Info, cleanup func(), err error) {
	noop := func() {}
	dir, err := os.MkdirTemp("", snapshotPrefix)
	if err != nil {
		return Info{}, noop, errs.Internal(err)
	}
	var once sync.Once
	remove := func() { once.Do(func() { _ = os.RemoveAll(dir) }) }
	// Every exit but success removes the copy, including a panic in the copy.
	succeeded := false
	defer func() {
		if !succeeded {
			remove()
		}
	}()
	dst := filepath.Join(dir, StoreFileName)

	var last string
	for attempt := 1; attempt <= SnapshotAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Info{}, noop, err
		}
		onAttempt(attempt)
		got, changed, err := copyOnce(ctx, storePath, dir, dst, attempt)
		if err != nil {
			return Info{}, noop, err
		}
		if changed == "" {
			got.Path = dst
			got.Attempts = attempt
			succeeded = true
			stop := context.AfterFunc(ctx, remove)
			return got, func() { stop(); remove() }, nil
		}
		last = changed
		if attempt < SnapshotAttempts {
			if err := wait(ctx, retryWaits[attempt-1]); err != nil {
				return Info{}, noop, err
			}
		}
	}
	return Info{}, noop, &StoreBusyError{Path: storePath, Attempts: SnapshotAttempts, Last: last}
}

// copyOnce makes one attempt. A non-empty changed means the source moved during the copy and a
// fresh attempt may succeed; err is a failure no retry fixes.
func copyOnce(ctx context.Context, src, dir, dst string, attempt int) (info Info, changed string, err error) {
	before, err := statSource(src)
	if err != nil {
		return Info{}, "", mapSourceError(src, err)
	}
	if !before.Mode().IsRegular() {
		return Info{}, "", errs.Internal(fmt.Errorf("%s is not a regular file", src))
	}
	avail, measured, err := freeBytes(dir)
	if err != nil {
		return Info{}, "", errs.Internal(err)
	}
	if need := uint64(before.Size()) + spaceHeadroom; measured && avail < need { //nolint:gosec // G115: Size is non-negative for a regular file
		return Info{}, "", &InsufficientSpaceError{Dir: dir, Need: need, Avail: avail}
	}
	if err := removeFile(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Info{}, "", errs.Internal(err)
	}

	in, err := openFile(src, sourceOpenFlags, 0)
	if err != nil {
		return Info{}, "", mapSourceError(src, err)
	}
	defer func() { _ = in.Close() }()
	opened, err := statOpen(in)
	if err != nil {
		return Info{}, "", errs.Internal(err)
	}
	if !opened.Mode().IsRegular() {
		return Info{}, "", errs.Internal(fmt.Errorf("%s is not a regular file", src))
	}
	if !sameFile(opened, before) {
		return Info{}, "the store file was swapped between stat and open", nil
	}
	out, err := createFile(dst)
	if err != nil {
		return Info{}, "", errs.Internal(err)
	}
	// Copy at most the size the open handle reported, so a store that grows cannot make the
	// copy outrun the disk check.
	n, err := copyStream(ctx, out, io.LimitReader(in, opened.Size()))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Info{}, "", mapCopyError(src, err)
	}
	afterCopy(attempt)

	// Settle: a write through a memory mapping may not move the modification time until the
	// system flushes it, so look again after a short pause.
	if err := wait(ctx, settle); err != nil {
		return Info{}, "", err
	}
	after, err := statSource(src)
	if err != nil {
		return Info{}, "", mapSourceError(src, err)
	}
	switch {
	case !sameFile(before, after):
		return Info{}, "the store file was replaced", nil
	case before.Size() != after.Size() || opened.Size() != before.Size():
		return Info{}, fmt.Sprintf("size %d before, %d at open, %d after", before.Size(), opened.Size(), after.Size()), nil
	case !before.ModTime().Equal(after.ModTime()) || !opened.ModTime().Equal(before.ModTime()):
		return Info{}, fmt.Sprintf("modification time moved %s during the copy", after.ModTime().Sub(before.ModTime())), nil
	case n != before.Size():
		return Info{}, fmt.Sprintf("copied %d of %d bytes", n, before.Size()), nil
	}
	return Info{Size: n, ModTime: before.ModTime()}, "", nil
}

// copyStream copies r to w in chunks, stopping early when ctx is cancelled, and returns the
// bytes written.
func copyStream(ctx context.Context, w io.Writer, r io.Reader) (int64, error) {
	buf := make([]byte, copyBufBytes)
	var total int64
	for {
		if cerr := ctx.Err(); cerr != nil {
			return total, cerr
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			total += int64(wn)
			if werr != nil {
				return total, werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
}

// mapSourceError maps a failure to stat or open the original.
func mapSourceError(path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrStoreNotFound, path)
	}
	return mapFSError(path, err)
}

// mapCopyError maps a failure during the copy; context errors pass through.
func mapCopyError(path string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return mapFSError(path, err)
}
