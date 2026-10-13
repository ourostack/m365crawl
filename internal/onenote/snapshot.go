package onenote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/sourcecapture"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const snapshotLimit = 64 << 20

var (
	snapshotOpen   = os.Open
	snapshotRoot   = teamsdesktop.NewPrivateSnapshotRoot
	snapshotCreate = teamsdesktop.CreatePrivateSnapshotFile
	snapshotRemove = os.Remove
	snapshotRead   = (*os.File).Read
	snapshotReadAt = (*os.File).ReadAt
	snapshotWrite  = (*os.File).Write
	snapshotClose  = (*os.File).Close
	snapshotAbs    = filepath.Abs
	snapshotEval   = filepath.EvalSymlinks
	snapshotLstat  = os.Lstat
	// POSIX locks are process-scoped: no other capture may close a descriptor for the
	// same inode while this capture's reader is still using its copy, so the capture owns
	// the shared source lifetime from before opening until dispose completes.
	snapshotAcquire = sourcecapture.Acquire
	snapshotLock    = sourcecapture.LockSQLite
)

type IndexCapture struct {
	Path                 string
	SourceModified       time.Time
	AcquisitionSignature string
	ContentDigest        string

	mu          sync.Mutex
	source      *os.File
	sourcePath  string
	info        os.FileInfo
	privateInfo os.FileInfo
	privatePath string
	root        string
	ctx         context.Context
	cancel      context.CancelFunc
	unlock      func() error
	release     func()
	closed      bool
	closeErr    error
}

func snapshotError(ctx context.Context) error {
	err := &ReadError{Code: "onenote_snapshot_unavailable"}
	if ctx.Err() != nil {
		return errors.Join(err, ctx.Err())
	}
	return err
}

// ObserveIndex examines metadata only; it never opens a source descriptor.
// Its signature is retry evidence, not proof that a SQLite snapshot is safe.
func ObserveIndex(path string) (string, error) {
	abs, err := snapshotAbs(path)
	if err != nil {
		return "", snapshotError(context.Background())
	}
	info, err := snapshotLstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 20 || info.Size() > snapshotLimit {
		return "", snapshotError(context.Background())
	}
	canonical, err := snapshotEval(abs)
	if err != nil || canonical != abs {
		return "", snapshotError(context.Background())
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s|%d|%d|%d|%s", abs, info.Size(), info.Mode(),
		info.ModTime().UnixNano(), snapshotIdentity(info))
	unsafe := false
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		companion, err := snapshotLstat(abs + suffix)
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintf(hash, "|%s:absent", suffix)
			continue
		}
		if err != nil {
			return "", snapshotError(context.Background())
		}
		_, _ = fmt.Fprintf(hash, "|%s:%d:%d:%d:%s", suffix, companion.Size(),
			companion.Mode(), companion.ModTime().UnixNano(), snapshotIdentity(companion))
		if !companion.Mode().IsRegular() || companion.Size() != 0 || suffix == "-shm" {
			unsafe = true
		}
	}
	signature := fmt.Sprintf("%x", hash.Sum(nil))
	if unsafe {
		return signature, snapshotError(context.Background())
	}
	return signature, nil
}

func CaptureIndex(ctx context.Context, path string) (_ *IndexCapture, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	release, err := snapshotAcquire(ctx)
	if err != nil {
		cancel()
		return nil, snapshotError(ctx)
	}
	capture := &IndexCapture{ctx: ctx, cancel: cancel, release: release}
	defer func() {
		if err != nil {
			err = errors.Join(err, capture.dispose())
		}
	}()
	if ctx.Err() != nil {
		return nil, snapshotError(ctx)
	}
	abs, err := snapshotAbs(path)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	signature, err := ObserveIndex(abs)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	info, err := snapshotLstat(abs)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	capture.sourcePath, capture.info = abs, info
	capture.source, err = snapshotOpen(abs)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	if err := snapshotSingleLink(capture.source); err != nil {
		return nil, snapshotError(ctx)
	}
	capture.unlock, err = snapshotLock(capture.source)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	if err := capture.validate(signature); err != nil {
		return nil, err
	}
	capture.AcquisitionSignature = signature
	capture.SourceModified = info.ModTime()
	capture.root, err = snapshotRoot()
	if err != nil {
		return nil, snapshotError(ctx)
	}
	capture.Path = filepath.Join(capture.root, "index.db")
	capture.privatePath = capture.Path
	output, err := snapshotCreate(capture.Path)
	if err != nil {
		return nil, snapshotError(ctx)
	}
	digest, copyErr := capture.copy(output)
	capture.privateInfo, err = output.Stat()
	closeErr := snapshotClose(output)
	if copyErr != nil || err != nil || closeErr != nil {
		return nil, snapshotError(ctx)
	}
	capture.ContentDigest = digest
	if err := capture.validate(signature); err != nil {
		return nil, err
	}
	if err := capture.verifyContent(); err != nil {
		return nil, err
	}
	return capture, nil
}

