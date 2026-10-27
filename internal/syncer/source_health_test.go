package syncer

import (
	"context"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestSourceHealthDiscoveryFailurePersists(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	old := discoverSources
	discoverSources = func(string) ([]teamsdesktop.Source, []string, error) {
		return nil, nil, &errs.Coded{Code: "no_full_disk_access", Exit: errs.ExitEnvironment, Message: "Synthetic refusal"}
	}
	t.Cleanup(func() { discoverSources = old })
	_, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db})
	if err == nil {
		t.Fatal("discovery refusal became success")
	}
	st, err := store.OpenReadOnly(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	rows, err := st.SourceHealth(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Source != "teams" ||
		rows[0].AccountID != nil || rows[0].State != "never_succeeded" || rows[0].LastErrorCode != "no_full_disk_access" {
		t.Fatalf("discovery failure was lost: %+v %v", rows, err)
	}
}

func TestSourceHealthSyncReportAndRecovery(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "HxStore.hxd")
	o := outlookOpts(db, root)
	_, _ = run(t, o)
	putOutlookStore(t, root, "store-version-j.hxd")
	rep, _, _ := Run(context.Background(), o)
	if len(rep.FailedSourceHealth) == 0 || rep.FailedSourceHealth[0].LastOKAt == nil {
		t.Fatalf("sync report hid source failure: %+v", rep.FailedSourceHealth)
	}
	putOutlookStore(t, root, "HxStore.hxd")
	rep, _ = run(t, o)
	if len(rep.FailedSourceHealth) != 0 {
		t.Fatalf("successful read retained failure: %+v", rep.FailedSourceHealth)
	}
}

func TestSourceHealthWholeProfileFailureCoversMailAndDisabledReportDoesNot(t *testing.T) {
	isolateTmp(t)
	db := newDB(t)
	root := outlookRoot(t, "store-version-j.hxd")
	o := outlookOpts(db, root)
	rep, _, _ := Run(context.Background(), o)
	mail := false
	for _, h := range rep.FailedSourceHealth {
		if h.Source == "outlook_mail" {
			mail = true
		}
	}
	if !mail {
		t.Fatalf("whole-profile refusal hid unreadable mail: %+v", rep.FailedSourceHealth)
	}
	rep, _ = run(t, Options{Root: fixtureRoot, DBPath: db})
	if len(rep.FailedSourceHealth) != 0 {
		t.Fatalf("disabled source leaked into current sync health: %+v", rep.FailedSourceHealth)
	}
}
