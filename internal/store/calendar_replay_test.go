package store

import (
	"context"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamscal"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func TestCalendarReplayRetainsFilteredSkippedAndFailedWork(t *testing.T) {
	for _, kind := range []string{"filtered", "missing-metadata", "derive-failure", "rollback", "commit-failure"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			archive := scalar(t, s, `select file from pragma_database_list where name='main'`)
			putRecords(t, s, t0, calEv(acctA, "replay-a", "2026-09-10T09:00:00Z", nil),
				calEv(acctB, "replay-b", "2026-09-10T10:00:00Z", nil))
			if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
				t.Fatal(err)
			}
			beforeClock := scalar(t, s, `select group_concat(synced_at,'|') from (select synced_at from calendar_sources order by account_id)`)
			x, err := s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if x != nil {
					x.Rollback()
				}
			})
			if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
				calEv(acctA, "replay-a", "2026-09-10T09:00:00Z", map[string]any{"subject": "Changed A", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}),
				calEv(acctB, "replay-b", "2026-09-10T10:00:00Z", map[string]any{"subject": "Changed B", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}),
			}, t1); err != nil {
				x.Rollback()
				t.Fatal(err)
			}
			res, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false)
			if err != nil || !res.ReplayPending {
				x.Rollback()
				t.Fatalf("incomplete read lost replay obligation: %+v %v", res, err)
			}
			if err := x.Commit(); err != nil {
				t.Fatal(err)
			}
			if scalar(t, s, `select group_concat(synced_at,'|') from (select synced_at from calendar_sources order by account_id)`) != beforeClock ||
				scalar(t, s, `select subject from calendar_source_events where source_id='replay-b'`) != "Subject replay-b" {
				t.Fatal("incomplete positive content advanced calendar authority")
			}
			x, err = s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			res, err = x.DeriveCalendarObservation(ctx, calSource, t1.Add(time.Minute), time.UTC, nil, false, false)
			if err != nil || !res.ReplayPending {
				x.Rollback()
				t.Fatalf("repeated damage lost replay obligation: %+v %v", res, err)
			}
			if err := x.Commit(); err != nil {
				t.Fatal(err)
			}
			if scalar(t, s, `select count(*) from meta where key like 'calendar_replay/%'`) != "1" {
				t.Fatal("repeated damage grew or discarded the bounded per-source obligation")
			}
			if kind == "derive-failure" {
				if _, err := s.db.Exec(`create trigger replay_refuse before update on calendar_source_events begin select raise(abort,'owned replay fault'); end`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "commit-failure" {
				if _, err := s.db.Exec(`create table replay_parent(id integer primary key); create table replay_child(pid integer references replay_parent(id) deferrable initially deferred)`); err != nil {
					t.Fatal(err)
				}
			}
			x, err = s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			present := []string{calDB("calendar", acctA), calDB("calendar", acctB)}
			if kind == "filtered" || kind == "missing-metadata" {
				present = present[:1]
			}
			res, err = x.DeriveCalendarObservation(ctx, calSource, t2, time.UTC, present, true, kind == "filtered")
			if err != nil {
				x.Rollback()
				t.Fatal(err)
			}
			switch kind {
			case "rollback":
				x.Rollback()
			case "commit-failure":
				if _, err := x.tx.Exec(`insert into replay_child(pid) values(999)`); err != nil {
					x.Rollback()
					t.Fatal(err)
				}
				if err := x.Commit(); err == nil {
					t.Fatal("qualification: deferred foreign-key COMMIT did not fail")
				}
				// Verify durable state after releasing the actual failed-transaction connection.
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s, err = Open(ctx, archive)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
			default:
				if !res.ReplayPending {
					x.Rollback()
					t.Fatalf("narrow/skipped/failed work cleared source obligation: %+v", res)
				}
				if err := x.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || !pending {
				t.Fatalf("replay obligation was not durable: %t %v", pending, err)
			}
			if scalar(t, s, `select subject from calendar_source_events where source_id='replay-b'`) != "Subject replay-b" {
				t.Fatal("unqualified/rolled-back peer projection changed")
			}
			if scalar(t, s, `select count(*) from meta where key='calendar_derivation'`) != "1" {
				t.Fatal("replay failure invalidated the global qualified derivation stamp")
			}
			if kind == "derive-failure" {
				if _, err := s.db.Exec(`drop trigger replay_refuse`); err != nil {
					t.Fatal(err)
				}
			}
			x, err = s.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			res, err = x.DeriveCalendarObservation(ctx, calSource, t2.Add(time.Hour), time.UTC,
				[]string{calDB("calendar", acctA), calDB("calendar", acctB)}, true, false)
			if err != nil || res.ReplayPending || res.failure != nil {
				x.Rollback()
				t.Fatalf("complete unchanged-byte replay failed: %+v %v", res, err)
			}
			if err := x.Commit(); err != nil {
				t.Fatal(err)
			}
			if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || pending {
				t.Fatalf("successful full replay retained obligation: %t %v", pending, err)
			}
			for id, want := range map[string]string{"replay-a": "Changed A", "replay-b": "Changed B"} {
				if got := scalar(t, s, `select subject from calendar_source_events where source_id=?`, id); got != want {
					t.Fatalf("unchanged raw positive remained stranded: %s=%q", id, got)
				}
			}
		})
	}
}

