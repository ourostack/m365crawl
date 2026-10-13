package officecapture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

const sqliteHeaderBytes = 100

// The rollback capture's hooks. The Office registry and the OneDrive usage source share this one
// lifetime, so they share its hooks; the names are the registry's, which came first.
var (
	registryReadAt  = (*os.File).ReadAt
	registryStat    = (*os.File).Stat
	registryLock    = sourcecapture.LockSQLite
	registryPrivate = os.Open
	registryStage   = func(string) {} // observes each passed lock-safe validation
)

// rollbackCapture is one guarded private copy of an ordinary rollback-journal SQLite source. Its
// public fields are immutable; the original held source descriptor and its SHARED protection stay
// bound to it until the wrapper's final validation, unlock and close. Its failures carry the
// registry's fixed codes; another family translates them at its own boundary.
type rollbackCapture struct {
	Path                 string
	ContentDigest        string
	AcquisitionSignature string
	AcquiredAt           time.Time
	SourceModified       time.Time

	mu          sync.Mutex
	ctx         context.Context
	source      *guardedInput
	baseline    string
	root        string
	privatePath string
	privateInfo os.FileInfo
	size        int64
	digest      string
	signature   string
	acquired    time.Time
	modified    time.Time
	closed      bool
	closeErr    error
}

// registryHeader reads the SQLite header through the held descriptor and accepts only rollback
// journal file formats.
func registryHeader(ctx context.Context, source *guardedInput) (string, error) {
	var header [sqliteHeaderBytes]byte
	if n, err := registryReadAt(source.file(), header[:], 0); n != len(header) || (err != nil && !errors.Is(err, io.EOF)) {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	if !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) || header[18] != 1 || header[19] != 1 {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	return fmt.Sprintf("%x", header), nil
}

// registryState is the held header plus anchored companion metadata.
func registryState(ctx context.Context, source *guardedInput) (string, error) {
	header, err := registryHeader(ctx, source)
	if err != nil {
		return "", err
	}
	companions, err := registryCompanions(ctx, source)
	if err != nil {
		return "", err
	}
	return header + "|" + companions, nil
}

// observeRollback is retry evidence from held metadata: the source stamp and its anchored
// companions. It holds the source lifetime and the platform guard, and never reads content.
func observeRollback(ctx context.Context, path string) (signature string, result error) {
	if !validRegistryPath(path) {
		return "", registryError(ctx, "office_registry_input_unqualified")
	}
	result = withSourceLifetime(ctx, "office_registry_capture_failed", func() error {
		return withInputGuard(ctx, func() error {
			input, err := openInput(path, false)
			if err != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			companions, err := registryCompanions(ctx, input)
			if err == nil {
				signature = input.stamps[len(input.stamps)-1] + "|" + companions
			}
			if closeErr := input.close(); closeErr != nil {
				err = errors.Join(err, registryError(ctx, "office_registry_capture_failed"))
			}
			return err
		})
	})
	if result != nil {
		return "", result
	}
	return signature, nil
}

// captureRollback runs inside the source lifetime and the platform guard. It keeps the original
// source chain and SQLite SHARED protection from admission through cleanup, copies at most limit
// bytes to a private file named private, and hands the bound copy to consume. consume may stage a
// decoded result but must not publish it: a nil return means every late validation, the private
// removal, the unlock and the source close have already succeeded.
func captureRollback(ctx context.Context, path string, limit int64, private string, consume func(*rollbackCapture) error) (result error) {
	if ctx.Err() != nil {
		return registryError(ctx, "office_registry_capture_failed")
	}
	source, err := openInput(path, false)
	if err != nil {
		return registryError(ctx, "office_registry_input_unqualified")
	}
	defer func() {
		if err := source.close(); err != nil {
			result = errors.Join(result, registryError(ctx, "office_registry_capture_failed"))
		}
	}()
	info, err := registryStat(source.file())
	if err != nil {
		return registryError(ctx, "office_registry_capture_failed")
	}
	if info.Size() > limit {
		return registryError(ctx, "office_registry_index_too_large")
	}
	baseline, err := registryState(ctx, source)
	if err != nil {
		return err
	}
	companions := baseline[strings.Index(baseline, "|")+1:]
	if err := source.validateSQLite(ctx); err != nil {
		return registryError(ctx, "office_registry_input_unqualified")
	}
	unlock, err := registryLock(source.file())
	if err != nil {
		return registryError(ctx, "office_registry_source_busy")
	}
	defer func() {
		if err := unlock(); err != nil {
			result = errors.Join(result, registryError(ctx, "office_registry_capture_failed"))
		}
	}()
	c := &rollbackCapture{ctx: ctx, source: source, baseline: baseline, size: info.Size(),
		signature: source.stamps[len(source.stamps)-1] + "|" + companions, modified: info.ModTime()}
	defer func() { result = errors.Join(result, c.Close()) }()
	if err := c.validateSource("locked"); err != nil {
		return err
	}
	if err := c.copyPrivate(private); err != nil {
		return err
	}
	if err := c.validateSource("copied"); err != nil {
		return err
	}
	if err := c.validatePrivate(); err != nil {
		return err
	}
	if err := consume(c); err != nil {
		return err
	}
	if err := c.validateSource("after-callback"); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return err
	}
	return c.validateSource("cleanup")
}

