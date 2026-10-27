package syncer

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSourceHealthDiscoveryFailureUsesKnownAccountsAndSuccess(t *testing.T) {
	isolateTmp(t)
	ctx := context.Background()
	db := newDB(t)
	run(t, Options{Root: fixtureRoot, DBPath: db})
	old := discoverSources
	discoverSources = func(string) ([]teamsdesktop.Source, []string, error) {
		return nil, nil, &errs.Coded{Code: "no_full_disk_access", Exit: errs.ExitEnvironment}
	}
	t.Cleanup(func() { discoverSources = old })
	_, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: db})
	if err == nil {
		t.Fatal("discovery refusal became success")
	}
	st, err := store.OpenReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	health, err := st.SourceHealth(ctx)
	if err != nil || len(health) != 2 {
		t.Fatalf("known accounts were lost: %+v %v", health, err)
	}
	for _, h := range health {
		if h.AccountID == nil || h.LastOKAt == nil || h.State != "failing" {
			t.Fatalf("known success became never-succeeded: %+v", h)
		}
	}
}

func TestSourceHealthMailFingerprintRecoveryAtBoundary(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	for _, outcome := range []store.Run{
		{Source: "outlook|Main|mail", Status: StatusOK, Fingerprint: "qualified", StartedAt: now, FinishedAt: now},
		{Source: "outlook|Main|mail", Status: StatusFailed, ErrorCode: "outlook_mail_layout_unsupported", StartedAt: now, FinishedAt: now},
	} {
		if err := st.RecordRun(ctx, outcome); err != nil {
			t.Fatal(err)
		}
	}
	rep := Report{Status: StatusUnchanged, Sources: []SourceReport{{Source: "outlook|Main|mail", Status: StatusUnchanged}}}
	r := &runner{st: st}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil || len(rep.FailedSourceHealth) != 0 {
		t.Fatalf("qualified mail fingerprint did not recover: %+v %v", rep.FailedSourceHealth, err)
	}
}

func TestSourceHealthAccountFilterDoesNotInventDiscoveryIdentity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := &runner{st: st, o: Options{Account: &acctA}}
	rep := Report{}
	if err := r.recordSourceHealth(ctx, &rep, &errs.Coded{Code: "no_full_disk_access"}); err != nil {
		t.Fatal(err)
	}
	if len(rep.FailedSourceHealth) != 1 || rep.FailedSourceHealth[0].AccountID != nil {
		t.Fatalf("query filter became an observed account: %+v", rep.FailedSourceHealth)
	}
}
