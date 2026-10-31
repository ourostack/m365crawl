package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func windowAttemptID(t *testing.T, s *Store) int64 {
	t.Helper()
	var id int64
	must0(s.cs.DB().QueryRow(`select max(id) from workiq_attempts where kind='mail_window'`).Scan(&id))
	return id
}

func TestWorkIQWindowStagingFaultsLeaveNoTrace(t *testing.T) {
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	delta := "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x"
	october := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	setup := func(t *testing.T, s *Store) {
		attempt := seedWorkIQWindowAttempt(t, s)
		stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
			windowMail("in", "inbox-id", "in@example.test", windowSince, "2026-09-01T00:00:00Z"),
			windowMail("late", "inbox-id", "late@example.test", october, "2026-10-01T00:00:00Z"),
		}, "", delta)
	}
	page := func(t *testing.T) workiq.Page {
		return windowPage(t, []json.RawMessage{
			windowMail("in", "inbox-id", "in@example.test", windowSince, "2026-09-01T00:00:00Z"),
			windowMail("new", "inbox-id", "new@example.test", windowSince, "2026-09-01T00:00:00Z"),
			windowRemoved("late"), windowRemoved("unknown"),
		}, "", delta)
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		attempt := windowAttemptID(t, s)
		p, err := s.WorkIQWindowProgressFor(ctx, attempt, resource)
		if err != nil {
			return err
		}
		_, err = s.StageWorkIQWindowPage(ctx, attempt, "inbox-id", resource, p.Revision, page(t), mailT0)
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		attempt := windowAttemptID(t, s)
		return s.RetireWorkIQWindowFrontier(ctx, attempt, resource, 1)
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		attempt := windowAttemptID(t, s)
		_, err := s.WorkIQWindowFactsFor(ctx, attempt, resource)
		return err
	})
	sweepReadFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.hasWorkIQWindows(ctx)
		return err
	})
}

func TestWorkIQWindowAdmissionFaultsLeaveNoTrace(t *testing.T) {
	resource := WorkIQWindowDeltaResource("inbox-id", windowSince)
	setup := func(t *testing.T, s *Store) {
		ctx := context.Background()
		odd := mailMsg(101, fInbox, "<odd@example.test>", "Subject", 0)
		odd.SenderAddress = "someone.else@example.test"
		mustCommitMail(t, s, mailBatch(mailT0, mailMsg(100, fInbox, "<cached@example.test>", "Subject", 0), odd))
		must0(s.SetWorkIQEnabled(ctx, true))
		attempt := beginWorkIQWindow(t, s)
		must0(s.EnsureWorkIQWindowFolders(ctx, attempt, []workiq.Folder{{ID: "inbox-id", Name: "Inbox", WellKnown: "inbox"}}))
		in := windowSince.Add(time.Hour)
		stageWindow(t, s, attempt, "inbox-id", resource, []json.RawMessage{
			windowMail("cached-copy", "inbox-id", "cached@example.test", in, "2026-09-01T01:00:00Z"),
			windowMail("fresh", "inbox-id", "fresh@example.test", in, "2026-09-01T01:00:00Z"),
			windowMail("odd-copy", "inbox-id", "odd@example.test", in, "2026-09-01T01:00:00Z"),
		}, "", "/me/mailFolders/inbox-id/messages/delta?$deltatoken=x")
		other := WorkIQWindowDeltaResource("other-id", windowSince)
		stageWindow(t, s, attempt, "other-id", other, []json.RawMessage{
			windowMail("bound-later", "other-id", "cached@example.test", in, "2026-09-01T01:00:00Z"),
		}, "", "/me/mailFolders/other-id/messages/delta?$deltatoken=x")
		_ = must(s.AdmitWorkIQWindow(ctx, attempt, []string{other}, 128))
		var row int64
		must0(s.db.QueryRowContext(ctx, `select rowid from mail_messages where detail_key=100`).Scan(&row))
		must(s.db.ExecContext(ctx, `insert into workiq_aliases(kind,account_id,namespace,alias,item_rowid) values('mail',?,'graph','bound-later',?)`, mailAcct, row))
	}
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		_, err := s.AdmitWorkIQWindow(ctx, windowAttemptID(t, s), []string{resource}, 128)
		return err
	})
	sweepFaults(t, setup, func(ctx context.Context, s *Store) error {
		return s.EnsureWorkIQWindowFolders(ctx, windowAttemptID(t, s), []workiq.Folder{{ID: "new-id", ParentID: "inbox-id", Name: "New"}})
	})
	admitted := func(t *testing.T, s *Store) {
		setup(t, s)
		_ = must(s.AdmitWorkIQWindow(context.Background(), windowAttemptID(t, s), []string{resource}, 128))
	}
	sweepFaults(t, admitted, func(ctx context.Context, s *Store) error {
		return s.RecordWorkIQWindowCoverage(ctx, windowAttemptID(t, s), []string{"inbox-id"}, true, windowUntil)
	})
}

// TestWorkIQWindowMigrationFaultsLeaveNoTrace fails the migration once at every driver call. A
// failure before the commit leaves the version 22 archive exactly as it was; one while restoring
// the connection settings after the commit leaves the finished migration, and is still reported,
// so the open fails.
func TestWorkIQWindowMigrationFaultsLeaveNoTrace(t *testing.T) {
	ctx := context.Background()
	fresh := func(t *testing.T) *Store {
		s := newStore(t)
		seedCompletedWorkIQMail(t, s)
		downgradeWorkIQWindowSchema(t, s)
		return s
	}
	for _, kind := range faultKinds {
		t.Run(kind, func(t *testing.T) {
			for at := 1; ; at++ {
				if at > 500 {
					t.Fatal("sweep did not end")
				}
				s := fresh(t)
				before, attempts := dump(t, s), dumpTable(t, s, "workiq_attempts")
				inj := injectFaults(t, s)
				inj.arm(kind, at)
				err := s.migrateWorkIQWindow(ctx)
				if !inj.disarm() {
					if err != nil {
						t.Fatalf("unfaulted migration failed: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatalf("%s #%d: failure was swallowed", kind, at)
				}
				if after := dump(t, s); after != before && (!attemptsAllowWindows(t, s) || dumpTable(t, s, "workiq_attempts") != attempts ||
					rowCount(t, s, `select count(*) from pragma_table_info('workiq_rows') where name='identity_unproven'`) != 1) {
					t.Fatalf("%s #%d: a failed migration left a partial change", kind, at)
				}
			}
		})
	}
}