// validateSource checks the held chain, every anchored parent/name edge, the held header and the
// anchored companions without opening another descriptor for the source inode.
func (c *rollbackCapture) validateSource(stage string) error {
	if err := c.source.validateSQLite(c.ctx); err != nil {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	state, err := registryState(c.ctx, c.source)
	if err != nil || state != c.baseline || c.ctx.Err() != nil {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	registryStage(stage)
	return nil
}

func (c *rollbackCapture) copyPrivate(name string) error {
	root, err := jsonRoot()
	if err != nil {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	c.root = root
	// Canonicalizing our own freshly created root is not source-path repair.
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		c.root = canonical
	} else {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	c.privatePath = filepath.Join(c.root, name)
	output, err := jsonCreate(c.privatePath)
	if err != nil {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	digest := sha256.New()
	count, copyErr := copyRegistry(c.ctx, io.MultiWriter(fileWriter{output}, digest), c.source.file(), c.size)
	c.privateInfo, err = output.Stat()
	closeErr := jsonClose(output)
	if copyErr != nil || err != nil || closeErr != nil || count != c.size {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	c.acquired = jsonNow().UTC()
	c.digest = fmt.Sprintf("%x", digest.Sum(nil))
	c.Path, c.ContentDigest, c.AcquisitionSignature = c.privatePath, c.digest, c.signature
	c.AcquiredAt, c.SourceModified = c.acquired, c.modified
	return nil
}

// copyRegistry copies exactly size bytes from the held source by offset, in 64 KiB chunks, so
// the source descriptor is never reopened or repositioned.
func copyRegistry(ctx context.Context, output io.Writer, source *os.File, size int64) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for total < size {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		chunk := buffer[:min(int64(len(buffer)), size-total)]
		n, readErr := registryReadAt(source, chunk, total)
		if n < 0 || n > len(chunk) {
			return total, errors.New("input size bound")
		}
		if n > 0 {
			// Callers pass io.MultiWriter or a hash, which report every short write as an error.
			written, err := output.Write(chunk[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
		}
		if readErr != nil && (!errors.Is(readErr, io.EOF) || total != size) {
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

func (c *rollbackCapture) validateFields() error {
	if c.Path != c.privatePath || c.ContentDigest != c.digest || c.AcquisitionSignature != c.signature ||
		!c.AcquiredAt.Equal(c.acquired) || !c.SourceModified.Equal(c.modified) {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	return nil
}

// validatePrivate reopens only the owned private inode and compares its bytes with the copy.
func (c *rollbackCapture) validatePrivate() error {
	info, err := jsonLstat(c.privatePath)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(c.privateInfo, info) || info.Size() != c.size {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	private, err := registryPrivate(c.privatePath)
	if err != nil {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	digest := sha256.New()
	count, copyErr := copyRegistry(c.ctx, digest, private, c.size)
	closeErr := jsonClose(private)
	if copyErr != nil || closeErr != nil || count != c.size || fmt.Sprintf("%x", digest.Sum(nil)) != c.digest {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	return nil
}

// readBound parses exactly the bound private copy with read, the family's delivered reader. The
// held source is validated before and after the reader, and the private copy is compared with
// the captured digest on both sides. failure keeps the reader's own fixed code.
func readBound[T any](ctx context.Context, c *rollbackCapture, read func(context.Context, string) (T, error),
	failure func(context.Context, error) error) (T, error) {
	var none T
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil || c.ctx.Err() != nil || c.validateFields() != nil {
		return none, errors.Join(registryError(c.ctx, "office_registry_capture_failed"), ctx.Err())
	}
	if err := c.validateSource("before-reader"); err != nil {
		return none, err
	}
	if err := c.validatePrivate(); err != nil {
		return none, err
	}
	result, readErr := read(c.ctx, c.privatePath)
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return none, errors.Join(registryError(c.ctx, "office_registry_capture_failed"), ctx.Err())
	}
	if err := c.validatePrivate(); err != nil {
		return none, err
	}
	if err := c.validateSource("after-reader"); err != nil {
		return none, err
	}
	if readErr != nil {
		return none, failure(c.ctx, readErr)
	}
	return result, nil
}

// Close verifies and removes only the owned private paths. The wrapper keeps the source chain and
// SHARED protection until its own final validation.
func (c *rollbackCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	c.closeErr = errors.Join(c.validateFields(), c.ctx.Err())
	if c.privateInfo != nil {
		c.closeErr = errors.Join(c.closeErr, c.validatePrivate())
	}
	for _, path := range []string{c.privatePath, c.root} {
		if path == "" {
			continue
		}
		if err := jsonRemove(path); err != nil && !os.IsNotExist(err) {
			c.closeErr = errors.Join(c.closeErr, registryError(c.ctx, "office_registry_cleanup_failed"))
		}
		if _, err := jsonLstat(path); !os.IsNotExist(err) {
			c.closeErr = errors.Join(c.closeErr, registryError(c.ctx, "office_registry_cleanup_failed"))
		}
	}
	return c.closeErr
}