func (c *IndexCapture) validate(signature string) error {
	if c.ctx.Err() != nil {
		return snapshotError(c.ctx)
	}
	held, err := c.source.Stat()
	if err != nil || !os.SameFile(c.info, held) || held.Size() != c.info.Size() ||
		!held.ModTime().Equal(c.info.ModTime()) {
		return snapshotError(c.ctx)
	}
	current, err := snapshotLstat(c.sourcePath)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(held, current) {
		return snapshotError(c.ctx)
	}
	observed, err := ObserveIndex(c.sourcePath)
	if err != nil || observed != signature {
		return snapshotError(c.ctx)
	}
	var header [20]byte
	if _, err := snapshotReadAt(c.source, header[:], 0); err != nil ||
		!bytes.Equal(header[:16], []byte("SQLite format 3\x00")) || header[18] != 1 || header[19] != 1 {
		return snapshotError(c.ctx)
	}
	if c.ctx.Err() != nil {
		return snapshotError(c.ctx)
	}
	return nil
}

func (c *IndexCapture) copy(output *os.File) (string, error) {
	if _, err := c.source.Seek(0, io.SeekStart); err != nil {
		return "", snapshotError(c.ctx)
	}
	return snapshotDigest(c.ctx, c.source, output, c.info.Size())
}

func snapshotDigest(ctx context.Context, input, output *os.File, size int64) (string, error) {
	hash := sha256.New()
	buffer := make([]byte, 64<<10)
	var count int64
	for {
		if ctx.Err() != nil {
			return "", snapshotError(ctx)
		}
		n, err := snapshotRead(input, buffer)
		if n > 0 {
			count += int64(n)
			if count > size || count > snapshotLimit {
				return "", snapshotError(ctx)
			}
			_, _ = hash.Write(buffer[:n])
			if output != nil {
				written, writeErr := snapshotWrite(output, buffer[:n])
				if writeErr != nil || written != n {
					return "", snapshotError(ctx)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || n == 0 {
			return "", snapshotError(ctx)
		}
	}
	if count != size || ctx.Err() != nil {
		return "", snapshotError(ctx)
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func (c *IndexCapture) verifyContent() error {
	if c.Path != c.privatePath {
		return snapshotError(c.ctx)
	}
	digest, err := c.copy(nil)
	if err != nil || digest != c.ContentDigest {
		return snapshotError(c.ctx)
	}
	info, err := snapshotLstat(c.Path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(c.privateInfo, info) {
		return snapshotError(c.ctx)
	}
	// Only the owned copy may be reopened. Reopening the source here would
	// release process-scoped POSIX locks when that second descriptor closes.
	private, err := snapshotOpen(c.Path)
	if err != nil {
		return snapshotError(c.ctx)
	}
	digest, err = snapshotDigest(c.ctx, private, nil, c.info.Size())
	closeErr := snapshotClose(private)
	if err != nil || closeErr != nil || digest != c.ContentDigest {
		return snapshotError(c.ctx)
	}
	return nil
}

func (c *IndexCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	err := c.validate(c.AcquisitionSignature)
	if err == nil {
		err = c.verifyContent()
	}
	c.closeErr = errors.Join(err, c.dispose())
	c.closed = true
	return c.closeErr
}

func (c *IndexCapture) dispose() error {
	var err error
	if c.unlock != nil {
		if unlockErr := c.unlock(); unlockErr != nil {
			err = snapshotError(c.ctx)
		}
		c.unlock = nil
	}
	if c.source != nil {
		if closeErr := snapshotClose(c.source); closeErr != nil {
			err = errors.Join(err, snapshotError(c.ctx))
		}
		c.source = nil
	}
	for _, path := range []string{c.privatePath, c.root} {
		if path != "" {
			if removeErr := snapshotRemove(path); removeErr != nil && !os.IsNotExist(removeErr) {
				err = errors.Join(err, snapshotError(c.ctx))
			}
		}
	}
	c.cancel()
	c.release()
	return err
}
