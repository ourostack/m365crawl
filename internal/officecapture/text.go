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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ourostack/m365crawl/internal/documenttext"
)

type TextInput struct{ Path, RequestedKind string }

var textOpenPrivate = openInput

func ValidTextInput(input TextInput) bool {
	return utf8.ValidString(input.Path) && len(input.Path) <= 32768 && !strings.ContainsRune(input.Path, 0) &&
		filepath.IsAbs(input.Path) && filepath.Clean(input.Path) == input.Path &&
		slices.Contains([]string{"docx", "xlsx", "pptx"}, input.RequestedKind)
}

type TextCapture struct {
	SourceByteDigest string
	Evidence         FileEvidence
	AcquiredAt       time.Time

	mu                                  sync.Mutex
	ctx                                 context.Context
	source                              *guardedInput
	root, privatePath, digest, evidence string
	privateInfo                         os.FileInfo
	acquired                            time.Time
	kind                                string
	size                                int64
	closed                              bool
	closeErr                            error
}

func textFailure(ctx context.Context, err error) error {
	code := "local_text_capture_failed"
	codes := map[string]bool{}
	fixedCodes(err, codes)
	if codes["office_registry_policy_restore_failed"] || codes["local_text_policy_restore_failed"] {
		return errors.Join(&ReadError{Code: "local_text_policy_restore_failed"}, ctx.Err())
	}
	if codes["local_text_cleanup_failed"] {
		return errors.Join(&ReadError{Code: "local_text_cleanup_failed"}, ctx.Err())
	}
	var parse *documenttext.ReadError
	var read *ReadError
	if errors.As(err, &parse) {
		code = "local_text_" + parse.Code
	} else if errors.As(err, &read) {
		switch read.Code {
		case "office_json_input_unqualified":
			code = "local_text_input_unqualified"
		default:
			if strings.HasPrefix(read.Code, "local_text_") {
				code = read.Code
			}
		}
	}
	return errors.Join(&ReadError{Code: code}, ctx.Err())
}

// WithTextCapture stages only. Success follows final validation, cleanup, source close and policy restoration.
func WithTextCapture(ctx context.Context, input TextInput, consume func(*TextCapture) error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !ValidTextInput(input) || consume == nil {
		return textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
	}
	err := withSourceLifetime(ctx, "local_text_capture_failed", func() error {
		return captureGuard(ctx, func() error { return captureText(ctx, input, consume) })
	})
	if err != nil {
		return textFailure(ctx, err)
	}
	return nil
}

func captureText(ctx context.Context, input TextInput, consume func(*TextCapture) error) (result error) {
	source, err := openInput(input.Path, false)
	if err != nil {
		return textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
	}
	defer func() { result = errors.Join(result, source.close()) }()
	if err := source.validateSQLite(ctx); err != nil {
		return textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
	}
	info, err := registryStat(source.file())
	if err != nil {
		return textFailure(ctx, err)
	}
	if info.Size() > documenttext.DefaultLimits().SourceBytes {
		return textFailure(ctx, &ReadError{Code: "local_text_too_large"})
	}
	evidence, err := heldTextEvidence(source.file())
	if err != nil {
		return textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
	}
	c := &TextCapture{ctx: ctx, source: source, Evidence: evidence, evidence: evidenceJSON(evidence), kind: input.RequestedKind, size: evidence.Size}
	defer func() { result = errors.Join(result, c.Close()) }()
	if err := c.copyPrivate(); err != nil {
		return err
	}
	if err := c.validateSource(); err != nil {
		return err
	}
	if err := consume(c); err != nil {
		return err
	}
	if err := c.validateSource(); err != nil {
		return err
	}
	if err := c.Close(); err != nil {
		return err
	}
	return c.validateSource()
}

func (c *TextCapture) copyPrivate() error {
	root, err := jsonRoot()
	if err != nil {
		return textFailure(c.ctx, err)
	}
	c.root = root
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return textFailure(c.ctx, err)
	}
	c.root = canonical
	c.privatePath = filepath.Join(c.root, "resident-ooxml")
	output, err := jsonCreate(c.privatePath)
	if err != nil {
		return textFailure(c.ctx, err)
	}
	hash := sha256.New()
	count, copyErr := copyRegistry(c.ctx, io.MultiWriter(fileWriter{output}, hash), c.source.file(), c.size)
	c.privateInfo, err = output.Stat()
	closeErr := jsonClose(output)
	if copyErr != nil || err != nil || closeErr != nil || count != c.size {
		return textFailure(c.ctx, errors.Join(copyErr, err, closeErr))
	}
	c.digest = fmt.Sprintf("%x", hash.Sum(nil))
	c.acquired = jsonNow().UTC()
	c.SourceByteDigest, c.AcquiredAt = c.digest, c.acquired
	return nil
}

func (c *TextCapture) validateFields() error {
	if c.SourceByteDigest != c.digest || evidenceJSON(c.Evidence) != c.evidence || !c.AcquiredAt.Equal(c.acquired) {
		return textFailure(c.ctx, nil)
	}
	return nil
}

