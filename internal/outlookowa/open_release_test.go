package outlookowa

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func TestCanceledMailOpenPreservesUnprovedClose(t *testing.T) {
	profile := tableProfile(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open, closeOrigin := mailOpenWith, mailClose
	t.Cleanup(func() { mailOpenWith, mailClose = open, closeOrigin })
	mailOpenWith = func(dir, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
		o, err := open(dir, blob, opts)
		if err == nil {
			cancel()
		}
		return o, err
	}
	mailClose = func(o *indexeddb.Origin) error {
		if err := closeOrigin(o); err != nil {
			return err
		}
		return errors.New("private close cause")
	}
	o, err := Open(ctx, profile.Source.LevelDBDir, profile.Source.BlobDir)
	if o != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReaderClose) || strings.Contains(err.Error(), "private") {
		t.Fatal("cancellation discarded the failed-close outcome or exposed its cause")
	}
}

func TestCanceledMailOpenPreservesContextWhenClosePanics(t *testing.T) {
	profile := tableProfile(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	open, closeOrigin := mailOpenWith, mailClose
	t.Cleanup(func() { mailOpenWith, mailClose = open, closeOrigin })
	mailOpenWith = func(dir, blob string, opts indexeddb.OpenOptions) (*indexeddb.Origin, error) {
		o, err := open(dir, blob, opts)
		cancel()
		return o, err
	}
	mailClose = func(o *indexeddb.Origin) error {
		if err := closeOrigin(o); err != nil {
			return err
		}
		panic("synthetic_close_panic")
	}
	o, err := Open(ctx, profile.Source.LevelDBDir, profile.Source.BlobDir)
	if o != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReaderClose) || strings.Contains(err.Error(), "synthetic") {
		t.Fatal("close panic replaced cancellation or exposed its cause")
	}
}
