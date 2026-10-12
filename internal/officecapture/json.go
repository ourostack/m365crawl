package officecapture

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

const jsonLimit = 8 << 20

var (
	jsonRoot     = teamsdesktop.NewPrivateSnapshotRoot
	jsonCreate   = teamsdesktop.CreatePrivateSnapshotFile
	jsonRead     = (*os.File).Read
	jsonWrite    = (*os.File).Write
	jsonClose    = (*os.File).Close
	jsonRemove   = os.Remove
	jsonLstat    = os.Lstat
	jsonNow      = time.Now
	jsonSettle   = settleJSON
	captureGuard = withInputGuard
)

type DocumentInput struct {
	Path        string
	Surface     officedocuments.Surface
	Application string
}

type ReadError struct{ Code string }

func (e *ReadError) Error() string { return e.Code }

func jsonError(ctx context.Context, code string) error {
	return errors.Join(&ReadError{Code: code}, ctx.Err())
}

func readerFailure(ctx context.Context, cause error) error {
	var fixed *officedocuments.ReadError
	if errors.As(cause, &fixed) && slices.Contains([]string{"office_surface_unknown", "office_json_unreadable",
		"office_json_too_large", "office_json_unrecognized"}, fixed.Code) {
		return jsonError(ctx, fixed.Code)
	}
	var capture *ReadError
	if errors.As(cause, &capture) && slices.Contains([]string{"office_json_input_unqualified", "office_json_capture_failed",
		"office_json_cleanup_failed", "office_json_policy_restore_failed", "office_surface_unknown", "office_json_unreadable",
		"office_json_too_large", "office_json_unrecognized"}, capture.Code) {
		return jsonError(ctx, capture.Code)
	}
	return jsonError(ctx, "office_json_capture_failed")
}

type guardedInput struct {
	path      string
	directory bool
	files     []*os.File
	stamps    []string
}

func (s *guardedInput) file() *os.File { return s.files[len(s.files)-1] }

func (s *guardedInput) close() error {
	var result error
	for _, f := range slices.Backward(s.files) {
		result = errors.Join(result, jsonClose(f))
	}
	s.files = nil
	return result
}

func (s *guardedInput) validate() error {
	for i, f := range s.files {
		signature, err := inputSignature(f, s.directory || i < len(s.files)-1)
		if err != nil || signature != s.stamps[i] {
			return errors.New("held input changed")
		}
	}
	again, err := openInput(s.path, s.directory)
	if err != nil {
		return err
	}
	match := slices.Equal(s.stamps, again.stamps)
	closeErr := again.close()
	if !match {
		return errors.Join(errors.New("input path changed"), closeErr)
	}
	return closeErr
}

type JSONCapture struct {
	Path                 string
	ContentDigest        string
	AcquisitionSignature string
	AcquiredAt           time.Time
	SourceModified       time.Time

	mu          sync.Mutex
	ctx         context.Context
	root        string
	privatePath string
	privateInfo os.FileInfo
	digest      string
	signature   string
	acquired    time.Time
	modified    time.Time
	surface     officedocuments.Surface
	closed      bool
	closeErr    error
}

func (c *JSONCapture) validateFields() error {
	if c.Path != c.privatePath || c.ContentDigest != c.digest ||
		c.AcquisitionSignature != c.signature || !c.AcquiredAt.Equal(c.acquired) ||
		!c.SourceModified.Equal(c.modified) {
		return jsonError(c.ctx, "office_json_capture_failed")
	}
	return nil
}

func (c *JSONCapture) validatePrivate() error {
	info, err := jsonLstat(c.privatePath)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(c.privateInfo, info) {
		return jsonError(c.ctx, "office_json_capture_failed")
	}
	input, err := openInput(c.privatePath, false)
	if err != nil {
		return jsonError(c.ctx, "office_json_capture_failed")
	}
	digest := sha256.New()
	count, copyErr := copyJSON(c.ctx, digest, input.file())
	closeErr := input.close()
	if copyErr != nil || closeErr != nil || count != c.privateInfo.Size() ||
		fmt.Sprintf("%x", digest.Sum(nil)) != c.digest {
		return jsonError(c.ctx, "office_json_capture_failed")
	}
	return nil
}