func TestCalendarReplaySetAndRawRollbackTogether(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}

	defer x.Rollback()
	if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{calEv(acctA, "aborted", "2026-09-10T09:00:00Z", nil)}, t0); err != nil {
		t.Fatal(err)
	}
	if res, err := x.DeriveCalendarObservation(ctx, calSource, t0, time.UTC, nil, false, false); err != nil || !res.ReplayPending {
		t.Fatalf("in-transaction replay mark: %+v %v", res, err)
	}
	x.Rollback()
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || pending {
		t.Fatalf("rolled-back source retained replay state: %t %v", pending, err)
	}
	if got := scalar(t, s, `select count(*) from records`); got != "0" {
		t.Fatalf("rolled-back source retained raw content: %s", got)
	}
}

func TestCalendarReplayObservationFailuresRollBack(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "incomplete-mark"
		if complete {
			name = "pending-complete-clear"
		}
		t.Run(name, func(t *testing.T) {
			sweepCalendar(t, func(t *testing.T, s *Store) {
				ctx := context.Background()
				putRecords(t, s, t0, calEv(acctA, "fault-replay", "2026-09-10T09:00:00Z", nil))
				if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
					t.Fatal(err)
				}
				if complete {
					x, err := s.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer x.Rollback()
					res, err := x.DeriveCalendarObservation(ctx, calSource, t0, time.UTC, nil, false, false)
					if err != nil || res.failure != nil || !res.ReplayPending {
						t.Fatalf("qualification: pending replay seed failed: %+v %v", res, err)
					}
					if err := x.Commit(); err != nil {
						t.Fatal(err)
					}
				}
			}, func(ctx context.Context, s *Store) error {
				x, err := s.Begin(ctx)
				if err != nil {
					return err
				}
				defer x.Rollback()
				res, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC,
					[]string{calDB("calendar", acctA)}, complete, false)
				if err != nil {
					return err
				}
				if res.failure != nil {
					return res.failure
				}
				return x.Commit()
			})
		})
	}
}