func (c *TextCapture) validateSource() error {
	if err := c.source.validateSQLite(c.ctx); err != nil {
		return textFailure(c.ctx, err)
	}
	e, err := heldTextEvidence(c.source.file())
	if err != nil || evidenceJSON(e) != c.evidence {
		return textFailure(c.ctx, err)
	}
	hash := sha256.New()
	count, err := copyRegistry(c.ctx, hash, c.source.file(), c.size)
	if err != nil || count != c.size || fmt.Sprintf("%x", hash.Sum(nil)) != c.digest {
		return textFailure(c.ctx, err)
	}
	return c.validateFields()
}

func (c *TextCapture) privateReader() (*guardedInput, error) {
	info, err := jsonLstat(c.privatePath)
	if err != nil || c.privateInfo == nil || !info.Mode().IsRegular() || !os.SameFile(c.privateInfo, info) {
		return nil, textFailure(c.ctx, err)
	}
	input, err := textOpenPrivate(c.privatePath, false)
	if err != nil {
		return nil, textFailure(c.ctx, err)
	}
	held, statErr := registryStat(input.file())
	if statErr != nil || !os.SameFile(c.privateInfo, held) || held.Size() != c.size {
		return nil, errors.Join(textFailure(c.ctx, nil), input.close())
	}
	if err := input.validateSQLite(c.ctx); err != nil {
		return nil, errors.Join(textFailure(c.ctx, err), input.close())
	}
	if _, err := heldTextEvidence(input.file()); err != nil {
		return nil, errors.Join(textFailure(c.ctx, err), input.close())
	}
	hash := sha256.New()
	count, copyErr := copyRegistry(c.ctx, hash, input.file(), c.size)
	if copyErr != nil ||
		count != c.size || fmt.Sprintf("%x", hash.Sum(nil)) != c.digest {
		return nil, errors.Join(textFailure(c.ctx, nil), input.close())
	}
	return input, nil
}

func (c *TextCapture) ReadText(ctx context.Context) (documenttext.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil {
		return documenttext.Result{}, textFailure(ctx, nil)
	}
	caller := ctx
	ctx, cancel := context.WithCancel(c.ctx)
	stop := context.AfterFunc(caller, cancel)
	defer func() { stop(); cancel() }()
	var result documenttext.Result
	err := captureGuard(ctx, func() error {
		var err error
		result, err = c.readText(ctx)
		return err
	})
	if err != nil {
		return documenttext.Result{}, textFailure(ctx, err)
	}
	return result, nil
}

func (c *TextCapture) readText(ctx context.Context) (documenttext.Result, error) {
	if err := c.validateSource(); err != nil {
		return documenttext.Result{}, err
	}
	f, err := c.privateReader()
	if err != nil {
		return documenttext.Result{}, err
	}
	result, parseErr := documenttext.Extract(ctx, f.file(), c.size, c.kind, documenttext.DefaultLimits())
	closeErr := f.close()
	validateErr := c.validateSource()
	again, privateErr := c.privateReader()
	if again != nil {
		privateErr = errors.Join(privateErr, again.close())
	}
	if err := errors.Join(parseErr, closeErr, validateErr, privateErr); err != nil {
		return documenttext.Result{}, textFailure(ctx, err)
	}
	return result, nil
}

func (c *TextCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return c.closeErr
	}
	c.closed = true
	c.closeErr = errors.Join(c.validateFields(), c.ctx.Err())
	if c.privateInfo != nil {
		err := captureGuard(context.WithoutCancel(c.ctx), func() error {
			f, err := c.privateReader()
			if f != nil {
				err = errors.Join(err, f.close())
			}
			return err
		})
		c.closeErr = errors.Join(c.closeErr, err)
	}
	for _, path := range []string{c.privatePath, c.root} {
		if path == "" {
			continue
		}
		if err := jsonRemove(path); err != nil && !os.IsNotExist(err) {
			c.closeErr = errors.Join(c.closeErr, &ReadError{Code: "local_text_cleanup_failed"})
		}
		if _, err := jsonLstat(path); !os.IsNotExist(err) {
			c.closeErr = errors.Join(c.closeErr, &ReadError{Code: "local_text_cleanup_failed"})
		}
	}
	return c.closeErr
}

// ObserveText reads guarded metadata only; this is retry evidence, not currentness.
func ObserveText(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var signature string
	if !ValidTextInput(TextInput{path, "docx"}) {
		return "", textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
	}
	err := withSourceLifetime(ctx, "local_text_capture_failed", func() error {
		return captureGuard(ctx, func() error {
			source, err := openInput(path, false)
			if err != nil {
				return textFailure(ctx, &ReadError{Code: "local_text_input_unqualified"})
			}
			validateErr := source.validateSQLite(ctx)
			e, evidenceErr := heldTextEvidence(source.file())
			closeErr := source.close()
			if err := errors.Join(validateErr, evidenceErr, closeErr); err != nil {
				return err
			}
			signature = evidenceJSON(e)
			return nil
		})
	})
	if err != nil {
		return "", textFailure(ctx, err)
	}
	return signature, nil
}
