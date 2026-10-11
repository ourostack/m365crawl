package store

import (
	"context"
	"testing"
)

func TestPeopleCaptureLinkRemainsHistoricalAfterUnlink(t *testing.T) {
	s := newStore(t)
	setupPeopleObservation(t, s)
	ctx := context.Background()
	_, err := s.db.Exec(`insert into calendar_account_links(source,account_id,principal_id,method,linked_at)
values('outlook','outlook/Fixture','tenant/user','config',?)`, base.Format(timeLayout))
	if err != nil {
		t.Fatal(err)
	}
	link, err := s.PeopleProfileLink(ctx, "outlook/Fixture")
	if err != nil || link.Principal != "tenant/user" || link.LinkedAt == nil {
		t.Fatalf("link evidence: %+v %v", link, err)
	}
	if _, err := s.db.Exec(`update calendar_account_links set unlinked_at=?`, base.Format(timeLayout)); err != nil {
		t.Fatal(err)
	}
	link, err = s.PeopleProfileLink(ctx, "outlook/Fixture")
	if err != nil || link.Principal != "" || link.ProfileAccount != "outlook/Fixture" {
		t.Fatalf("unlinked evidence: %+v %v", link, err)
	}
}
