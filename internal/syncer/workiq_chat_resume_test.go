package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Pages staged by an earlier run are prepared again on the next run, but only pages fetched in a run
// count against its page cap, so a chat larger than the cap continues across runs and finishes.
func TestWorkIQChatPageLimitCountsOnlyFetchedPagesAndResumes(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	old := workIQChatPageLimit
	t.Cleanup(func() { workIQChatPageLimit = old })
	workIQChatPageLimit = 1
	options := WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1}
	first, err := RunWorkIQ(context.Background(), options)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_page_limit" || !first.Resumable || first.Counts["pages"] != 1 {
		t.Fatal("the first run did not stop resumably at its fetched-page cap", err, first.Resumable, first.Counts)
	}
	second, err := RunWorkIQ(context.Background(), options)
	if err != nil || second.Resumable || second.Counts["pages"] != 1 || second.Counts["chats_completed"] != 1 {
		t.Fatal("the next run recounted earlier pages instead of continuing the chat", err, second.Counts)
	}
}

// Only resumable stops say so: a chat run that fails for any other reason is not resumable.
func TestWorkIQChatNonResumableFailureSaysSo(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "fail-second")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1})
	if err == nil || report.Resumable {
		t.Fatal("a refused chat run claimed it could resume", err)
	}
}
