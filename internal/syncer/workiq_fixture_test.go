package syncer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

// Callback faults and retained-content fixtures reuse real handles; acquisition/open-lifecycle tests keep the production wrappers.
func workIQBoundaryFixture(t *testing.T, path string) func() {
	t.Helper()
	ctx := context.Background()
	writer, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenReadOnly(ctx, path)
	if err != nil {
		_ = writer.Close()
		t.Fatal(err)
	}
	oldRead, oldWrite := workIQRead, workIQWrite
	workIQRead = func(ctx context.Context, db string, op func(*store.Store) error) error {
		if db != path {
			return oldRead(ctx, db, op)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return op(reader)
	}
	workIQWrite = func(ctx context.Context, db string, op func(context.Context, *store.Store) error) error {
		if db != path {
			return oldWrite(ctx, db, op)
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		release, err := store.AcquireWriter(ctx, path, store.WriterShortServer)
		if err != nil {
			return err
		}
		defer release()
		return errors.Join(op(ctx, writer), ctx.Err())
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			workIQRead, workIQWrite = oldRead, oldWrite
			if err := errors.Join(reader.Close(), writer.Close()); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func workIQContentFixture(t *testing.T) (string, string) {
	t.Helper()
	path, account := workIQAcquisitionArchive(t)
	workIQBoundaryFixture(t, path)
	return path, account
}

func TestWorkIQBoundaryFixtureRetainsNativeLocksCancellationAndReadOnlyAccess(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	releaseFixture := workIQBoundaryFixture(t, db)
	defer releaseFixture()
	ctx := context.Background()
	held, err := store.AcquireWriter(ctx, db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	op := func(context.Context, *store.Store) error { called = true; return nil }
	err = workIQWrite(ctx, db, op)
	held()
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "locked" || called {
		t.Fatal("fixture bypassed a real native writer lock", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := workIQWrite(canceled, db, op); !errors.Is(err, context.Canceled) || called {
		t.Fatal("fixture admitted a canceled writer", err)
	}
	if err := workIQRead(canceled, db, func(*store.Store) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatal("fixture admitted a canceled reader", err)
	}
	if err := workIQRead(ctx, db, func(s *store.Store) error { return s.SetWorkIQEnabled(ctx, false) }); err == nil {
		t.Fatal("fixture reader could mutate the archive")
	}
	broken := errors.New("synthetic fixture operation failure")
	if err := workIQWrite(ctx, db, func(ctx context.Context, _ *store.Store) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("fixture writer lost the two-second transaction bound")
		}
		return broken
	}); !errors.Is(err, broken) {
		t.Fatal("fixture lost the operation's failure", err)
	}
	unlock, err := store.AcquireWriter(ctx, db, store.WriterOrdinarySync)
	if err != nil {
		t.Fatal("fixture retained a native writer lock after failure", err)
	}
	unlock()
}
