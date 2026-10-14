package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestEngageAttemptReceiptBoundedCurrentIDs(t *testing.T) {
	s := newStore(t)
	var latest KnowledgeAttempt
	for i := 0; i < 65; i++ {
		r := engageResult("A")
		title := fmt.Sprintf("Synthetic session %d", i)
		r.Threads[0].Title = &title
		latest = commitEngage(t, s, r, base.Add(time.Duration(i)*time.Hour))
	}
	snap, err := s.EngageSnapshot(context.Background(), EngageFilter{})
	if err != nil || len(snap.Rows) != 128 || !snap.Truncated {
		t.Fatalf("unspecified snapshot output was unbounded: rows=%d %v", len(snap.Rows), err)
	}
	source, ids, err := s.EngageAttemptReceipt(context.Background(), latest)
	if err != nil || source.CurrentSessionID != 65 || len(ids) != 2 || ids[0] != 129 || ids[1] != 130 {
		t.Fatalf("bounded own receipt: %+v %v %v", source, ids, err)
	}
	if _, _, err := s.EngageAttemptReceipt(context.Background(), KnowledgeAttempt{ID: 1, LocatorID: latest.LocatorID}); err == nil {
		t.Fatal("historical attempt became current receipt")
	}
}
