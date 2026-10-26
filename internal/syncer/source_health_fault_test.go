package syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestSourceHealthRetiresMissingProfilesAndRecoveredDiscovery(t *testing.T) {
	ctx := context.Background()
	db := newDB(t)
	st, err := store.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	for _, key := range []string{"teams", "outlook", "outlook|Gone", "outlook|Gone|mail"} {
		if err := st.RecordRun(ctx, store.Run{Source: key, Status: StatusFailed, ErrorCode: "no_full_disk_access", StartedAt: time.Now(), FinishedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	r := &runner{st: st, o: Options{OutlookEnabled: true}}
	rep := Report{Status: StatusOK}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	health, err := st.SourceHealth(ctx)
	if err != nil || len(health) != 0 || len(rep.FailedSourceHealth) != 0 {
		t.Fatalf("recovered discovery retained phantom failures: %+v %v", health, err)
	}
}

func TestSourceHealthBoundarySkipsIndependentSourcesAndScopesAccounts(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	if err := st.RecordRun(ctx, store.Run{Source: "synthetic|teams", Status: StatusOK, SourceAccounts: []string{acctA.TenantID + "/" + acctA.UserID}, StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	r := &runner{st: st, o: Options{Account: &acctA}}
	rep := Report{Status: StatusPartial, Sources: []SourceReport{
		{Source: "outlook|Main|people", Status: StatusFailed, Error: &SourceError{Code: "internal"}},
		{Source: "synthetic|teams", Status: StatusFailed, Error: &SourceError{Code: "internal"}},
		{Source: "outlook|Main|mail", Status: StatusSkippedInterval},
	}}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	if len(rep.FailedSourceHealth) != 1 || rep.FailedSourceHealth[0].AccountID == nil ||
		*rep.FailedSourceHealth[0].AccountID != acctA.TenantID+"/"+acctA.UserID {
		t.Fatalf("source/account classification: %+v", rep.FailedSourceHealth)
	}
}

func TestSourceHealthMissingProfileDiscoveryFailureDoesNotRetire(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now()
	if err := st.RecordRun(ctx, store.Run{Source: "outlook|Main|mail", Status: StatusFailed, ErrorCode: "outlook_mail_layout_unsupported", StartedAt: now, FinishedAt: now}); err != nil {
		t.Fatal(err)
	}
	r := &runner{st: st, o: Options{OutlookEnabled: true}}
	rep := Report{Status: StatusOK, Sources: []SourceReport{{Source: "outlook", Status: StatusUnavailable, Error: &SourceError{Code: "no_full_disk_access"}}}}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	health, err := st.SourceHealth(ctx)
	if err != nil || len(health) != 2 {
		t.Fatalf("failed discovery erased known profile: %+v %v", health, err)
	}
}

func TestSourceHealthBoundaryErrorsSurface(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		rep  Report
		err  error
	}{
		{"discovery write", Report{}, errs.NoTeamsOrigin("synthetic")},
		{"prior read", Report{Status: StatusOK}, nil},
		{"mail fingerprint", Report{Status: StatusOK, Sources: []SourceReport{{Source: "outlook|Main|mail", Status: StatusUnchanged}}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(ctx, newDB(t))
			if err != nil {
				t.Fatal(err)
			}
			_ = st.Close()
			r := &runner{st: st}
			if err := r.recordSourceHealth(ctx, &tc.rep, tc.err); err == nil {
				t.Fatal("outcome persistence failure became a healthy result")
			}
		})
	}
	r := &runner{}
	if err := r.recordSourceHealth(ctx, &Report{}, nil); err != nil {
		t.Fatalf("no attempted source: %v", err)
	}
}

func TestSourceHealthPersistenceAndFinalReadFailuresSurface(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"write", "final read"} {
		t.Run(stage, func(t *testing.T) {
			db := newDB(t)
			st, err := store.Open(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = st.Close() }()
			sql := `create trigger synthetic_health_fault before insert on sync_runs begin select raise(fail,'synthetic refusal'); end`
			if stage == "final read" {
				sql = `create trigger synthetic_health_fault after insert on sync_runs begin update sync_runs set source_accounts_json='invalid' where id=new.id; end`
			}
			if _, err := openRaw(t, db).Exec(sql); err != nil {
				t.Fatal(err)
			}
			r := &runner{st: st}
			rep := Report{Status: StatusPartial, Sources: []SourceReport{{Source: "synthetic|teams", Status: StatusFailed}}}
			if err := r.recordSourceHealth(ctx, &rep, nil); err == nil {
				t.Fatal("outcome persistence failure became healthy")
			}
		})
	}
}

func TestSourceHealthRunReportsHealthPersistenceFailure(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	old := afterApply
	afterApply = func(*writer) {
		if _, err := openRaw(t, db).Exec(`insert into meta(key,value) values('outlook_mail_failure:outlook/Synthetic','invalid') on conflict(key) do update set value='invalid'`); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { afterApply = old })
	rep, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeDBError || rep.Status == StatusFailed {
		t.Fatalf("health persistence error was hidden or rolled back a committed source: %+v %v", rep, err)
	}
	st, openErr := store.OpenReadOnly(context.Background(), db)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = st.Close() }()
	last, readErr := st.LastSuccess(context.Background())
	if readErr != nil || !last.IsZero() {
		t.Fatalf("failed outcome persistence refreshed the archive: %v %v", last, readErr)
	}
}
