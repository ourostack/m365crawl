package syncer

import (
	"context"
	"testing"
)

// Live: a coverage run that failed left its attempt open with no finish time or gap.
func TestWorkIQFailedCoverageClosesItsAttemptWithItsCode(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	t.Setenv("WORKIQ_PROBE_CASE", "bad-message")
	report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", Kind: "coverage", MaxChats: 1})
	if err == nil || report.Gaps["workiq_persistence_failed"] != 0 {
		t.Fatal("fixture did not fail coverage, or its outcome was not saved", err, report.Gaps)
	}
	if count(t, db, `select count(*) from workiq_attempts where kind='coverage' and status='open'`) != 0 ||
		count(t, db, `select count(*) from workiq_attempts where kind='coverage' and status='failed'
and finished_at is not null and error_code<>''`) != 1 {
		t.Fatal("the failed coverage attempt was not closed with its failure")
	}
}
