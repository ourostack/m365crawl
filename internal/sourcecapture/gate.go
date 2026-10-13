// Package sourcecapture owns the one process-wide lifetime for local source descriptors.
//
// POSIX record locks belong to the process: closing any descriptor for an inode releases every
// lock the process holds on it. Notes, Office JSON and Office registry captures may all select
// the same ordinary file, so each holds this gate from before its first source open until its
// last source close. The lock order is archive lock, then this gate, then SQLite byte locks; an
// owner never reacquires the gate inside its own lifetime.
package sourcecapture

import (
	"context"
	"sync"
)

var gate = make(chan struct{}, 1)

// Acquire waits for the source lifetime or for ctx. The returned release is idempotent: a
// repeated call never frees a later owner's lifetime. A cancelled ctx never receives a permit.
func Acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-gate
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { <-gate }) }, nil
}
