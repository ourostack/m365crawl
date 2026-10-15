package store

import (
	"context"
	"testing"
	"time"
)

func TestAttendeeLinkRevisionNoopUpsert(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	commitOutlook(t, s, time.UTC, "same")
	s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at)
values('outlook','outlook/Main','tenant/A','config','2031-03-01T00:00:00Z')`)
	first, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
	if err != nil {
		t.Fatal(err)
	}
	s.qExec(t, `insert into calendar_account_links(source,account_id,principal_id,method,linked_at)
values('outlook','outlook/Main','tenant/A','config','2031-03-01T00:00:00Z')
on conflict(source,account_id) do update set principal_id=excluded.principal_id`)
	after, err := attendeeLinkRevision(ctx, s.db, "outlook/Main")
	if err != nil || after != first {
		t.Fatal("no-op UPSERT advanced actual link mutation identity")
	}
}
