package syncer

import (
	"context"
	"errors"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
)

// Live: Graph repeats unchanged chat messages on the next page. Chat sync counts them once and
// turns a repeat with changed clocks into a gap instead of a failure or a double count.
func TestWorkIQChatSyncCountsCrossPageRepeatsOnce(t *testing.T) {
	for _, tc := range []struct {
		probe         string
		seen, skipped int
		changed       int
	}{{"exact-repeat-message", 3, 1, 0}, {"duplicate-message", 1, 0, 1}} {
		t.Run(tc.probe, func(t *testing.T) {
			db, _ := workIQAcquisitionArchive(t)
			workIQAcquisitionStub(t)
			seedWorkIQChatOwner(t, db)
			t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
			t.Setenv("WORKIQ_PROBE_CASE", tc.probe)
			report, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1})
			if err != nil || report.Counts["messages_seen"] != tc.seen || report.Counts["duplicates_skipped"] != tc.skipped ||
				report.Gaps["workiq_message_changed_during_traversal"] != tc.changed {
				t.Fatalf("%v counts %v gaps %v", err, report.Counts, report.Gaps)
			}
		})
	}
}

func TestWorkIQChatSyncSeenSetIsBounded(t *testing.T) {
	db, _ := workIQAcquisitionArchive(t)
	workIQAcquisitionStub(t)
	seedWorkIQChatOwner(t, db)
	t.Setenv("WORKIQ_ACQUISITION_CHATS", "paged")
	old := workIQChatSeenLimit
	t.Cleanup(func() { workIQChatSeenLimit = old })
	workIQChatSeenLimit = 1
	_, err := RunWorkIQ(context.Background(), WorkIQOptions{DBPath: db, Source: "chats", MaxChats: 1})
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != "workiq_message_count_limit" {
		t.Fatal("an exhausted seen bound was not a named failure", err)
	}
}
