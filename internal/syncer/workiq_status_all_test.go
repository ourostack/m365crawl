package syncer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/store"
)

// Live: `workiq status --source all` without --account exited workiq_account_required with a fix that
// never named --account, because no Outlook profile has a stored WorkIQ identity yet.
func TestWorkIQStatusWithoutAccountProbesWhatItCanAndNamesEachMailAccount(t *testing.T) {
	db, account := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	s, err := store.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	if _, err := s.CommitMail(context.Background(), store.MailBatch{Account: "outlook/Second Profile", ReadAt: at, FreshAt: at, Trusted: true,
		Result: outlookmail.Result{Folders: []outlookmail.Folder{{Key: 1, Name: "Inbox", Kind: "inbox"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "all", Kind: "status"})
	if err != nil {
		t.Fatal("status for all sources failed instead of probing what it can", err)
	}
	needs, chats := map[string]bool{}, false
	for _, probe := range report.Probes {
		if probe.Family == "mail" && probe.State == "account_required" && probe.Code == "workiq_account_required" {
			needs[probe.Account] = true
		}
		chats = chats || probe.Family == "chats"
	}
	if !chats || !needs[account] || !needs["outlook/Second Profile"] || report.Gaps["workiq_account_required"] != 2 {
		t.Fatalf("probes %+v gaps %v", report.Probes, report.Gaps)
	}
	_, err = RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail"})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_account_required" ||
		!strings.Contains(coded.Fix, "--account "+account) || !strings.Contains(coded.Fix, "--account 'outlook/Second Profile'") {
		t.Fatal("the account-required fix did not list the real --account choices", err)
	}
}
