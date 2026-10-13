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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/officeregistry"
	"github.com/ourostack/m365crawl/internal/sourcecapture"
)

// Finite consumer policy for the Office registry source, not observed maxima.
const (
	registryDirectoryNames = 4096
	registryLeafLimit      = 16
	registryLeafPrefix     = "MicrosoftRegistrationDB_"
	registryLeafSuffix     = ".reg"
	sqliteHeaderBytes      = 100
)

var (
	registryLimit     int64 = 64 << 20
	registryReadAt          = (*os.File).ReadAt
	registryStat            = (*os.File).Stat
	registryReadIndex       = officeregistry.ReadIndex
	registryLock            = sourcecapture.LockSQLite
	registryPrivate         = os.Open
	registryStage           = func(string) {} // observes each passed lock-safe validation
)

// registryCodes are the fixed failure codes of registry acquisition, most specific first.
var registryCodes = []string{"office_registry_policy_restore_failed", "office_registry_cleanup_failed",
	"office_registry_source_busy", "office_registry_index_too_large", "office_registry_index_unrecognized",
	"office_registry_index_unreadable", "office_registry_default_unsupported", "office_registry_unavailable",
	"office_registry_input_unqualified", "office_registry_capture_failed"}

// guardCodes translate the shared platform guard's fixed codes into registry codes.
var guardCodes = map[string]string{"office_json_policy_restore_failed": "office_registry_policy_restore_failed",
	"office_json_input_unqualified": "office_registry_input_unqualified",
	"office_json_capture_failed":    "office_registry_capture_failed"}

func registryError(ctx context.Context, code string) error {
	return errors.Join(&ReadError{Code: code}, ctx.Err())
}

