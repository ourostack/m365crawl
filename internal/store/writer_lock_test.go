package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

func writerLocked(err error) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == "locked"
}

func TestWriterRoleReservesNormalSyncAndPreventsServerStarvation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	global := must(AcquireLock(path))
	defer global()
	intent := must(AcquireLock(path + ".short-writer"))
	defer intent()
	oldWait := writerWait
	t.Cleanup(func() { writerWait = oldWait })
	waits := 0
	writerWait = func(context.Context, time.Duration) error {
		waits++
		if release, err := AcquireWriter(ctx, path, WriterShortServer); !writerLocked(err) {
			if release != nil {
				release()
			}
			t.Fatal("server stole the reserved normal writer's turn")
		}
		if release, err := AcquireWriter(ctx, path, WriterOrdinarySync); !writerLocked(err) {
			if release != nil {
				release()
			}
			t.Fatal("second normal waiter stole the exclusive reservation")
		}
		global()
		intent()
		if release, err := AcquireWriter(ctx, path, WriterShortServer); !writerLocked(err) {
			if release != nil {
				release()
			}
			t.Fatal("server reacquired ahead of the reserved normal writer")
		}
		if release, err := AcquireWriter(ctx, path, WriterOrdinarySync); !writerLocked(err) {
			if release != nil {
				release()
			}
			t.Fatal("second normal writer bypassed the reservation after global release")
		}
		return nil
	}
	release, err := AcquireWriter(ctx, path, WriterOrdinarySync)
	if err != nil || waits != 1 {
		t.Fatal("ordinary sync did not wait for the real short-writer role")
	}
	release()
	release()
	pending := must(AcquireLock(path + ".sync-pending"))
	pending()
	short := must(AcquireWriter(ctx, path, WriterShortServer))
	short()
}

func TestWriterRoleOldSideFilesDoNotGrantWaitOrBlockServer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	stale := must(AcquireLock(path + ".short-writer"))
	stale()
	stale = must(AcquireLock(path + ".sync-pending"))
	stale()
	global := must(AcquireLock(path))
	defer global()
	oldWait := writerWait
	t.Cleanup(func() { writerWait = oldWait })
	writerWait = func(context.Context, time.Duration) error {
		t.Fatal("unheld side file granted an extra wait")
		return nil
	}
	if release, err := AcquireWriter(ctx, path, WriterOrdinarySync); !writerLocked(err) {
		if release != nil {
			release()
		}
		t.Fatal("ordinary lock contention was not immediate")
	}
	global()
	release := must(AcquireWriter(ctx, path, WriterShortServer))
	defer release()
	if probe, err := AcquireLock(path + ".short-writer"); !writerLocked(err) {
		if probe != nil {
			probe()
		}
		t.Fatal("short writer did not hold its real intent lock")
	}
}

func TestWriterRoleDeadlineCancellationAndInvalidModeReleaseConsumers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "archive.db")
	global := must(AcquireLock(path))
	defer global()
	intent := must(AcquireLock(path + ".short-writer"))
	defer intent()
	oldWait, oldNow := writerWait, writerNow
	t.Cleanup(func() { writerWait, writerNow = oldWait, oldNow })
	now := time.Unix(1, 0)
	writerNow = func() time.Time { return now }
	waits := 0
	writerWait = func(context.Context, time.Duration) error { now = now.Add(time.Second); waits++; return nil }
	if release, err := AcquireWriter(ctx, path, WriterOrdinarySync); !writerLocked(err) || waits != 5 {
		if release != nil {
			release()
		}
		t.Fatal("normal waiter did not observe its finite five-second budget")
	}
	pending := must(AcquireLock(path + ".sync-pending"))
	pending()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if release, err := AcquireWriter(cancelled, path, WriterShortServer); !errors.Is(err, context.Canceled) {
		if release != nil {
			release()
		}
		t.Fatal("cancelled writer still attempted to acquire")
	}
	if release, err := AcquireWriter(ctx, path, WriterMode(99)); err == nil {
		if release != nil {
			release()
		}
		t.Fatal("unknown writer mode accepted")
	}
}

func TestWriterRoleAcquisitionErrorsNeverRetainAConsumer(t *testing.T) {
	for _, scenario := range []string{"ordinary-free", "ordinary-wait", "server"} {
		t.Run(scenario, func(t *testing.T) {
			for fail := 1; fail <= 6; fail++ {
				t.Run(fmt.Sprint(fail), func(t *testing.T) {
					ctx := context.Background()
					path := filepath.Join(t.TempDir(), "archive.db")
					oldLock, oldWait := writerLock, writerWait
					t.Cleanup(func() { writerLock, writerWait = oldLock, oldWait })
					mode := WriterOrdinarySync
					if scenario == "server" {
						mode = WriterShortServer
					}
					if scenario == "ordinary-wait" {
						global := must(AcquireLock(path))
						defer global()
						intent := must(AcquireLock(path + ".short-writer"))
						defer intent()
						writerWait = func(context.Context, time.Duration) error { global(); intent(); return nil }
					}
					sentinel := errors.New("synthetic native IO failure")
					calls, held := 0, 0
					writerLock = func(p string) (func(), error) {
						calls++
						if calls == fail {
							return nil, sentinel
						}
						release, err := AcquireLock(p)
						if err != nil {
							return nil, err
						}
						held++
						var once sync.Once
						return func() { once.Do(func() { release(); held-- }) }, nil
					}
					release, err := AcquireWriter(ctx, path, mode)
					if calls >= fail {
						if !errors.Is(err, sentinel) || release != nil {
							t.Fatal("acquisition failure was swallowed or returned ownership")
						}
					} else {
						must0(err)
						release()
						release()
					}
					if held != 0 {
						t.Fatal("failed acquisition retained a side/global lock")
					}
					pending := must(AcquireLock(path + ".sync-pending"))
					pending()
					if scenario != "ordinary-wait" {
						intent := must(AcquireLock(path + ".short-writer"))
						intent()
					}

				})
			}
		})
	}
}

