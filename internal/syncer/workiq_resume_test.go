package syncer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
)

// The synthetic helper reproduces live WorkIQ mail: one message per delta page, links with a
// parenthesized folder key that WorkIQ refuses as a request, and a 429 whose wait is the result's
// retryAfterSeconds. A mailbox larger than one run's page budget must finish across runs from its
// stored position, following those links in the accepted form.
func TestWorkIQMailResumesAcrossRunsThroughServerLinksAndThrottling(t *testing.T) {
	const messages = 6
	db, account := workIQAcquisitionArchive(t)
	transcript := workIQAcquisitionStub(t)
	t.Setenv("WORKIQ_ACQUISITION_MAIL_PAGES", "many")
	t.Setenv("WORKIQ_ACQUISITION_MESSAGE_COUNT", "6")
	old, oldWait := workIQMailPageLimit, workIQWait
	t.Cleanup(func() { workIQMailPageLimit, workIQWait = old, oldWait })
	// Six well-known folder reads, two folder-list pages and the Inbox child-folder page; folders are
	// then read in ID order, so the Archive, Deleted Items and Drafts deltas precede the Inbox, whose
	// delta stops after three of its six pages.
	workIQMailPageLimit = 6 + 2 + 1 + 3 + 3
	var waits []time.Duration
	workIQWait = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	first, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_page_limit" || first.Accounts[0].Status == "ok" ||
		first.Accounts[0].Counts["pages"] != workIQMailPageLimit || !strings.Contains(coded.Fix, "run the same workiq sync again") {
		t.Fatalf("first run: %v %+v", err, first.Accounts[0])
	}
	t.Setenv("WORKIQ_ACQUISITION_THROTTLE_ONCE", filepath.Join(t.TempDir(), "throttled-once"))
	for run := 2; run <= 3; run++ {
		if run == 3 {
			// A fresh incremental census lists every folder again, which needs more than the test cap.
			workIQMailPageLimit = old
		}
		report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "mail", Account: account})
		if err != nil || report.Accounts[0].Status != "ok" {
			t.Fatalf("run %d did not continue to completion: %v %+v", run, err, report.Accounts)
		}
	}
	if count(t, db, `select count(*) from mail_messages`) != messages {
		t.Fatal("a capped mailbox lost or duplicated mail across runs")
	}
	if len(waits) != 1 || waits[0] != 12*time.Second {
		t.Fatalf("a throttled page did not wait for its stated retryAfterSeconds: %v", waits)
	}
	raw, err := os.ReadFile(filepath.Clean(transcript))
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if strings.Contains(calls, "('") || strings.Count(calls, `"/me/mailFolders/folder-inbox/messages/delta"`) != 1 ||
		!strings.Contains(calls, `/me/mailFolders/folder-inbox/messages/delta?$skiptoken=p3`) ||
		!strings.Contains(calls, `/me/mailFolders/folder-inbox/messages/delta?$deltatoken=stable%2bstate`) {
		t.Fatalf("runs restarted the folder or did not follow server links in the accepted form:\n%s", calls)
	}
}
