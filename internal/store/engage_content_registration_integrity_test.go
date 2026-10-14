package store

import (
	"context"
	"testing"
)

func TestEngageBeginCannotRepairMissingRegistration(t *testing.T) {
	s := newStore(t)
	commitEngage(t, s, engageResult("A"), base)
	if _, err := s.db.Exec(`delete from engage_locator_metadata`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginEngageAttempt(context.Background(), KnowledgeInput{Kind: EngageKind, Platform: "browser", Locator: EngageHomeURL, At: base}); err == nil {
		t.Fatal("Begin repaired missing immutable registration")
	}
}

func TestEngageOrphanRegistrationIsCorruption(t *testing.T) {
	s := newStore(t)
	if _, err := s.db.Exec(`insert into knowledge_locators(id,kind,platform,locator) values(1,'engage_home','browser','https://engage.cloud.microsoft/');
 insert into engage_locator_metadata(locator_id,target_url) values(1,'https://engage.cloud.microsoft/')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EngageSources(context.Background()); err == nil {
		t.Fatal("persisted registration without an attempt hidden")
	}
}
