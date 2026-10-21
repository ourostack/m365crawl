package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/calendar"
)

func TestCommitOutlookPreservesUnknownIdentityWithoutInventingARead(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		s := newStore(t)
		ctx := context.Background()
		batch := OutlookBatch{
			Account: "outlook/Main", Events: []calendar.Event{outlookEvent(calendar.TriFalse, base)},
			FreshAt: base, At: base, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC),
		}
		if seeded {
			batch.Addresses = []string{"Known@Example.invalid"}
			if _, err := s.CommitOutlook(ctx, batch, outlookRun); err != nil {
				t.Fatal(err)
			}
		}
		batch.Addresses, batch.PreserveIdentity = nil, true
		batch.At = base.Add(time.Minute)
		if _, err := s.CommitOutlook(ctx, batch, outlookRun); err != nil {
			t.Fatal(err)
		}
		state, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
		if err != nil || state.IdentityRead != seeded || !state.HoldsEvents {
			t.Fatalf("unknown identity became an empty read: seeded=%v state=%+v err=%v", seeded, state, err)
		}
		var rows int
		if err := s.db.QueryRow(`select count(*) from meta where key=?`, outlookIdentityKey+"outlook/Main").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if !seeded && rows != 0 {
			t.Fatal("a never-observed identity row was invented")
		}
		if seeded {
			var addresses string
			if err := s.db.QueryRow(`select value from meta where key=?`, outlookIdentityKey+"outlook/Main").Scan(&addresses); err != nil || addresses != "known@example.invalid" {
				t.Fatal("unknown identity replaced previously observed addresses")
			}
		}
	}
}

func TestCommitOutlookKnownEmptyIdentityStillClearsAddresses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	batch := OutlookBatch{Account: "outlook/Main", At: base, FreshAt: base, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC), Addresses: []string{"known@example.invalid"}}
	if _, err := s.CommitOutlook(ctx, batch, outlookRun); err != nil {
		t.Fatal(err)
	}
	batch.Addresses = nil
	if _, err := s.CommitOutlook(ctx, batch, outlookRun); err != nil {
		t.Fatal(err)
	}
	var addresses string
	if err := s.db.QueryRow(`select value from meta where key=?`, outlookIdentityKey+"outlook/Main").Scan(&addresses); err != nil || addresses != "" {
		t.Fatal("an explicitly observed empty identity stopped clearing old addresses")
	}
	state, err := s.OutlookState(ctx, "Main", "outlook|Main", "outlook/Main")
	if err != nil || !state.IdentityRead {
		t.Fatal("the legacy known-empty identity marker was lost")
	}
}

func TestCommitOutlookRejectsConflictingIdentityIntent(t *testing.T) {
	s := newStore(t)
	for _, addresses := range [][]string{{}, {"private@example.invalid"}} {
		_, err := s.CommitOutlook(context.Background(), OutlookBatch{
			Account: "outlook/Main", At: base, FreshAt: base, Zone: time.UTC, Stamp: OutlookStamp(1, time.UTC),
			Addresses: addresses, PreserveIdentity: true,
		}, outlookRun)
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("conflicting identity intent was accepted or exposed its values")
		}
	}
	var runs int
	if err := s.db.QueryRow(`select count(*) from sync_runs`).Scan(&runs); err != nil || runs != 0 {
		t.Fatal("a refused identity batch wrote a successful run")
	}
}
