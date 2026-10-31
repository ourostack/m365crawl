package store

import (
	"context"
	"testing"
)

func TestWorkIQSQLOriginsIncludeNativeRowsWithoutSourceClaims(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	seedReadable(t, s)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "native@example.test", "Native", 0)))
	for _, kind := range []string{"message", "conversation", "mail"} {
		var native, invented int
		must0(s.db.QueryRowContext(ctx, `select count(*),coalesce(sum(provenance<>'desktop_cache' or graph_id is not null or server_seen_at is not null),0)
from item_sources where kind=?`, kind).Scan(&native, &invented))
		if native < 1 || invented != 0 {
			t.Fatal("SQL source projection lost native rows or invented server evidence")
		}
	}
}

func TestWorkIQSQLOriginTracksSightingsNotWinningProjection(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	_, rowid := seedWorkIQBoundMessage(t, s, false)
	var origin string
	must0(s.db.QueryRowContext(ctx, `select provenance from item_sources where kind='message' and item_rowid=?`, rowid).Scan(&origin))
	if origin != "workiq_server" {
		t.Fatal("source-only SQL origin was lost")
	}
	_, err := s.db.ExecContext(ctx, `update workiq_rows set cache_seen=1,winner='desktop_cache' where kind='message' and item_rowid=?`, rowid)
	must0(err)
	must0(s.db.QueryRowContext(ctx, `select provenance from item_sources where kind='message' and item_rowid=?`, rowid).Scan(&origin))
	if origin != "both" {
		t.Fatal("SQL provenance followed the winner instead of independent sightings")
	}
}
