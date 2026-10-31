package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookmail"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func sourceMailIntegrityArchive(t *testing.T) (*Store, int64) {
	t.Helper()
	s := newStore(t)
	attempt := setupWorkIQMailControl(t, s, false)
	_ = must(s.CommitWorkIQMailGroup(context.Background(), attempt, "one@example.test"))
	return s, attempt
}

func nextMailIntegrityCopy(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Hour)))
	verifyWorkIQTestAttempt(t, s, run)
	must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, run, "inbox-id", []json.RawMessage{graphMailFixture("one", "inbox-id", "one@example.test")}, mailT0.Add(time.Hour)))
	settleWorkIQMailFixture(t, s, run)
	return run
}

func TestWorkIQMailRetainedSourceIdentityCannotCrossCanonicalBindings(t *testing.T) {
	for _, edit := range []string{
		`update workiq_rows set server_projection='broken'`,
		`update workiq_rows set server_projection=json_set(server_projection,'$.ID','other-graph')`,
		`update workiq_rows set graph_id='other-graph',server_projection=json_set(server_projection,'$.ID','other-graph')`,
		`update workiq_rows set cache_seen=1,cache_projection='broken'`,
		`update workiq_rows set cache_seen=1,cache_projection='{"Account":"outlook/other","DetailKey":99,"Body":{"HTML":"eA=="}}'`,
		`update workiq_rows set server_deleted_at='2026-09-10T08:00:00Z',server_deleted_version='bad'`,
	} {
		t.Run(edit, func(t *testing.T) {
			s, _ := sourceMailIntegrityArchive(t)
			ctx := context.Background()
			run := nextMailIntegrityCopy(t, s)
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.CommitWorkIQMailGroup(ctx, run, "one@example.test"); err == nil || dump(t, s) != before {
				t.Fatal("corrupt or contradictory retained mail projection was admitted or partially changed data")
			}
		})
	}
}

func TestWorkIQMailRetainedNativeCodecAndEmptyBodyRemainSourceAware(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	ctx := context.Background()
	empty := mailMsg(100, fInbox, "<one@example.test>", "Subject", 0)
	empty.Body = outlookmail.Body{State: outlookmail.BodyNone}
	mustCommitMail(t, s, mailBatch(mailT0, empty))
	if rowCount(t, s, `select count(*) from mail_messages where body_text='Server body'`) != 1 ||
		rowCount(t, s, `select count(*) from workiq_rows where cache_seen=1 and winner='workiq_server'`) != 1 {
		t.Fatal("empty native appearance blanked known server body or failed source applicability")
	}
	run := nextMailIntegrityCopy(t, s)
	_ = must(s.CommitWorkIQMailGroup(ctx, run, "one@example.test"))
	for _, mode := range []string{"bad-body-scrub", "bad-projection-scrub", "invalid-clock"} {
		t.Run(mode, func(t *testing.T) {
			original := scrubWorkIQProjection
			calls := 0
			scrubWorkIQProjection = func(raw []byte) ([]byte, int) {
				calls++
				if mode == "bad-body-scrub" || mode == "bad-projection-scrub" && calls == 2 {
					return []byte(`{`), 0
				}
				return original(raw)
			}
			t.Cleanup(func() { scrubWorkIQProjection = original })
			incoming := empty
			incoming.DetailKey = 1
			if mode == "invalid-clock" {
				incoming.Received = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			before := dump(t, s)
			err := s.inTx(ctx, func(tx *sql.Tx) error {
				_, err := applyWorkIQCacheMail(ctx, tx, mailAcct, 1, incoming, mailT0, mailT0)
				return err
			})
			if err == nil || dump(t, s) != before {
				t.Fatal("invalid native source codec was retained or partially changed data")
			}
			scrubWorkIQProjection = original
		})
	}
	for _, direct := range []bool{false, true} {
		archive, _ := sourceMailIntegrityArchive(t)
		_, err := archive.db.ExecContext(ctx, `update workiq_rows set server_projection='broken'`)
		must0(err)
		before := dump(t, archive)
		if direct {
			err = archive.inTx(ctx, func(tx *sql.Tx) error {
				_, err := applyWorkIQCacheMail(ctx, tx, mailAcct, 1, empty, mailT0, mailT0)
				return err
			})
		} else {
			_, err = archive.CommitMail(ctx, mailBatch(mailT0, empty))
		}
		if err == nil || dump(t, archive) != before {
			t.Fatal("invalid server shadow was silently repaired by native arrival")
		}
	}
}

func TestWorkIQMailCandidatePreparationAndCanonicalKeyExhaustionFailClosed(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `update meta set key='retained_index_marker' where key=?`, workIQMailIndexPrefix+mailAcct)
	must0(err)
	before := dump(t, s)
	if _, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0))); err == nil || dump(t, s) != before {
		t.Fatal("native source identity match proceeded without prepared candidate coverage")
	}
	archive, _ := sourceMailIntegrityArchive(t)
	var graphKey uint32
	must0(archive.db.QueryRowContext(ctx, `select detail_key from mail_messages`).Scan(&graphKey))
	batch := mailBatch(mailT0, mailMsg(graphKey, fInbox, "<different@test>", "Different", 0), mailMsg(^uint32(0), fInbox, "<last@test>", "Last", 0))
	before = dump(t, archive)
	if _, err := archive.CommitMail(ctx, batch); err == nil || dump(t, archive) != before {
		t.Fatal("batch high-water overflow was not an atomic refusal")
	}
	_, err = archive.db.ExecContext(ctx, `update mail_messages set detail_key=?`, uint64(^uint32(0)))
	must0(err)
	if err := archive.inTx(ctx, func(tx *sql.Tx) error { _, err := freeWorkIQMailCanonicalKey(ctx, tx, mailAcct); return err }); err == nil {
		t.Fatal("exhausted canonical key space returned an unusable key")
	}
}

