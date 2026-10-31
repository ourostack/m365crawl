package syncer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func TestWorkIQStartupFailureCannotHidePersistenceFailureOrWriteWhileDisabled(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	ctx := context.Background()
	cause := &errs.Coded{Code: "workiq_policy_denied", Exit: errs.ExitRuntime}
	for _, mode := range []string{"disabled", "writer", "unknown-owner", "completion"} {
		t.Run(mode, func(t *testing.T) {
			old := workIQWrite
			defer func() { workIQWrite = old }()
			broken := errors.New("synthetic startup persistence failure")
			writes := 0
			if mode == "writer" || mode == "disabled" {
				workIQWrite = func(context.Context, string, func(context.Context, *store.Store) error) error {
					writes++
					return broken
				}
			}
			selected := account
			if mode == "unknown-owner" {
				selected = "outlook/Unknown"
			}
			if mode == "completion" {
				_, err := openRaw(t, db).Exec(`create trigger refuse_startup_completion before update on workiq_attempts
begin select raise(abort,'synthetic completion refusal'); end`)
				if err != nil {
					t.Fatal(err)
				}
			}
			report, err := recordWorkIQStartupFailure(ctx, WorkIQOptions{DBPath: db}, mode != "disabled",
				WorkIQAccountReport{Family: "mail", Account: selected}, cause)
			if !errors.Is(err, cause) || report.Status != "failed" {
				t.Fatal("startup persistence lost the original source refusal")
			}
			if mode == "disabled" {
				if writes != 0 || report.Gaps["workiq_persistence_failed"] != 0 {
					t.Fatal("disabled status tried to persist source state")
				}
			} else {
				var coded *errs.Coded
				if !errors.As(err, &coded) || coded.Code != errs.CodeDBError || report.Gaps["workiq_persistence_failed"] != 1 {
					t.Fatal("failed persistence became an ordinary source-only failure", err)
				}
				if mode == "writer" && !errors.Is(err, broken) {
					t.Fatal("startup persistence lost the actual writer cause")
				}
			}
		})
	}
}

func TestWorkIQRetainedPinCannotOverrideLaterKnownEmptyNativeIdentity(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	ctx := context.Background()
	options := WorkIQOptions{DBPath: db, Source: "mail", Account: account}
	if _, err := RunWorkIQ(ctx, options); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WORKIQ_ACQUISITION_ME", "unbound-other")
	before, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunWorkIQ(ctx, options); err != nil {
		t.Fatal("verified retained explicit pin still depended on a different default", err)
	}
	after, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil || len(after) < len(before) || strings.Contains(string(after[len(before):]), `"pinned":false`) {
		t.Fatal("retained pin used an unrelated default user", err)
	}
	_, err = openRaw(t, db).Exec(`insert into meta(key,value) values(?,?)`, "outlook_identity:"+account, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWorkIQ(ctx, options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_identity_mismatch" {
		t.Fatal("retained pin overrode a known-empty native identity", err)
	}
	final, err := os.ReadFile(transcript) //nolint:gosec // G304: own synthetic fetch transcript returned by the test helper.
	if err != nil || string(final) != string(after) {
		t.Fatal("known-empty identity spawned a source before refusal", err)
	}
}

func TestWorkIQExplicitBootstrapRefusesUnknownMailBeforeSpawning(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	_, err := workIQBootstrap(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: "outlook/Unknown",
		Invocation: workiq.Invocation{ExplicitTeamsRoot: true}})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_mail_account_missing" {
		t.Fatal("unknown mail bootstrap did not retain its named refusal", err)
	}
	if _, err := os.Stat(transcript); !os.IsNotExist(err) {
		t.Fatal("unknown mail selector started a subprocess")
	}
}
