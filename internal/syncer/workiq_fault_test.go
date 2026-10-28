package syncer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQAcquisitionEveryArchiveBoundaryFailsClosed(t *testing.T) {
	originalRead, originalWrite := workIQRead, workIQWrite
	t.Cleanup(func() { workIQRead, workIQWrite = originalRead, originalWrite })
	reads, writes := 0, 0
	workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
		reads++
		return originalRead(ctx, path, op)
	}
	workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
		writes++
		return originalWrite(ctx, path, op)
	}
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	if _, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account}); err != nil {
		t.Fatal(err)
	}
	maxReads, maxWrites := reads, writes
	workIQRead, workIQWrite = originalRead, originalWrite
	for _, boundary := range []struct {
		name string
		max  int
	}{{"read", maxReads}, {"write", maxWrites}} {
		for at := 1; at <= boundary.max; at++ {
			t.Run(fmt.Sprintf("%s-%d", boundary.name, at), func(t *testing.T) {
				db, account := workIQAcquisitionArchive(t)
				workIQAcquisitionStub(t)
				failure := errors.New("synthetic archive boundary failure")
				calls := 0
				workIQRead = func(ctx context.Context, path string, op func(*store.Store) error) error {
					if boundary.name == "read" {
						calls++
						if calls == at {
							return failure
						}
					}
					return originalRead(ctx, path, op)
				}
				workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
					if boundary.name == "write" {
						calls++
						if calls == at {
							return failure
						}
					}
					return originalWrite(ctx, path, op)
				}
				report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
				workIQRead, workIQWrite = originalRead, originalWrite
				if !errors.Is(err, failure) {
					t.Fatal("an archive boundary failure became empty success or lost its cause", err)
				}
				for _, account := range report.Accounts {
					if account.Status == "ok" || account.Status == "ok_with_omissions" {
						t.Fatal("failed acquisition reported a positive account outcome")
					}
				}
				if n := count(t, db, `select count(*) from sync_runs where source like 'workiq:%' and status in ('ok','ok_with_omissions')`); n != 0 {
					t.Fatal("failed acquisition recorded a covering source success")
				}
				if n := count(t, db, `select count(*) from mail_messages`); n > 1 {
					t.Fatal("failure/recovery duplicated canonical source data")
				}
				release, err := store.AcquireWriter(context.Background(), db, store.WriterOrdinarySync)
				if err != nil {
					t.Fatal("failed acquisition retained a writer consumer", err)
				}
				release()
			})
		}
	}
}

func TestWorkIQAcquisitionOpenAndRepinFailuresLeaveNoSuccess(t *testing.T) {
	original := workIQOpen
	t.Cleanup(func() { workIQOpen = original })
	for _, at := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			if at == 3 {
				t.Setenv("WORKIQ_ACQUISITION_FAIL_ONCE", filepath.Join(t.TempDir(), "failed-once"))
			}
			failure := errors.New("synthetic client open failure")
			calls := 0
			workIQOpen = func(ctx context.Context, inv workiq.Invocation, account string) (*workiq.Client, error) {
				calls++
				if calls == at {
					return nil, failure
				}
				return original(ctx, inv, account)
			}
			_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
			workIQOpen = original
			if !errors.Is(err, failure) || count(t, db, `select count(*) from mail_messages`) != 0 {
				t.Fatal("client open/pin failure admitted source data or lost its cause", err)
			}
		})
	}
	session := &workIQSession{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := session.fetch(ctx, "/me"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled session performed initialization")
	}
}

func TestWorkIQAcquisitionRetainsNativeFailureCodeWithoutInvalidGapMetadata(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	original := workIQWrite
	t.Cleanup(func() { workIQWrite = original })
	calls := 0
	workIQWrite = func(ctx context.Context, path string, op func(context.Context, *store.Store) error) error {
		calls++
		if calls == 3 {
			return &errs.Coded{Code: "locked", Exit: errs.ExitEnvironment, Message: "synthetic held archive"}
		}
		return original(ctx, path, op)
	}
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "locked" ||
		count(t, db, `select count(*) from sync_runs where source like 'workiq:%' and status='failed' and error_code='locked'`) != 1 {
		t.Fatal("native source failure was lost or prevented its own failed outcome from persisting")
	}
	if report.Gaps["workiq_failed"] != 1 || report.Gaps["workiq_persistence_failed"] != 0 {
		t.Fatal("non-WorkIQ code was written as an invalid gap or invented a persistence failure")
	}
}
