package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQExplicitStoredAccountDoesNotDependOnDefaultUser(t *testing.T) {
	for _, kind := range []string{"sync", "coverage", "status"} {
		t.Run(kind, func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			transcript := workIQAcquisitionStub(t)
			_, err := openRaw(t, db).Exec(`insert into meta(key,value) values(?,?)`,
				"outlook_identity:"+account, "test.user@example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("WORKIQ_ACQUISITION_ME", "unbound-other")
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account, Kind: kind})
			if err != nil || len(report.Accounts) != 1 || report.Accounts[0].Status != "ok" {
				t.Fatal("an available explicitly pinned account depended on another default user", err)
			}
			raw, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"pinned":false`) {
				t.Fatal("known explicit identity still used the default user")
			}
		})
	}
}

func TestWorkIQIdentifiedStartupFailureRevokesPriorMailCoverage(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "auto-bound", true: "explicit"}[explicit], func(t *testing.T) {
			db, account := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			_, err := openRaw(t, db).Exec(`insert into meta(key,value) values(?,?)`,
				"outlook_identity:"+account, "test.user@example.invalid")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			options := WorkIQOptions{DBPath: db, Source: "mail"}
			if explicit {
				options.Account = account
			}
			if _, err := RunWorkIQ(ctx, options); err != nil {
				t.Fatal(err)
			}
			s, err := store.Open(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-time.Hour)
			err = s.RecordRun(ctx, store.Run{Source: "outlook|" + strings.TrimPrefix(account, "outlook/") + "|mail",
				Status: "failed", ErrorCode: "outlook_mail_layout_unsupported", StartedAt: at, FinishedAt: at,
				SourceAccounts: []string{account}})
			if err != nil {
				t.Fatal(err)
			}
			before, err := s.SourceHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			covered := false
			for _, health := range before {
				covered = covered || health.Source == "outlook_mail" && health.State == "covered_by_server"
			}
			if !covered {
				t.Fatal("fixture did not establish exact prior server coverage")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			old := workIQOpen
			t.Cleanup(func() { workIQOpen = old })
			broken := errors.New("synthetic pinned startup failure")
			workIQOpen = func(ctx context.Context, invocation workiq.Invocation, address string) (*workiq.Client, error) {
				if address != "" {
					return nil, broken
				}
				return old(ctx, invocation, address)
			}
			_, err = RunWorkIQ(ctx, options)
			if !errors.Is(err, broken) {
				t.Fatal("actual startup failure was swallowed", err)
			}
			s, err = store.OpenReadOnly(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			health, err := s.SourceHealth(ctx)
			if err != nil {
				t.Fatal(err)
			}
			failed := false
			for _, row := range health {
				if row.Source == "outlook_mail" && row.State == "covered_by_server" {
					t.Fatal("known owner startup failure left the earlier covering success active")
				}
				failed = failed || row.Source == "workiq" && row.LastErrorCode == workIQOutcomeCode(broken)
			}
			if !failed {
				t.Fatal("identified account lost its failed data attempt")
			}
		})
	}
}