func TestWriterRoleCancellationDuringAndAfterWait(t *testing.T) {
	must0(waitWriter(context.Background(), 0))
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWriter(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("wait ignored cancellation")
	}
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprint(during), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			path := filepath.Join(t.TempDir(), "archive.db")
			global := must(AcquireLock(path))
			defer global()
			intent := must(AcquireLock(path + ".short-writer"))
			defer intent()
			oldWait := writerWait
			t.Cleanup(func() { writerWait = oldWait })
			writerWait = func(context.Context, time.Duration) error {
				cancel()
				if during {
					return ctx.Err()
				}
				return nil
			}
			if release, err := AcquireWriter(ctx, path, WriterOrdinarySync); !errors.Is(err, context.Canceled) {
				if release != nil {
					release()
				}
				t.Fatal("writer wait ignored cancellation")
			}
			pending := must(AcquireLock(path + ".sync-pending"))
			pending()
		})
	}
}

func TestWriterShortOperationBoundsContextRollsBackAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.db")
	oldLock := writerLock
	t.Cleanup(func() { writerLock = oldLock })
	lockTime := time.Duration(0)
	writerLock = func(path string) (func(), error) {
		start := time.Now()
		release, err := oldLock(path)
		lockTime += time.Since(start)
		return release, err
	}
	start := time.Now()
	firstEntered := false
	firstErr := WithWorkIQWriter(context.Background(), path, func(ctx context.Context, s *Store) error {
		firstEntered = true
		deadline, ok := ctx.Deadline()
		t.Logf("short-writer initial callback: elapsed=%s locks=%s remaining=%s", time.Since(start), lockTime, time.Until(deadline))
		if !ok || time.Until(deadline) > WorkIQWriteBudget || time.Until(deadline) <= 0 {
			t.Fatal("short writer has no bounded operation budget")
		}
		return s.SetWorkIQEnabled(ctx, true)
	})
	t.Logf("short-writer initial result: elapsed=%s locks=%s entered=%t err=%T:%v", time.Since(start), lockTime, firstEntered, firstErr, firstErr)
	must0(firstErr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start, lockTime = time.Now(), 0
	entered, inserted, canceled := false, false, false
	err := WithWorkIQWriter(ctx, path, func(ctx context.Context, s *Store) error {
		entered = true
		deadline, _ := ctx.Deadline()
		t.Logf("short-writer cancellation callback: elapsed=%s locks=%s remaining=%s", time.Since(start), lockTime, time.Until(deadline))
		return s.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `insert into meta(key,value) values('short-writer-rollback','unsafe')`)
			if err != nil {
				return err
			}
			inserted = true
			cancel()
			canceled = true
			return ctx.Err()
		})
	})
	t.Logf("short-writer cancellation result: elapsed=%s locks=%s entered=%t inserted=%t canceled=%t parent=%v is_canceled=%t is_deadline=%t err=%T:%v",
		time.Since(start), lockTime, entered, inserted, canceled, ctx.Err(), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded), err, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled short write was not surfaced", err)
	}
	s := must(Open(context.Background(), path))
	defer func() { _ = s.Close() }()
	if rowCount(t, s, `select count(*) from meta where key='short-writer-rollback'`) != 0 {
		t.Fatal("cancelled short operation committed its transaction")
	}
	release := must(AcquireLock(path))
	release()
	intent := must(AcquireLock(path + ".short-writer"))
	intent()
}

func TestWriterShortOperationOpenAndLockErrorsDoNotRunCallback(t *testing.T) {
	for _, badArchive := range []bool{false, true} {
		t.Run(fmt.Sprint(badArchive), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive.db")
			if badArchive {
				must0(os.WriteFile(path, []byte("synthetic corrupt archive"), 0o600))
			} else {
				release := must(AcquireLock(path))
				defer release()
			}
			if err := WithWorkIQWriter(context.Background(), path, func(context.Context, *Store) error {
				t.Fatal("failed writer acquisition/open reached callback")
				return nil
			}); err == nil {
				t.Fatal("short writer swallowed lock/archive failure")
			}
			intent := must(AcquireLock(path + ".short-writer"))
			intent()
			pending := must(AcquireLock(path + ".sync-pending"))
			pending()
		})
	}
}
