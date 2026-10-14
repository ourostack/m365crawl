package store

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type engageCancelContext struct {
	context.Context
	cancel context.CancelFunc
	n      atomic.Int64
	at     int64
}

func (c *engageCancelContext) Err() error {
	if c.n.Add(1) == c.at {
		c.cancel()
	}
	return c.Context.Err()
}

func TestEngageHistoryCancellation(t *testing.T) {
	s := newStore(t)
	commitEngage(t, s, engageResult("A"), base)
	cancelled := 0
	for at := int64(1); at <= 120; at++ {
		parent, cancel := context.WithCancel(context.Background())
		ctx := &engageCancelContext{Context: parent, cancel: cancel, at: at}
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_, err = readEngage(ctx, tx, EngageFilter{}, 0, true)
		cancel()
		_ = tx.Rollback()
		if errors.Is(err, context.Canceled) {
			cancelled++
		} else if err != nil {
			t.Fatalf("cancellation %d: %v", at, err)
		}
	}
	if cancelled == 0 {
		t.Fatal("no cancellation exercised")
	}
}

func TestEngageOptionalSchemaFaultSweep(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		for _, table := range []string{"engage_locator_metadata", "engage_source_metadata", "engage_sessions", "engage_occurrences", "engage_capture_members", "engage_source_state", "engage_fts"} {
			if _, err := s.db.Exec("drop table " + table); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.Exec(`update schema_migrations set version=17`); err != nil {
			t.Fatal(err)
		}
	}
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error { _, err := s.EngageSources(ctx); return err })
}
