package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestWorkIQCoverageCountsCanonicalRowsWithoutInventingCacheRange(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	attempt := seedWorkIQMailAttempt(t, s)
	mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "one@example.test", "Subject", 0)))
	before := must(s.MailCoverage(ctx))
	if len(before) != 1 || before[0].Count != 1 {
		t.Fatal("native coverage fixture has no exact original census")
	}
	prepareRealMailInventory(t, s, attempt, []json.RawMessage{
		graphMailFixture("one", "inbox-id", "one@example.test"),
		graphMailFixture("two", "inbox-id", "two@example.test"),
	})
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "one@example.test"))
	_ = must(s.CommitWorkIQMailGroup(ctx, attempt, "two@example.test"))
	rows := must(s.MailCoverage(ctx))
	count, retained := 0, false
	for _, row := range rows {
		count += row.Count
		basis := reflect.ValueOf(row).FieldByName("CountBasis")
		if !basis.IsValid() || basis.String() != "canonical_archive" {
			t.Fatal("source-containing counts claimed to be a cache census")
		}
		if row.FolderKey == fInbox {
			retained = true
			if row.ReadAt != before[0].ReadAt || row.OldestAt != before[0].OldestAt || row.NewestAt != before[0].NewestAt {
				t.Fatal("source rows replaced the retained native range or read clock")
			}
		} else if !row.ReadAt.IsZero() || !row.OldestAt.IsZero() || !row.NewestAt.IsZero() {
			t.Fatal("a server-only folder fabricated cache range/read evidence")
		}
	}
	if count != 2 || !retained {
		t.Fatal("coverage omitted server-only rows, double-counted a merge, or lost the native range")
	}
	must0(s.SetWorkIQEnabled(ctx, false))
	if !reflect.DeepEqual(rows, must(s.MailCoverage(ctx))) {
		t.Fatal("disabling acquisition changed retained canonical coverage")
	}
}

func TestWorkIQCoverageSourceOnlyAccountHasCountsButNoCacheClock(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	rows := must(s.MailCoverage(context.Background()))
	if len(rows) != 1 || rows[0].Count != 1 || !rows[0].ReadAt.IsZero() ||
		!rows[0].OldestAt.IsZero() || !rows[0].NewestAt.IsZero() {
		t.Fatal("server-only coverage was absent or fabricated native freshness")
	}
}

func TestWorkIQCoverageReadFailuresPreserveCausesAndReturnNoPartialRows(t *testing.T) {
	setup := func(t *testing.T, s *Store) {
		attempt := setupWorkIQMailControl(t, s, true)
		_ = must(s.CommitWorkIQMailGroup(context.Background(), attempt, "one@example.test"))
	}
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.MailCoverage(ctx)
		return err
	})
	for _, kind := range []string{"begin", "commit"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			setup(t, s)
			inj := injectFaults(t, s)
			inj.arm(kind, 1)
			rows, err := s.MailCoverage(context.Background())
			fired := inj.disarm()
			if !fired || !errors.Is(err, errInjected) || rows != nil {
				t.Fatal("a failed coverage read returned partial counts or lost its error")
			}
		})
	}
}

func TestWorkIQCoverageDoesNotInventCountsFromInvalidSourceEvidence(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `update workiq_rows set cache_seen=0,server_seen=0`)
	must0(err)
	if rows, err := s.MailCoverage(ctx); err == nil || rows != nil {
		t.Fatal("invalid source evidence was reported as computed canonical coverage")
	}
}