func emptyFullMailIntegrityCensus(t *testing.T, s *Store) int64 {
	t.Helper()
	ctx := context.Background()
	run := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct}, mailT0.Add(time.Hour)))
	verifyWorkIQTestAttempt(t, s, run)
	must0(s.StageWorkIQMailFolder(ctx, run, workiq.Folder{ID: "inbox-id", WellKnown: "inbox"}))
	must0(s.StageWorkIQMailItems(ctx, run, "inbox-id", nil, mailT0.Add(time.Hour)))
	settleWorkIQMailFixture(t, s, run)
	_, err := s.db.ExecContext(ctx, `update workiq_censuses set full_enumeration=1,completed_at=? where attempt_id=?`, fmtTime(mailT0.Add(time.Hour)), run)
	must0(err)
	return run
}

func TestWorkIQMailCensusRejectsGapsCorruptionAndInconsistentOrder(t *testing.T) {
	for _, mode := range []string{"clock", "gap", "pending", "projection", "missing-id", "seen-clock", "future-first", "unqualified-first"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := sourceMailIntegrityArchive(t)
			ctx := context.Background()
			run := emptyFullMailIntegrityCensus(t, s)
			var q string
			var args []any
			switch mode {
			case "clock":
				q = `update workiq_censuses set completed_at='bad' where attempt_id=?`
				args = []any{run}
			case "gap":
				q = `insert into workiq_gaps(attempt_id,code,n) values(?,'workiq_policy_denied',1)`
				args = []any{run}
			case "pending":
				q = `insert into workiq_census_members(census_id,normalized_id,graph_id,folder_id,sender,subject_norm) select id,'new','new','inbox-id','','' from workiq_censuses where attempt_id=?`
				args = []any{run}
			case "projection":
				q = `update workiq_rows set server_projection='broken'`
			case "missing-id":
				q = `update workiq_rows set server_projection=json_set(server_projection,'$.InternetMessageID','')`
			case "seen-clock":
				q = `update workiq_rows set server_seen_at='bad'`
			case "future-first":
				q = `update workiq_rows set server_first_missing_census=(select max(id)+1 from workiq_censuses)`
			case "unqualified-first":
				q = `update workiq_rows set server_first_missing_census=1`
			}
			_, err := s.db.ExecContext(ctx, q, args...)
			must0(err)
			before := dump(t, s)
			if err := s.ConfirmWorkIQMailCensus(ctx, run); err == nil || dump(t, s) != before {
				t.Fatal("gap/corrupt/out-of-order census inferred absence or partially changed evidence")
			}
		})
	}
}

