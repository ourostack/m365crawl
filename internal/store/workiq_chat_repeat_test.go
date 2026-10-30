package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Graph repeats unchanged chat messages on the next page (live). A repeat with the same clocks is
// counted once; a repeat whose clocks changed is a per-chat gap, not a failure.
func TestWorkIQChatPageCountsExactRepeatsOnceAndGapsConflicts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	current := base.Add(-time.Hour)
	edited := workIQChatPageMessage(t, "edited", &current)
	var changed map[string]any
	must0(json.Unmarshal(edited, &changed))
	changed["lastModifiedDateTime"] = base.Add(time.Minute).Format(time.RFC3339Nano)
	attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{
		workIQChatPageMessage(t, "earlier-page", &current), must(json.Marshal(changed)), workIQChatPageMessage(t, "fresh", &current),
	})
	seen := map[string]string{}
	// "earlier-page" was counted on a previous page with the same clocks; "edited" with other clocks.
	seen["earlier-page"] = current.Format(time.RFC3339Nano) + "\x00" + base.Format(time.RFC3339Nano)
	seen["edited"] = current.Format(time.RFC3339Nano) + "\x00" + base.Format(time.RFC3339Nano)
	result := must(s.PrepareWorkIQChatPageSeen(ctx, attempt, page, chat, 128, seen))
	if !result.Complete || result.Seen != 1 || result.InWindow != 1 || result.DuplicatesSkipped != 1 ||
		result.Gaps["workiq_message_changed_during_traversal"] != 1 || len(seen) != 3 || result.SeenBytes == 0 {
		t.Fatalf("repeats were not counted once or a changed repeat was not a gap: %+v seen=%d", result, len(seen))
	}
	if rowCount(t, s, `select count(*) from workiq_gaps where code='workiq_message_changed_during_traversal'`) != 1 {
		t.Fatal("the changed repeat's gap was not stored for this chat page")
	}
}

func TestWorkIQChatPageSeenChangesOnlyAfterItsChunkCommits(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	current := base.Add(-time.Hour)
	attempt, page, chat := seedWorkIQChatPage(t, s, []json.RawMessage{workIQChatPageMessage(t, "one", &current)})
	seen := map[string]string{}
	must0(s.Close())
	if _, err := s.PrepareWorkIQChatPageSeen(ctx, attempt, page, chat, 128, seen); err == nil || len(seen) != 0 {
		t.Fatal("a failed chunk recorded its messages as seen", err, seen)
	}
}
