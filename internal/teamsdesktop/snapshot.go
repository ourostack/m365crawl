package teamsdesktop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/leveldb"
)

const (
	snapshotPrefix   = "teamscrawl-snapshot-"
	snapshotAttempts = 3
)

// Test seams: called at the start of each attempt and after the copy, before validation.
var (
	onSnapshotAttempt = func(int) {}
	afterCopy         = func(int) {}
)

// Snapshot copies the source into a private temp directory: <snapDir>/leveldb and
// <snapDir>/blob (open with indexeddb.Open(snapDir+"/leveldb", snapDir+"/blob")). Teams may
// write or compact while we copy, so the copy runs in a safe order (tables and logs first, then
// the MANIFEST, then CURRENT, then blobs), CURRENT is re-read afterwards, and the copy is
// validated with leveldb.Load. A copy that names missing files, has a truncated MANIFEST or saw
// CURRENT change is retried, up to three attempts, then snapshot_inconsistent.
//
// The snapshot (mode 0700) is removed by cleanup, which is always non-nil and idempotent, and
// also when ctx is cancelled.
func Snapshot(ctx context.Context, s Source) (snapDir string, cleanup func(), err error) {
	noop := func() {}
	dir, err := os.MkdirTemp("", snapshotPrefix)
	if err != nil {
		return "", noop, errs.Internal(err)
	}
	var once sync.Once
	remove := func() { once.Do(func() { _ = os.RemoveAll(dir) }) }

	var last error
	for attempt := 1; attempt <= snapshotAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			remove()
			return "", noop, err
		}
		onSnapshotAttempt(attempt)
		retry, err := copyOnce(ctx, s, dir, attempt)
		if err == nil {
			stop := context.AfterFunc(ctx, remove)
			return dir, func() { stop(); remove() }, nil
		}
		if !retry {
			remove()
			return "", noop, err
		}
		last = err
	}
	remove()
	return "", noop, errs.SnapshotInconsistent(fmt.Sprintf("the cache changed during all %d copy attempts (%v)", snapshotAttempts, last))
}

// copyOnce makes one attempt. retry is true when the failure is the kind a fresh attempt can fix.
func copyOnce(ctx context.Context, s Source, dir string, attempt int) (retry bool, err error) {
	ldbDst, blobDst := filepath.Join(dir, "leveldb"), filepath.Join(dir, "blob")
	for _, d := range []string{ldbDst, blobDst} {
		if err := os.RemoveAll(d); err != nil {
			return false, errs.Internal(err)
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return false, errs.Internal(err)
		}
	}

	if err := copyLevelDB(ctx, s.LevelDBDir, ldbDst); err != nil {
		return false, err
	}
	if s.BlobDir != "" {
		if err := copyTree(ctx, s.BlobDir, blobDst); err != nil {
			return false, err
		}
	}
	afterCopy(attempt)

	// CURRENT names the live MANIFEST; if it moved while we copied, the copy is a mix.
	copied, cerr := os.ReadFile(filepath.Join(ldbDst, "CURRENT"))    //nolint:gosec // inside our private snapshot directory
	now, nerr := os.ReadFile(filepath.Join(s.LevelDBDir, "CURRENT")) //nolint:gosec // reading Teams' CURRENT, never writing
	if cerr != nil || nerr != nil || string(copied) != string(now) {
		return true, errors.New("CURRENT changed during the copy")
	}

	// The MANIFEST grows while Teams writes. A copy whose MANIFEST is not the size of the live one
	// was taken mid-write, so take it again.
	if err := sameManifestSize(s.LevelDBDir, ldbDst, string(copied)); err != nil {
		return true, err
	}

	// Validation reads every table and log but holds no values.
	if _, err := leveldb.LoadWith(ldbDst, leveldb.LoadOptions{Keep: func([]byte) bool { return false }}); err != nil {
		var missing *leveldb.MissingFileError
		switch {
		case errors.As(err, &missing) || errors.Is(err, leveldb.ErrManifestTruncated):
			return true, err
		case errors.Is(err, leveldb.ErrUnsupportedCompression):
			return false, errs.UnsupportedBlockCompression("snapshot validation")
		case errors.Is(err, fs.ErrPermission):
			return false, errs.NoFullDiskAccess(s.LevelDBDir, err)
		default:
			return false, errs.DBError(err)
		}
	}
	return false, nil
}

