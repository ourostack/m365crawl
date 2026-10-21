package outlookowa

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/indexeddb"
)

func TestCanceledMailOpenRetainsUnprovedCloseOutcome(t *testing.T) {
	p := tableProfile(t)
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
	o, err := Open(ctx, p.Source.LevelDBDir, p.Source.BlobDir)
	if o != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrReaderClose) || strings.Contains(err.Error(), "private") {
		t.Fatal("canceled open lost its close outcome or exposed the raw cause")
	}
}
