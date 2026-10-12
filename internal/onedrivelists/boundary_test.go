package onedrivelists

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

type checkedContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int64
	at     int64
}

type scopeCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int64
}

func (c *scopeCancelContext) Err() error {
	// SQL may call Err concurrently; only the reader's own second checkpoint arms cancellation.
	if pc, _, _, ok := runtime.Caller(1); ok {
		if caller := runtime.FuncForPC(pc); caller != nil &&
			caller.Name() == "github.com/ourostack/m365crawl/internal/onedrivelists.readIndex" &&
			c.checks.Add(1) == 2 {
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestReadIndexCancellationAtScopeLoopIsDeterministic(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &scopeCancelContext{Context: parent, cancel: cancel}
	got, err := ReadIndex(ctx, fixture(t, nativeScope(siteA)))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) || ctx.checks.Load() != 2 {
		t.Fatalf("scope boundary did not cancel deterministically: %#v,%v; checkpoints=%d", got, err, ctx.checks.Load())
	}
}

func (c *checkedContext) Err() error {
	// SQL observers may check after the reader returns; only reader-owned checks inject cancellation.
	var pcs [1]uintptr
	if runtime.Callers(2, pcs[:]) != 0 {
		caller, _ := runtime.CallersFrames(pcs[:]).Next()
		const prefix = "github.com/ourostack/m365crawl/internal/onedrivelists."
		if caller.Function == prefix+"readIndex" || strings.HasPrefix(caller.Function, prefix+"readIndex.func") ||
			caller.Function == prefix+"table" || caller.Function == prefix+"safeError" {
			if c.checks.Add(1) == c.at {
				c.cancel()
			}
		}
	}
	return c.Context.Err()
}

func TestReadIndexCancellationIgnoresExternalContextChecks(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &checkedContext{Context: parent, cancel: cancel, at: 7}
	observe := func() {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for range 100 {
				_ = ctx.Err()
			}
		}()
		<-done
	}
	observe()
	if parent.Err() != nil || ctx.checks.Load() != 0 {
		t.Fatalf("external checks armed cancellation: %v; checkpoints=%d", parent.Err(), ctx.checks.Load())
	}
	got, err := ReadIndex(ctx, fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`))
	if err != nil || len(got.Scopes) != 1 || ctx.checks.Load() != 6 {
		t.Fatalf("successful source=%#v,%v; checkpoints=%d", got, err, ctx.checks.Load())
	}
	observe()
	if parent.Err() != nil || ctx.checks.Load() != 6 {
		t.Fatalf("late external checks armed cancellation: %v; checkpoints=%d", parent.Err(), ctx.checks.Load())
	}
}

func TestReadIndexCancellationAtEveryPublicationBoundary(t *testing.T) {
	path := fixture(t, nativeScope(siteA)+`CREATE TABLE "`+inventoryTable(siteA)+`"`+itemSchema+`;CREATE TABLE "`+usersTable(siteA)+`"`+userSchema+`;`)
	for at := int64(1); at <= 7; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &checkedContext{Context: parent, cancel: cancel, at: at}
		got, err := ReadIndex(ctx, path)
		cancelled := parent.Err() != nil
		cancel()
		if !cancelled {
			if at != 7 || err != nil || len(got.Scopes) != 1 || ctx.checks.Load() != 6 {
				t.Fatalf("uncancelled source=%#v,%v; threshold=%d, checkpoints=%d", got, err, at, ctx.checks.Load())
			}
			return
		}
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("cancelled partial capture=%#v,%v; threshold=%d", got, err, at)
		}
	}
	t.Fatal("cancellation sweep never reached an uncancelled publication")
}

func TestReadIndexOpenAndTerminalCloseFailures(t *testing.T) {
	path := fixture(t)
	open, abs, closeDB := indexOpen, indexAbs, indexClose
	t.Cleanup(func() { indexOpen, indexAbs, indexClose = open, abs, closeDB })
	for _, kind := range []string{"path", "open", "close", "close-cancel", "successful-close-cancel"} {
		indexOpen, indexAbs, indexClose = open, abs, closeDB
		ctx, cancel := context.WithCancel(context.Background())
		switch kind {
		case "path":
			indexAbs = func(string) (string, error) { return "", errInjected }
		case "open":
			indexOpen = func(string, string) (*sql.DB, error) { return nil, errInjected }
		default:
			indexClose = func(db *sql.DB) error {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if kind != "close" {
					cancel()
				}
				if kind == "successful-close-cancel" {
					return nil
				}
				return errInjected
			}
		}
		got, err := ReadIndex(ctx, path)
		cancel()
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("%s terminal boundary=%#v,%v", kind, got, err)
		}
	}
}

func TestReadIndexSameSiteMultipleLibrariesAndLateStringBounds(t *testing.T) {
	second := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	sql := nativeScope(siteA) +
		`INSERT INTO lists VALUES('` + second + `','web','` + siteA + `','drive','Second','site','list',NULL);` +
		`CREATE TABLE "` + inventoryTable(siteA) + `"` + itemSchema + `;CREATE TABLE "list_` + second + `_` + siteA + `_rows"` + itemSchema + `;CREATE TABLE "` + usersTable(siteA) + `"` + userSchema + `;` +
		`INSERT INTO "` + usersTable(siteA) + `" VALUES(1,'Title','Person');` +
		`INSERT INTO "` + inventoryTable(siteA) + `" VALUES(1,'u','p','A','0',NULL,NULL,NULL,NULL,NULL,NULL,NULL);`
	path := fixture(t, sql)
	base := readLimits{fileBytes: 1 << 20, fieldBytes: 1 << 20, stringBytes: 1 << 20, scopes: 10, items: 10, users: 10}
	got, err := readIndex(context.Background(), path, base)
	if err != nil || len(got.Scopes) != 2 || len(got.Users) != 1 {
		t.Fatalf("same-site user table read repeatedly: %#v,%v", got, err)
	}
	for _, budget := range []int64{205, 210, 213} {
		limits := base
		limits.stringBytes = budget
		got, err := readIndex(context.Background(), path, limits)
		if err == nil || !reflect.DeepEqual(got, Result{}) {
			t.Fatalf("late budget%d retained output: %#v,%v", budget, got, err)
		}
	}
}
