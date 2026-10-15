package syncer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/outlookdesktop"
	"github.com/ourostack/m365crawl/internal/outlookmail"
)

type attendeePanicReader struct {
	io.ReadCloser
	closed *bool
}

func (r *attendeePanicReader) Read([]byte) (int, error) { panic("synthetic-private-reader-panic") }
func (r *attendeePanicReader) Close() error             { *r.closed = true; return r.ReadCloser.Close() }

func TestAttendeeHashPanicClosesBeforeCleanup(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root, db := attendeeTestRoot(t, false), newDB(t)
	oldHash, oldSnapshot := peopleHashOpen, outlookSnapshot
	t.Cleanup(func() { peopleHashOpen, outlookSnapshot = oldHash, oldSnapshot })
	closed := false
	var borrowed io.ReadCloser
	peopleHashOpen = func(path string) (io.ReadCloser, error) {
		f, err := oldHash(path)
		borrowed = f
		return &attendeePanicReader{ReadCloser: f, closed: &closed}, err
	}
	t.Cleanup(func() {
		if borrowed != nil && !closed {
			_ = borrowed.Close()
		}
	})
	outlookSnapshot = func(ctx context.Context, path string) (outlookdesktop.Info, func(), error) {
		info, cleanup, err := oldSnapshot(ctx, path)
		return info, func() {
			if !closed {
				t.Error("hash panic retained reader across cleanup")
			}
			cleanup()
		}, err
	}
	if _, err := SyncAttendees(context.Background(), AttendeeOptions{DBPath: db, Root: root, Explicit: true, Discover: true}); err == nil {
		t.Fatal("panicking hash admitted native")
	}
}

func TestAttendeeMailReaderCloseIsObserved(t *testing.T) {
	isolateTmp(t)
	utcDays(t)
	root := attendeeTestRoot(t, true)
	info, cleanup, err := outlookSnapshot(context.Background(), filepath.Join(root, "Main", "HxStore.hxd"))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	old := outlookMailClose
	t.Cleanup(func() { outlookMailClose = old })
	failure := errors.New("synthetic-close")
	outlookMailClose = func(f *os.File) error { return errors.Join(f.Close(), failure) }
	if _, err := collectMail(context.Background(), info, filepath.Join(root, "Main"), "outlook/Main", outlookmail.Options{ReadBodies: true}); !errors.Is(err, failure) {
		t.Fatal("mail close failure disappeared", err)
	}
}