func TestWorkIQMailCensusIgnoresLaterLiveDeletionAndPresentMembers(t *testing.T) {
	for _, mode := range []string{"later-live", "own-deletion", "present"} {
		t.Run(mode, func(t *testing.T) {
			s, first := sourceMailIntegrityArchive(t)
			ctx := context.Background()
			run := first
			if mode == "present" {
				_, err := s.db.ExecContext(ctx, `update workiq_censuses set full_enumeration=1 where attempt_id=?`, first)
				must0(err)
			} else {
				run = emptyFullMailIntegrityCensus(t, s)
				if mode == "later-live" {
					_, err := s.db.ExecContext(ctx, `update workiq_rows set server_seen_at=?`, fmtTime(mailT0.Add(2*time.Hour)))
					must0(err)
				}
				if mode == "own-deletion" {
					_, err := s.db.ExecContext(ctx, `update workiq_rows set server_deleted_at=?`, fmtTime(mailT0))
					must0(err)
					must0(s.inTx(ctx, func(tx *sql.Tx) error { return reduceWorkIQMailMarks(ctx, tx, mailAcct) }))
				}
			}
			before := dump(t, s)
			must0(s.ConfirmWorkIQMailCensus(ctx, run))
			if dump(t, s) != before {
				t.Fatal("census manufactured absence across later live/deletion/present source evidence")
			}
		})
	}
}

func TestWorkIQMailRetainedMarksRejectBadClocksAndPublishNativeEviction(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	ctx := context.Background()
	_, err := s.db.ExecContext(ctx, `update workiq_rows set cache_seen=1,cache_first_miss_at=?,cache_evicted_at=?`,
		fmtTime(mailT0.Add(time.Hour)), fmtTime(mailT0.Add(2*time.Hour)))
	must0(err)
	must0(s.inTx(ctx, func(tx *sql.Tx) error { return reduceWorkIQMailMarks(ctx, tx, mailAcct) }))
	if must(s.MailGet(ctx, mailAcct, 1)).EvictedAt.IsZero() {
		t.Fatal("retained native eviction was lost or published with unreadable timestamp format")
	}
	_, err = s.db.ExecContext(ctx, `update workiq_rows set cache_evicted_at='bad'`)
	must0(err)
	before := dump(t, s)
	if err := s.inTx(ctx, func(tx *sql.Tx) error { return reduceWorkIQMailMarks(ctx, tx, mailAcct) }); err == nil || dump(t, s) != before {
		t.Fatal("corrupt source mark clock was accepted")
	}
}

func TestWorkIQMailOwnDeletionClockCorruptionNeverRestores(t *testing.T) {
	s, _ := sourceMailIntegrityArchive(t)
	ctx := context.Background()
	run := nextMailIntegrityCopy(t, s)
	_, err := s.db.ExecContext(ctx, `update workiq_rows set server_deleted_version='bad'`)
	must0(err)
	_, err = s.db.ExecContext(ctx, `update workiq_census_members set data_json=json_set(data_json,'$.deletedDateTime','2026-09-10T08:01:00Z') where census_id=(select id from workiq_censuses where attempt_id=?)`, run)
	must0(err)
	before := dump(t, s)
	if _, err := s.CommitWorkIQMailGroup(ctx, run, "one@example.test"); err == nil || dump(t, s) != before {
		t.Fatal("corrupt own deletion clock was interpreted as recovery")
	}
}

func TestWorkIQMailNativeArrivalCannotRepairForeignRetainedProjection(t *testing.T) {
	for _, edit := range []string{
		`update workiq_rows set server_projection=json_set(server_projection,'$.ID','other-graph')`,
		`update workiq_rows set cache_seen=1,cache_projection='{"Account":"outlook/other","DetailKey":99}'`,
	} {
		t.Run(edit, func(t *testing.T) {
			s, _ := sourceMailIntegrityArchive(t)
			ctx := context.Background()
			_, err := s.db.ExecContext(ctx, edit)
			must0(err)
			before := dump(t, s)
			if _, err := s.CommitMail(ctx, mailBatch(mailT0, mailMsg(100, fInbox, "<one@example.test>", "Subject", 0))); err == nil ||
				dump(t, s) != before {
				t.Fatal("native appearance silently repaired or applied another source identity")
			}
		})
	}
}
