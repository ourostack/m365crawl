package syncer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/store"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

// The sync has test seams (beforeRead, afterApply and the like) that are package variables, so a
// test that assigned one directly could not run beside any other test. A sync runs entirely on
// the goroutine that calls Run, so each seam is installed once for the whole process as a
// dispatcher that calls the function registered by the calling goroutine, if any, and otherwise
// the original. A test then sees only its own seams, and tests that use them can run in parallel.

// goroutineID returns the id of the calling goroutine.
func goroutineID() uint64 {
	var buf [64]byte
	b := bytes.TrimPrefix(buf[:runtime.Stack(buf[:], false)], []byte("goroutine "))
	id, err := strconv.ParseUint(string(b[:bytes.IndexByte(b, ' ')]), 10, 64)
	if err != nil {
		panic(err)
	}
	return id
}

// hookSlot holds the seam functions registered by test goroutines.
type hookSlot[F any] struct {
	mu sync.RWMutex
	by map[uint64]F
}

func (s *hookSlot[F]) get() (f F, ok bool) {
	id := goroutineID()
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok = s.by[id]
	return f, ok
}

// set registers f for the calling goroutine; it lasts until clear or the end of the test.
func (s *hookSlot[F]) set(t *testing.T, f F) {
	t.Helper()
	id := goroutineID()
	s.mu.Lock()
	if s.by == nil {
		s.by = map[uint64]F{}
	}
	s.by[id] = f
	s.mu.Unlock()
	t.Cleanup(func() { s.clearFor(id) })
}

// clear removes the calling goroutine's function: the seam is the original again.
func (s *hookSlot[F]) clear() { s.clearFor(goroutineID()) }

func (s *hookSlot[F]) clearFor(id uint64) {
	s.mu.Lock()
	delete(s.by, id)
	s.mu.Unlock()
}

type readGenericFunc = func(context.Context, string, *teamsdesktop.Account, int64, teamsdesktop.GenericOptions, func(teamsdesktop.GenericRecord) error) (teamsdesktop.GenericResult, error)

var (
	hookBeforeRead   hookSlot[func(*writer)]
	hookAfterApply   hookSlot[func(*writer)]
	hookBeforeFlush  hookSlot[func(string, int) error]
	hookAfterSnap    hookSlot[func()]
	hookEnsureCal    hookSlot[func(context.Context, *store.Store, time.Time) (*store.CalendarResult, error)]
	hookCalendarZone hookSlot[func() *time.Location]
	hookRederive     hookSlot[func(context.Context, *store.Store) (*store.Migration, error)]
	hookReadGeneric  hookSlot[readGenericFunc]
	hookConflict     hookSlot[func(*memo) bool]
	hookDenied       hookSlot[func(string) bool]
	hookMemoSig      hookSlot[func(int) []byte]

	// origMemoSignature is the real signature, for a test that wants "the real one plus a bump".
	origMemoSignature func(int) []byte
	origDenied        func(string) bool
)

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

func runTests(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "teamscrawl-syncer-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	for _, k := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(k, tmp); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	origBeforeRead, origAfterApply, origBeforeFlush, origAfterSnap := beforeRead, afterApply, beforeFlush, afterSnapshot
	origEnsure, origZone, origRederive := ensureCalendar, calendarZone, rederiveArchive
	origReadGeneric, origConflict := readGenericFn, conflictFn
	origMemoSignature, origDenied = memoSignature, deniedFn
	readGenericFn = func(ctx context.Context, root string, acct *teamsdesktop.Account, max int64, opts teamsdesktop.GenericOptions, emit func(teamsdesktop.GenericRecord) error) (teamsdesktop.GenericResult, error) {
		if f, ok := hookReadGeneric.get(); ok {
			return f(ctx, root, acct, max, opts, emit)
		}
		return origReadGeneric(ctx, root, acct, max, opts, emit)
	}
	conflictFn = func(m *memo) bool {
		if f, ok := hookConflict.get(); ok {
			return f(m)
		}
		return origConflict(m)
	}
	deniedFn = func(name string) bool {
		if f, ok := hookDenied.get(); ok {
			return f(name)
		}
		return origDenied(name)
	}
	memoSignature = func(v int) []byte {
		if f, ok := hookMemoSig.get(); ok {
			return f(v)
		}
		return origMemoSignature(v)
	}
	beforeRead = func(w *writer) {
		if f, ok := hookBeforeRead.get(); ok {
			f(w)
			return
		}
		origBeforeRead(w)
	}
	afterApply = func(w *writer) {
		if f, ok := hookAfterApply.get(); ok {
			f(w)
			return
		}
		origAfterApply(w)
	}
	beforeFlush = func(kind string, n int) error {
		if f, ok := hookBeforeFlush.get(); ok {
			return f(kind, n)
		}
		return origBeforeFlush(kind, n)
	}
	afterSnapshot = func() {
		if f, ok := hookAfterSnap.get(); ok {
			f()
			return
		}
		origAfterSnap()
	}
	ensureCalendar = func(ctx context.Context, st *store.Store, at time.Time) (*store.CalendarResult, error) {
		if f, ok := hookEnsureCal.get(); ok {
			return f(ctx, st, at)
		}
		return origEnsure(ctx, st, at)
	}
	calendarZone = func() *time.Location {
		if f, ok := hookCalendarZone.get(); ok {
			return f()
		}
		return origZone()
	}
	rederiveArchive = func(ctx context.Context, st *store.Store) (*store.Migration, error) {
		if f, ok := hookRederive.get(); ok {
			return f(ctx, st)
		}
		return origRederive(ctx, st)
	}

	code := m.Run()
	if left, _ := filepath.Glob(filepath.Join(tmp, "teamscrawl-snapshot-*")); len(left) != 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "snapshots left behind by the tests: %v\n", left)
		code = 1
	}
	return code
}
