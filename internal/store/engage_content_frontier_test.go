package store

import (
	"context"
	"testing"
)

func TestEngageMissingLocatorFrontierIsCorruption(t *testing.T) {
	s := newStore(t)
	commitEngage(t, s, engageResult("A"), base)
	if _, err := s.db.Exec(`update knowledge_locators set latest_attempt=null`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EngageSnapshot(context.Background(), EngageFilter{Query: "absent"}); err == nil {
		t.Fatal("missing locator frontier hidden by join")
	}
}
