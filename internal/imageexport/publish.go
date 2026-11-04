package imageexport

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/fileprivacy"
)

type RetainedTemp struct {
	State           string
	LastKnownPath   string
	LocatorVerified *bool
}

type Effect struct {
	Phase          string
	FilePublished  *bool
	OutputVerified *bool
	RetainedTemp   *RetainedTemp
	Codes          []string
}

type Reservation struct {
	root       *os.Root
	parent     *os.File
	file       *os.File
	parentInfo os.FileInfo
	tempInfo   os.FileInfo
	parentPath string
	output     string
	temp       string
	effect     Effect
	finished   bool
}

var (
	imageWrite     = (*os.File).Write
	imageSync      = (*os.File).Sync
	imageClose     = (*os.File).Close
	imageLink      = (*os.Root).Link
	imageRemove    = (*os.Root).Remove
	imageRootClose = (*os.Root).Close
)

func Reserve(ctx context.Context, output string) (*Reservation, Effect, error) {
	effect := Effect{Phase: "admission", FilePublished: imageBool(false), OutputVerified: imageBool(false)}
	if err := ctx.Err(); err != nil {
		return nil, effect, err
	}
	if output == "" || strings.ContainsAny(output, "\x00\r\n") {
		return nil, effect, publicationError("image_output_invalid", nil)
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		return nil, effect, publicationError("image_output_invalid", err)
	}
	name, dir := filepath.Base(absolute), filepath.Dir(absolute)
	if name == "." || name == string(filepath.Separator) {
		return nil, effect, publicationError("image_output_invalid", nil)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, effect, publicationError("image_output_unprotected", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, effect, publicationError("image_output_open_failed", err)
	}
	r := &Reservation{root: root, parentPath: dir, output: name, parentInfo: info, effect: effect}
	fail := func(cause error) (*Reservation, Effect, error) {
		after, cleanupErr := r.Abort()
		return nil, after, errors.Join(cause, cleanupErr)
	}
	r.parent, err = root.Open(".")
	if err != nil {
		return fail(publicationError("image_output_open_failed", err))
	}
	bound, err := r.parent.Stat()
	if err != nil || !os.SameFile(bound, info) {
		return fail(publicationError("image_output_changed", err))
	}
	if err := fileprivacy.Parent(r.parent); err != nil {
		return fail(err)
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return fail(publicationError("image_output_exists", err))
	}
	r.temp = ".m365crawl-image-" + rand.Text()
	r.file, err = root.OpenFile(r.temp, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		r.temp = ""
		return fail(publicationError("image_output_reserve_failed", err))
	}
	r.tempInfo, err = r.file.Stat()
	if err != nil {
		return fail(publicationError("image_output_changed", err))
	}
	r.effect.Phase = "reserved"
	r.effect.RetainedTemp = &RetainedTemp{State: "empty", LastKnownPath: filepath.Join(dir, r.temp), LocatorVerified: imageBool(true)}
	if err := fileprivacy.Temporary(r.file); err != nil {
		return fail(err)
	}
	if err := r.verify(); err != nil {
		return fail(err)
	}
	return r, r.effect, nil
}

func (r *Reservation) Publish(ctx context.Context, data []byte) (Effect, error) {
	if r == nil {
		return Effect{}, publicationError("image_output_invalid", nil)
	}
	fail := func(code string, err error) (Effect, error) {
		r.effect.Codes = append(r.effect.Codes, code)
		return r.effect, publicationError(code, err)
	}
	if r.finished || r.file == nil || r.root == nil {
		return fail("image_output_closed", nil)
	}
	if err := ctx.Err(); err != nil {
		return fail("interrupted", err)
	}
	if err := r.verify(); err != nil {
		return fail("image_output_changed", err)
	}
	r.effect.Phase = "writing"
	r.effect.RetainedTemp.State = "partial"
	written, err := imageWrite(r.file, data)
	if err != nil || written != len(data) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return fail("image_output_write_failed", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("interrupted", err)
	}
	r.effect.Phase = "syncing"
	if err := imageSync(r.file); err != nil {
		return fail("image_output_sync_failed", err)
	}
	if err := fileprivacy.Temporary(r.file); err != nil {
		return fail("image_output_unprotected", err)
	}
	r.effect.Phase = "closing"
	err = imageClose(r.file)
	r.file = nil
	if err != nil {
		return fail("image_output_close_failed", err)
	}
	r.effect.RetainedTemp.State = "complete"
	if err := ctx.Err(); err != nil {
		return fail("interrupted", err)
	}
	if err := r.verify(); err != nil {
		return fail("image_output_changed", err)
	}
	r.effect.Phase = "publishing"
	linkErr := imageLink(r.root, r.temp, r.output)
	destination, statErr := r.root.Lstat(r.output)
	if statErr != nil {
		if !errors.Is(statErr, os.ErrNotExist) {
			r.effect.FilePublished = nil
		}
		return fail("image_output_publish_failed", errors.Join(linkErr, statErr))
	}
	if !os.SameFile(destination, r.tempInfo) {
		if linkErr == nil {
			r.effect.FilePublished = nil
		}
		return fail("image_output_changed", linkErr)
	}
	r.effect.FilePublished = imageBool(true)
	r.effect.Phase = "published"
	verifyErr := r.verify()
	if verifyErr == nil {
		located, err := os.Lstat(filepath.Join(r.parentPath, r.output))
		if err != nil || !os.SameFile(located, r.tempInfo) {
			verifyErr = publicationError("image_output_changed", err)
		}
	}
	r.effect.OutputVerified = imageBool(verifyErr == nil)
	cancelErr := ctx.Err()
	after, cleanupErr := r.Abort()
	return after, errors.Join(linkErr, verifyErr, cancelErr, cleanupErr)
}

func (r *Reservation) verify() error {
	if r.parent == nil || r.root == nil {
		return publicationError("image_output_closed", nil)
	}
	if err := fileprivacy.Parent(r.parent); err != nil {
		return err
	}
	located, err := os.Lstat(r.parentPath)
	if err != nil || !os.SameFile(located, r.parentInfo) {
		if r.effect.RetainedTemp != nil {
			r.effect.RetainedTemp.LocatorVerified = imageBool(false)
		}
		return publicationError("image_output_changed", err)
	}
	if r.temp != "" {
		info, err := r.root.Lstat(r.temp)
		if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, r.tempInfo) {
			if r.effect.RetainedTemp != nil {
				r.effect.RetainedTemp.LocatorVerified = imageBool(false)
			}
			return publicationError("image_output_changed", err)
		}
	}
	return nil
}