func TestCalendarReplayBackfillPreservesQualifiedAuthority(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	putRecords(t, s, t0, calEv(acctA, "authority-old", "2026-09-10T09:00:00Z", nil))
	if _, err := s.EnsureCalendar(ctx, time.UTC, t0); err != nil {
		t.Fatal(err)
	}
	const sources = `select group_concat(json_array(source,account_id,window_start,window_end,synced_at,cache_fresh_at),'|') from
			(select * from calendar_sources order by source,account_id)`
	const days = `select group_concat(json_array(source,account_id,day,first_verified_at,last_verified_at),'|') from
			(select * from calendar_covered_days order by source,account_id,day)`
	const raw = `select group_concat(json_array(rowid,source,tenant_id,user_id,database,store,key_json,value_json,content_hash,first_seen_at,updated_at,removed_at,hex(raw_digest),value_redacted),'|') from
			(select rowid,* from records order by rowid)`
	oldSources, oldDays := scalar(t, s, sources), scalar(t, s, days)
	if scalar(t, s, `select count(*) from calendar_sources where synced_at<>'' and cache_fresh_at<>''`) != "1" ||
		scalar(t, s, `select count(*) from calendar_covered_days where first_verified_at<>'' and last_verified_at<>''`) != "1" {
		t.Fatal("qualification: need real previously qualified nonzero source/day authority")
	}
	x, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	if _, err := x.UpsertRecords(calSource, []teamsdesktop.GenericRecord{
		calEv(acctA, "authority-old", "2026-09-10T09:00:00Z", map[string]any{"subject": "Retained changed positive", "lastModifiedTime": map[string]any{"$date": "2026-08-02T00:00:00Z"}}),
		calEv(acctA, "authority-new", "2026-09-20T09:00:00Z", nil),
	}, t1); err != nil {
		t.Fatal(err)
	}
	res, err := x.DeriveCalendarObservation(ctx, calSource, t1, time.UTC, nil, false, false)
	if err != nil || res.failure != nil || !res.ReplayPending {
		t.Fatalf("qualification: real incomplete observation did not retain obligation: %+v %v", res, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s, sources) != oldSources || scalar(t, s, days) != oldDays ||
		scalar(t, s, `select subject from calendar_source_events where source_id='authority-old'`) != "Subject authority-old" {
		t.Fatal("incomplete input changed the previously qualified projection or authority")
	}
	beforeRaw := scalar(t, s, raw)
	stale := calendarDerivation(teamscal.MapperVersion-1, zoneStamp(time.UTC), teamsdesktop.RulesStamp())
	if _, err := s.db.Exec(`update meta set value=? where key=?`, stale, calendarMetaKey); err != nil {
		t.Fatal(err)
	}
	if stale == calendarDerivation(teamscal.MapperVersion, zoneStamp(time.UTC), teamsdesktop.RulesStamp()) ||
		scalar(t, s, `select value from meta where key=?`, calendarMetaKey) != stale {
		t.Fatal("qualification: legitimate mapper-only rebuild mismatch was not established")
	}
	rebuild, err := s.EnsureCalendar(ctx, time.UTC, t2)
	if err != nil || rebuild == nil || rebuild.failure != nil || rebuild.Omissions[OmitCalendarFailed] != 0 {
		t.Fatalf("archive-only pending rebuild failed: %+v %v", rebuild, err)
	}
	if scalar(t, s, `select subject from calendar_source_events where source_id='authority-old'`) != "Retained changed positive" ||
		scalar(t, s, `select count(*) from calendar_source_events where removed_at is null`) != "2" {
		t.Fatal("pending backfill silently skipped retained positive work")
	}
	if scalar(t, s, sources) != oldSources || scalar(t, s, days) != oldDays {
		t.Fatal("archive-only pending backfill changed old clocks/day envelope or verified a new day")
	}
	if scalar(t, s, raw) != beforeRaw {
		t.Fatal("archive-only pending rebuild changed raw/rowid/hash/removal state")
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || !pending {
		t.Fatalf("archive-only rebuild discharged incomplete source: %t %v", pending, err)
	}
	x, err = s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	res, err = x.DeriveCalendarObservation(ctx, calSource, t2.Add(time.Hour), time.UTC,
		[]string{calDB("calendar", acctA)}, true, false)
	if err != nil || res.failure != nil || res.ReplayPending {
		t.Fatalf("qualified complete recovery failed: %+v %v", res, err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.CalendarReplayPending(ctx, calSource); err != nil || pending {
		t.Fatalf("qualified complete recovery retained obligation: %t %v", pending, err)
	}
	if scalar(t, s, `select synced_at from calendar_sources`) != t2.Add(time.Hour).UTC().Format(timeLayout) ||
		scalar(t, s, `select count(*) from calendar_covered_days where first_verified_at<>'' and last_verified_at<>''`) != "2" ||
		scalar(t, s, raw) != beforeRaw {
		t.Fatal("complete recovery failed to establish real authority or changed retained raw")
	}
}