// Close removes only the immutable owned paths. The wrapper retains source and
// platform protection until its separate final validation and restoration.
func (c *JSONCapture) Close() error {
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
			c.closeErr = errors.Join(c.closeErr, jsonError(c.ctx, "office_json_cleanup_failed"))
		}
		if _, err := jsonLstat(path); !os.IsNotExist(err) {
			c.closeErr = errors.Join(c.closeErr, jsonError(c.ctx, "office_json_cleanup_failed"))
		}
	}
	return c.closeErr
}

func validDocumentInput(input DocumentInput) bool {
	return filepath.IsAbs(input.Path) && filepath.Clean(input.Path) == input.Path &&
		slices.Contains([]officedocuments.Surface{officedocuments.Recent, officedocuments.Shared, officedocuments.Recommended, officedocuments.Dialog}, input.Surface) &&
		slices.Contains([]string{"supplied", "Word", "Excel", "PowerPoint"}, input.Application)
}

func WithJSONCapture(ctx context.Context, input DocumentInput, consume func(*JSONCapture) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !validDocumentInput(input) || consume == nil {
		return jsonError(ctx, "office_json_input_unqualified")
	}
	return captureGuard(ctx, func() (result error) {
		if ctx.Err() != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		source, err := openInput(input.Path, false)
		if err != nil {
			return jsonError(ctx, "office_json_input_unqualified")
		}
		defer func() {
			if err := source.close(); err != nil {
				result = errors.Join(result, jsonError(ctx, "office_json_capture_failed"))
			}
		}()
		info, err := source.file().Stat()
		if err != nil || info.Size() > jsonLimit || info.Size() < 0 {
			return jsonError(ctx, "office_json_capture_failed")
		}
		c := &JSONCapture{ctx: ctx, signature: source.stamps[len(source.stamps)-1],
			modified: info.ModTime(), surface: input.Surface}
		defer func() { result = errors.Join(result, c.Close(), c.validateFields()) }()
		c.root, err = jsonRoot()
		if err != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		// Canonicalizing our own freshly created root is not source-path repair.
		originalRoot := c.root
		c.root, err = filepath.EvalSymlinks(c.root)
		if err != nil {
			c.root = originalRoot
			return jsonError(ctx, "office_json_capture_failed")
		}
		c.privatePath = filepath.Join(c.root, "source.json")
		output, err := jsonCreate(c.privatePath)
		if err != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		digest := sha256.New()
		count, copyErr := copyJSON(ctx, io.MultiWriter(fileWriter{output}, digest), source.file())
		c.privateInfo, err = output.Stat()
		closeErr := jsonClose(output)
		if copyErr != nil || err != nil || closeErr != nil || count != info.Size() {
			return jsonError(ctx, "office_json_capture_failed")
		}
		c.acquired = jsonNow().UTC()
		c.digest = fmt.Sprintf("%x", digest.Sum(nil))
		c.Path, c.ContentDigest, c.AcquisitionSignature = c.privatePath, c.digest, c.signature
		c.AcquiredAt, c.SourceModified = c.acquired, c.modified
		if err := jsonSettle(ctx); err != nil || source.validate() != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		if err := consume(c); err != nil {
			return readerFailure(ctx, err)
		}
		if ctx.Err() != nil || source.validate() != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		return nil
	})
}

type fileWriter struct{ file *os.File }

func (w fileWriter) Write(b []byte) (int, error) { return jsonWrite(w.file, b) }

func copyJSON(ctx context.Context, output io.Writer, source *os.File) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		n, readErr := jsonRead(source, buffer)
		if n < 0 || n > len(buffer) || total+int64(n) > jsonLimit {
			return total, errors.New("input size bound")
		}
		if n > 0 {
			written, err := output.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func settleJSON(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ObserveJSON(ctx context.Context, path string) (signature string, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result = withInputGuard(ctx, func() error {
		input, err := openInput(path, false)
		if err != nil {
			return jsonError(ctx, "office_json_input_unqualified")
		}
		signature = input.stamps[len(input.stamps)-1]
		if err := input.close(); err != nil {
			return jsonError(ctx, "office_json_capture_failed")
		}
		return nil
	})
	if result != nil {
		signature = ""
	}
	return signature, result
}