func (r *Reservation) Abort() (Effect, error) {
	if r == nil {
		return Effect{}, publicationError("image_output_invalid", nil)
	}
	if r.finished {
		return r.effect, nil
	}
	var failures []error
	record := func(code string, err error) {
		r.effect.Codes = append(r.effect.Codes, code)
		failures = append(failures, publicationError(code, err))
	}
	if r.file != nil {
		if err := imageClose(r.file); err != nil {
			record("image_output_close_failed", err)
		}
		r.file = nil
	}
	if r.temp != "" && r.root != nil {
		info, err := r.root.Lstat(r.temp)
		switch {
		case errors.Is(err, os.ErrNotExist):
			r.effect.RetainedTemp = nil
		case err != nil || r.tempInfo == nil || !os.SameFile(info, r.tempInfo):
			record("image_output_cleanup_ownership_unknown", err)
			if r.effect.RetainedTemp != nil {
				r.effect.RetainedTemp.LocatorVerified = imageBool(false)
			}
		default:
			if err := imageRemove(r.root, r.temp); err != nil {
				record("image_output_cleanup_failed", err)
			} else {
				r.effect.RetainedTemp = nil
			}
		}
	}
	if r.parent != nil {
		if err := r.parent.Close(); err != nil {
			record("image_output_close_failed", err)
		}
		r.parent = nil
	}
	if r.root != nil {
		if err := imageRootClose(r.root); err != nil {
			record("image_output_close_failed", err)
		}
		r.root = nil
	}
	r.finished = true
	return r.effect, errors.Join(failures...)
}

func imageBool(value bool) *bool { return &value }

func publicationError(code string, cause error) error {
	return errors.Join(&errs.Coded{Code: code, Exit: errs.ExitEnvironment,
		Message: "the selected image output could not be verified (" + code + ")",
		Fix:     "Use the acquisition receipt to check any retained file; an existing destination is never overwritten."}, cause)
}
