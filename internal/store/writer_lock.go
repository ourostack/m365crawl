package store

import (
	"context"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

type WriterMode int

const (
	WriterOrdinarySync WriterMode = iota
	WriterShortServer
)

var writerNow = time.Now
var writerWait = waitWriter
var writerLock = AcquireLock

func waitWriter(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AcquireWriter coordinates real native locks, never side-file presence or age.
// The server caller must bound its write context to two seconds and close the
// store/release before any external operation or retry wait.
func AcquireWriter(ctx context.Context, path string, mode WriterMode) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode != WriterOrdinarySync && mode != WriterShortServer {
		return nil, errors.New("invalid archive writer role")
	}
	if mode == WriterShortServer {
		intent, err := writerLock(path + ".short-writer")
		if err != nil {
			return nil, err
		}
		pending, err := writerLock(path + ".sync-pending")
		if err != nil {
			intent()
			return nil, err
		}
		pending()
		global, err := writerLock(path)
		if err != nil {
			intent()
			return nil, err
		}
		return func() { global(); intent() }, nil
	}
	// Serialize the initial probe with an existing waiter's reservation. Release
	// this role as soon as the global lock is acquired; ordinary sync itself
	// still owns only the unchanged authoritative lock.
	pending, err := writerLock(path + ".sync-pending")
	if err != nil {
		return nil, err
	}
	defer pending()
	global, err := writerLock(path)
	if err == nil {
		return global, nil
	}
	if !writerContention(err) {
		return nil, err
	}
	original := err
	intent, err := writerLock(path + ".short-writer")
	if err == nil {
		intent()
		return nil, original
	}
	if !writerContention(err) {
		return nil, err
	}
	deadline := writerNow().Add(5 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !writerNow().Before(deadline) {
			return nil, original
		}
		global, err = writerLock(path)
		if err == nil {
			return global, nil
		}
		if !writerContention(err) {
			return nil, err
		}
		if err := writerWait(ctx, min(25*time.Millisecond, deadline.Sub(writerNow()))); err != nil {
			return nil, err
		}
	}
}

func writerContention(err error) bool {
	var coded *errs.Coded
	return errors.As(err, &coded) && coded.Code == "locked"
}

func WithWorkIQWriter(ctx context.Context, path string, operation func(context.Context, *Store) error) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	release, err := AcquireWriter(ctx, path, WriterShortServer)
	if err != nil {
		return err
	}
	defer release()
	s, err := Open(ctx, path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close(), ctx.Err()) }()
	return operation(ctx, s)
}