// sameManifestSize compares the MANIFEST named by CURRENT in the source and in the copy.
func sameManifestSize(src, dst, current string) error {
	name := strings.TrimSpace(current)
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil // leveldb.Load reports a bad CURRENT
	}
	srcInfo, err := os.Stat(filepath.Join(src, name)) //nolint:gosec // G703: name is a bare file name checked above
	if err != nil {
		return fmt.Errorf("MANIFEST %s unreadable in the live cache: %w", name, err)
	}
	dstInfo, err := os.Stat(filepath.Join(dst, name)) //nolint:gosec // G703: name is a bare file name checked above
	if err != nil {
		return fmt.Errorf("MANIFEST %s missing from the copy: %w", name, err)
	}
	if srcInfo.Size() != dstInfo.Size() {
		return fmt.Errorf("MANIFEST %s is %d bytes in the live cache but %d in the copy", name, srcInfo.Size(), dstInfo.Size())
	}
	return nil
}

// copyLevelDB copies files in the order that keeps the copy self-consistent: data files, then
// the MANIFEST, then CURRENT. LOCK and LOG* are skipped. A file that vanishes mid-copy
// (compaction) is skipped; validation catches the case where the copied MANIFEST still needs it.
func copyLevelDB(ctx context.Context, src, dst string) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return mapFSError(src, err)
	}
	rank := func(name string) int {
		switch {
		case name == "CURRENT":
			return 2
		case strings.HasPrefix(name, "MANIFEST-"):
			return 1
		default:
			return 0
		}
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || n == "LOCK" || strings.HasPrefix(n, "LOG") {
			continue
		}
		names = append(names, n)
	}
	sort.SliceStable(names, func(i, j int) bool {
		if ri, rj := rank(names[i]), rank(names[j]); ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		if err := copyFile(ctx, filepath.Join(src, n), filepath.Join(dst, n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return mapFSError(src, err)
		}
	}
	return nil
}

func copyTree(ctx context.Context, src, dst string) error {
	err := filepath.WalkDir(src, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // deleted by Teams while we walked
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if e.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !e.Type().IsRegular() {
			return nil
		}
		if err := copyFile(ctx, path, target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no blob directory
	}
	if err != nil {
		return mapFSError(src, err)
	}
	return nil
}

// copyFile copies src to dst with mode 0600, stopping early when ctx is cancelled.
func copyFile(ctx context.Context, src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // src comes from listing the Teams directory
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return nil
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // dst is inside our private snapshot directory
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if cerr := ctx.Err(); cerr != nil {
			_ = out.Close()
			return cerr
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				_ = out.Close()
				return werr
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			_ = out.Close()
			return rerr
		}
	}
	return out.Close()
}

// mapFSError turns filesystem errors into coded ones; context errors pass through.
func mapFSError(path string, err error) error {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, fs.ErrPermission):
		return errs.NoFullDiskAccess(path, err)
	default:
		return errs.Internal(err)
	}
}

// SweepStaleSnapshots removes teamscrawl-snapshot-* directories in tmp that were last modified
// more than olderThan ago (left by a killed process) and returns how many it removed.
func SweepStaleSnapshots(tmp string, olderThan time.Duration) int {
	ents, err := os.ReadDir(tmp)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-olderThan)
	n := 0
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), snapshotPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if os.RemoveAll(filepath.Join(tmp, e.Name())) == nil {
			n++
		}
	}
	return n
}
