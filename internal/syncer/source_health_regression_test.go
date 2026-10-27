package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
)

func TestReview248SharedBarriersAreNotDuplicated(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	r := &runner{st: st, o: Options{OutlookEnabled: true}}
	rep := Report{Status: StatusOK, Sources: []SourceReport{
		{Source: "outlook|One", Status: StatusUnavailable, Error: &SourceError{Code: "no_full_disk_access"}},
		{Source: "outlook|Two", Status: StatusUnavailable, Error: &SourceError{Code: "no_full_disk_access"}},
	}}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, row := range rep.Sources {
		counts[row.Source]++
	}
	if len(rep.Sources) != 4 || counts["outlook|One|mail"] != 1 || counts["outlook|Two|mail"] != 1 {
		t.Fatalf("duplicate public source outcomes: %+v", counts)
	}
}

func TestReview248SyncReportHonorsSelectedAccount(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, newDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if err := st.ApplyAccount(ctx, acctA); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyAccount(ctx, acctB); err != nil {
		t.Fatal(err)
	}
	a, b := acctA.TenantID+"/"+acctA.UserID, acctB.TenantID+"/"+acctB.UserID
	at := time.Now().UTC()
	for _, row := range []store.Run{
		{Source: "synthetic|teams", Status: StatusOK, SourceAccounts: []string{a, b}, StartedAt: at, FinishedAt: at},
		{Source: "synthetic|teams", Status: StatusFailed, ErrorCode: "store_missing", SourceAccounts: []string{b}, StartedAt: at, FinishedAt: at},
		{Source: "synthetic|teams", Status: StatusOK, SourceAccounts: []string{a}, StartedAt: at, FinishedAt: at},
	} {
		if err := st.RecordRun(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	r := &runner{st: st, o: Options{Account: &acctA}}
	rep := Report{Status: StatusOK, Sources: []SourceReport{{Source: "synthetic|teams", Status: StatusOK, Accounts: []string{a}}}}
	if err := r.recordSourceHealth(ctx, &rep, nil); err != nil {
		t.Fatal(err)
	}
	if len(rep.FailedSourceHealth) != 0 {
		t.Fatalf("successful account-A run reports untouched account-B failure: %+v", rep.FailedSourceHealth)
	}
}

func TestReview248SourceFailureDoesNotHidePersistenceError(t *testing.T) {
	isolateTmp(t)
	ctx := context.Background()
	root, _ := twoSourceRoot(t)
	db := newDB(t)
	run(t, Options{Root: root, DBPath: db})
	raw := openRaw(t, db)
	if _, err := raw.Exec(`create trigger review_health_refusal before insert on sync_runs when new.source<>'' and new.status='failed' begin select raise(fail,'review health persistence refused'); end`); err != nil {
		t.Fatal(err)
	}
	failNthSource(t, 2)
	rep, _, err := Run(ctx, Options{Root: root, DBPath: db, FullRead: true})
	var coded *errs.Coded
	_ = errors.As(err, &coded)
	st, openErr := store.OpenReadOnly(ctx, db)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = st.Close() }()
	health, healthErr := st.SourceHealth(ctx)
	t.Logf("report status=%s error=%v failed_source_health=%+v persisted health=%+v read error=%v", rep.Status, coded, rep.FailedSourceHealth, health, healthErr)
	if err == nil || !strings.Contains(err.Error(), "review health persistence refused") {
		t.Fatalf("source error hid the outcome-persistence error: %v; later health=%+v", err, health)
	}
	if coded == nil || coded.Code != errs.CodeDBError || !strings.Contains(err.Error(), "partial_sync") || rep.FailedSourceHealth != nil {
		t.Fatalf("failed persistence published checked health or lost source error: %+v %v", rep, err)
	}
}

func TestSourceHealthPersistenceFailurePreservesCancellation(t *testing.T) {
	isolateTmp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := newDB(t)
	old := afterApply
	afterApply = func(*writer) {
		if _, err := openRaw(t, db).Exec(`insert into meta(key,value) values('outlook_mail_failure:outlook/Synthetic','invalid')`); err != nil {
			t.Fatal(err)
		}
		cancel()
	}
	t.Cleanup(func() { afterApply = old })
	rep, _, err := Run(ctx, Options{Root: fixtureRoot, DBPath: db})
	var coded *errs.Coded
	if !errors.Is(err, context.Canceled) || !errors.As(err, &coded) || coded.Code != errs.CodeDBError || rep.FailedSourceHealth != nil {
		t.Fatalf("persistence failure lost cancellation or exposed checked health: %+v %v", rep, err)
	}
	st, openErr := store.OpenReadOnly(context.Background(), db)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = st.Close() }()
	last, readErr := st.LastSuccess(context.Background())
	if readErr != nil || !last.IsZero() {
		t.Fatalf("failed persistence recorded fresh success: %v %v", last, readErr)
	}
}