func fixedCodes(err error, out map[string]bool) {
	switch typed := err.(type) { //nolint:errorlint // walks the joined tree itself to see every fixed code
	case *ReadError:
		out[firstNonEmpty(guardCodes[typed.Code], typed.Code)] = true
	case *officeregistry.ReadError:
		out[typed.Code] = true
	case interface{ Unwrap() []error }:
		for _, inner := range typed.Unwrap() {
			fixedCodes(inner, out)
		}
	case interface{ Unwrap() error }:
		fixedCodes(typed.Unwrap(), out)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// registryFailure keeps the most specific fixed registry code in err, or capture_failed.
func registryFailure(ctx context.Context, err error) error {
	codes := map[string]bool{}
	fixedCodes(err, codes)
	for _, code := range registryCodes {
		if codes[code] {
			return registryError(ctx, code)
		}
	}
	return registryError(ctx, "office_registry_capture_failed")
}

type RegistryInput struct{ Path string }

// RegistryCapture is one guarded private copy of a registry SQLite source. Its public fields are
// immutable; the original held source descriptor and its SHARED protection stay bound to it until
// the wrapper's final validation, unlock and close.
type RegistryCapture struct {
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

func validRegistryPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
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

func DiscoverRegistry(ctx context.Context) (inputs []RegistryInput, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !discoveryMac {
		return nil, registryError(ctx, "office_registry_default_unsupported")
	}
	result = withSourceLifetime(ctx, "office_registry_input_unqualified", func() error {
		return withInputGuard(ctx, func() error {
			home, err := discoveryHome()
			if err != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			directory := filepath.Join(home, "Library", "Group Containers", "UBF8T346G9.Office", "MicrosoftRegistrationDB")
			input, err := openInput(directory, true)
			if errors.Is(err, os.ErrNotExist) {
				return registryError(ctx, "office_registry_unavailable")
			}
			if err != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			names, readErr := discoveryRead(input.file(), registryDirectoryNames+1)
			closeErr := input.close()
			if len(names) > registryDirectoryNames || (readErr != nil && !errors.Is(readErr, io.EOF)) ||
				closeErr != nil || ctx.Err() != nil {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			for _, name := range names {
				if len(name) > len(registryLeafPrefix)+len(registryLeafSuffix) &&
					strings.HasPrefix(name, registryLeafPrefix) && strings.HasSuffix(name, registryLeafSuffix) {
					inputs = append(inputs, RegistryInput{Path: filepath.Join(directory, name)})
				}
			}
			if len(inputs) > registryLeafLimit {
				return registryError(ctx, "office_registry_input_unqualified")
			}
			if len(inputs) == 0 {
				return registryError(ctx, "office_registry_unavailable")
			}
			slices.SortFunc(inputs, func(a, b RegistryInput) int { return strings.Compare(a.Path, b.Path) })
			return nil
		})
	})
	if result != nil {
		return nil, registryFailure(ctx, result)
	}
	return inputs, nil
}

// ObserveRegistry is retry evidence from held metadata: the source stamp and its anchored
// companions. It never reads content.
func ObserveRegistry(ctx context.Context, path string) (signature string, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
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
		return "", registryFailure(ctx, result)
	}
	return signature, nil
}

// WithRegistryCapture holds the source lifetime, the platform guard, the original source chain and
// SQLite SHARED protection from admission through cleanup. consume may stage the decoded result
// but must not publish it: success means every late validation, private removal, unlock, source
// close and policy restoration has already succeeded.
func WithRegistryCapture(ctx context.Context, input RegistryInput, consume func(*RegistryCapture) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !validRegistryPath(input.Path) || consume == nil {
		return registryError(ctx, "office_registry_input_unqualified")
	}
	err := withSourceLifetime(ctx, "office_registry_capture_failed", func() error {
		return withInputGuard(ctx, func() error { return captureRegistry(ctx, input, consume) })
	})
	if err != nil {
		return registryFailure(ctx, err)
	}
	return nil
}

func captureRegistry(ctx context.Context, input RegistryInput, consume func(*RegistryCapture) error) (result error) {
	if ctx.Err() != nil {
		return registryError(ctx, "office_registry_capture_failed")
	}
	source, err := openInput(input.Path, false)
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
	if info.Size() > registryLimit {
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
	c := &RegistryCapture{ctx: ctx, source: source, baseline: baseline, size: info.Size(),
		signature: source.stamps[len(source.stamps)-1] + "|" + companions, modified: info.ModTime()}
	defer func() { result = errors.Join(result, c.Close()) }()
	if err := c.validateSource("locked"); err != nil {
		return err
	}
	if err := c.copyPrivate(); err != nil {
		return err
	}
	if err := c.validateSource("copied"); err != nil {
		return err
	}
	if err := c.validatePrivate(); err != nil {
		return err
	}
	if err := consume(c); err != nil {
		return registryFailure(ctx, err)
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
func (c *RegistryCapture) validateSource(stage string) error {
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

func (c *RegistryCapture) copyPrivate() error {
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
	c.privatePath = filepath.Join(c.root, "registry.db")
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

func (c *RegistryCapture) validateFields() error {
	if c.Path != c.privatePath || c.ContentDigest != c.digest || c.AcquisitionSignature != c.signature ||
		!c.AcquiredAt.Equal(c.acquired) || !c.SourceModified.Equal(c.modified) {
		return registryError(c.ctx, "office_registry_capture_failed")
	}
	return nil
}

// validatePrivate reopens only the owned private inode and compares its bytes with the copy.
func (c *RegistryCapture) validatePrivate() error {
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

// ReadRegistry parses exactly the bound private copy with the delivered reader's limits. The
// held source is validated before and after the reader, and the private copy is compared with
// the captured digest on both sides.
func (c *RegistryCapture) ReadRegistry(ctx context.Context) (officeregistry.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil || c.ctx.Err() != nil || c.validateFields() != nil {
		return officeregistry.Result{}, errors.Join(registryError(c.ctx, "office_registry_capture_failed"), ctx.Err())
	}
	if err := c.validateSource("before-reader"); err != nil {
		return officeregistry.Result{}, err
	}
	if err := c.validatePrivate(); err != nil {
		return officeregistry.Result{}, err
	}
	result, readErr := registryReadIndex(c.ctx, c.privatePath)
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return officeregistry.Result{}, errors.Join(registryError(c.ctx, "office_registry_capture_failed"), ctx.Err())
	}
	if err := c.validatePrivate(); err != nil {
		return officeregistry.Result{}, err
	}
	if err := c.validateSource("after-reader"); err != nil {
		return officeregistry.Result{}, err
	}
	if readErr != nil {
		return officeregistry.Result{}, registryFailure(c.ctx, readErr)
	}
	return result, nil
}

// Close verifies and removes only the owned private paths. The wrapper keeps the source chain and
// SHARED protection until its own final validation.
func (c *RegistryCapture) Close() error {
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
