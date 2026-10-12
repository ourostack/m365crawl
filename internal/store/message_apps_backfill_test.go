package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/ourostack/m365crawl/internal/botcitations"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// legacyAppArchive is an archive whose messages were written before application evidence existed:
// no digests, no representations and no backfill marker.
func legacyAppArchive(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	applyApp(t, s, "m2", 1, appSender, absentRaw)
	applyApp(t, s, "m3", 1, appSender, failedRaw)
	applyApp(t, s, "m4", 1, appSender, "")
	for _, q := range []string{`delete from message_app_current`, `delete from message_app_observations`, `update messages set raw_digest=null`, `delete from meta where key='message_app_mapper_version'`} {
		s.qExec(t, q)
	}
	return s
}

// legacyState is what the backfill must never change: the rows' content, hashes, clocks and text
// index, and every other table's counts.
func legacyState(t *testing.T, s *Store) string {
	t.Helper()
	var b strings.Builder
	rows, err := s.db.Query(`select id, content_text, content_hash, updated_at, coalesce(raw_json,'<null>'), version from messages order by rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, text, hash, updated, raw string
		var version int64
		if err := rows.Scan(&id, &text, &hash, &updated, &raw, &version); err != nil {
			t.Fatal(err)
		}
		b.WriteString(id + "|" + text + "|" + hash + "|" + updated + "|" + raw + "\n")
	}
	for _, q := range []string{`select count(*) from message_fts`, `select count(*) from people`, `select count(*) from sync_runs`, `select count(*) from conversations`} {
		b.WriteString(q + "=" + strconv.Itoa(countRows(t, s, q)) + "\n")
	}
	var derivation string
	_ = s.db.QueryRow(`select coalesce((select value from meta where key='derivation_version'),'')`).Scan(&derivation)
	return b.String() + derivation
}

func TestApplicationBackfillIndependent(t *testing.T) {
	ctx := context.Background()
	s := legacyAppArchive(t)
	legacy := legacyState(t, s)
	rep, err := s.BackfillMessageApplications(ctx)
	if err != nil || rep.MapperVersion != MessageApplicationVersion || rep.Processed != 4 || rep.Failed != 2 || rep.Absent != 1 {
		t.Fatalf("first backfill: %+v %v", rep, err)
	}
	if legacyState(t, s) != legacy {
		t.Fatal("backfill changed message content, hashes, clocks, text index or counts")
	}
	if countRows(t, s, `select count(*) from messages where raw_json is not null and raw_digest is null`) != 0 ||
		countRows(t, s, `select count(*) from messages where raw_json is null and raw_digest is not null`) != 0 {
		t.Fatal("backfill left digests missing or invented one for NULL raw")
	}
	want := map[string]string{"m1": AppStateMapped, "m2": AppStateAbsent, "m3": AppStateFailed, "m4": AppStateFailed}
	for id, state := range want {
		if env := appEnvelope(t, s, id); env.State != state || env.BackfillPending {
			t.Fatalf("%s: %+v", id, env)
		}
	}
	clocks := dumpTable(t, s, "message_app_observations") + dumpTable(t, s, "message_app_current")
	again, err := s.BackfillMessageApplications(ctx)
	if err != nil || again.Processed != 0 || again.MapperVersion != MessageApplicationVersion {
		t.Fatalf("settled backfill ran again: %+v %v", again, err)
	}
	if dumpTable(t, s, "message_app_observations")+dumpTable(t, s, "message_app_current") != clocks {
		t.Fatal("an unchanged second backfill wrote representations, pointers or first derivation clocks")
	}

	// A new active mapper version reprocesses every retained raw revision exactly once.
	setApplicationVersion(t, MessageApplicationVersion+1)
	if env := appEnvelope(t, s, "m1"); env.State != AppStateNotDerived || !env.BackfillPending {
		t.Fatalf("before the new version's backfill: %+v", env)
	}
	next, err := s.BackfillMessageApplications(ctx)
	if err != nil || next.Processed != 4 || next.MapperVersion != MessageApplicationVersion+1 {
		t.Fatalf("new version: %+v %v", next, err)
	}
	if again, _ := s.BackfillMessageApplications(ctx); again.Processed != 0 {
		t.Fatalf("new version reprocessed twice: %+v", again)
	}
	if legacyState(t, s) != legacy {
		t.Fatal("a version backfill changed legacy rows")
	}
}

func TestApplicationBackfillRetainedEmptyAndRepairedDigest(t *testing.T) {
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	applyApp(t, s, "m2", 1, appSender, appRaw("Beta"))
	s.qExec(t, `update messages set raw_json='' where id='m1'`)
	s.qExec(t, `update messages set raw_digest='stale' where id='m2'`)
	if _, err := s.BackfillMessageApplications(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := appEnvelope(t, s, "m1")
	if !env.Current.RawPresent || env.Current.RawDigest != digestOf("") || env.State != AppStateFailed || env.FailureCode != "teams_app_raw_invalid" || !env.Retained {
		t.Fatalf("retained SQL empty string: %+v", env)
	}
	if env := appEnvelope(t, s, "m2"); env.State != AppStateMapped || env.Current.RawDigest != digestOf(appRaw("Beta")) {
		t.Fatalf("stale digest was not recomputed from the exact bytes: %+v", env)
	}
}

func TestApplicationBackfillBatches(t *testing.T) {
	s := newStore(t)
	var ms []teamsdesktop.Message
	for i := 0; i < appBackfillBatch+3; i++ {
		ms = append(ms, appMessage(fmt.Sprintf("b%04d", i), 1, appSender, absentRaw))
	}
	must(s.ApplyMessages(context.Background(), ms))
	s.qExec(t, `delete from meta where key='message_app_mapper_version'`)
	rep, err := s.BackfillMessageApplications(context.Background())
	if err != nil || rep.Processed != len(ms) || rep.Absent != len(ms) {
		t.Fatalf("batched backfill: %+v %v (want %d)", rep, err, len(ms))
	}
}

func TestApplicationBackfillRollbackRecovery(t *testing.T) {
	t.Run("faults", func(t *testing.T) {
		sweepFaults(t, func(t *testing.T, s *Store) {
			applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
			applyApp(t, s, "m2", 1, appSender, failedRaw)
			s.qExec(t, `update messages set raw_digest=null`)
			s.qExec(t, `delete from meta where key='message_app_mapper_version'`)
		}, func(ctx context.Context, s *Store) error {
			_, err := s.BackfillMessageApplications(ctx)
			return err
		})
	})
	t.Run("cancellation then retry", func(t *testing.T) {
		s := legacyAppArchive(t)
		before := dump(t, s)
		old := projectApplication
		calls := 0
		projectApplication = func(ctx context.Context, id, sender string, raw []byte, present bool) (teamsdesktop.ApplicationProjection, error) {
			if calls++; calls == 2 {
				return teamsdesktop.ApplicationProjection{}, context.Canceled
			}
			return old(ctx, id, sender, raw, present)
		}
		rep, err := s.BackfillMessageApplications(context.Background())
		projectApplication = old
		if !errors.Is(err, context.Canceled) || rep.Processed != 0 {
			t.Fatalf("cancelled backfill: %+v %v", rep, err)
		}
		if dump(t, s) != before {
			t.Fatal("a cancelled backfill kept partial evidence or its marker")
		}
		if rep, err := s.BackfillMessageApplications(context.Background()); err != nil || rep.Processed != 4 {
			t.Fatalf("retry after rollback: %+v %v", rep, err)
		}
	})
}

func dumpTable(t *testing.T, s *Store, table string) string {
	t.Helper()
	full := dump(t, s)
	var keep []string
	for _, line := range strings.Split(full, "\n") {
		if strings.HasPrefix(line, table+" ") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}

// Version 2 decodes string-encoded bot metadata: its backfill reprocesses a version-1 archive once,
// and the version-1 representations stay as retained history.
func TestApplicationBackfillVersionOneArchive(t *testing.T) {
	ctx := context.Background()
	// Version 1 left the string in place: one teams_bot_metadata_unmapped loss, partial.
	setApplicationVersion(t, 1)
	oldProject := projectApplication
	t.Cleanup(func() { projectApplication = oldProject })
	projectApplication = func(context.Context, string, string, []byte, bool) (teamsdesktop.ApplicationProjection, error) {
		return teamsdesktop.ApplicationProjection{State: AppStatePartial, Losses: []botcitations.Loss{{Code: "teams_bot_metadata_unmapped", Count: 1}}}, nil
	}
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, `{"properties":{"botMetadata":"{\"replyToId\":\"r1\"}"}}`)
	if env := appEnvelope(t, s, "m1"); env.State != AppStatePartial {
		t.Fatalf("version 1 archive: %+v", env)
	}
	if _, err := s.BackfillMessageApplications(ctx); err != nil {
		t.Fatal(err)
	}
	projectApplication = oldProject
	activeApplicationVersion = MessageApplicationVersion
	if rep, err := s.BackfillMessageApplications(ctx); err != nil || rep.Processed != 1 || rep.MapperVersion != 2 {
		t.Fatalf("version 2 backfill: %+v %v", rep, err)
	}
	if rep, _ := s.BackfillMessageApplications(ctx); rep.Processed != 0 {
		t.Fatalf("version 2 reprocessed twice: %+v", rep)
	}
	env := appEnvelope(t, s, "m1")
	if env.State != AppStateMapped || env.Served.Payload.ReplyToID == nil || *env.Served.Payload.ReplyToID != "r1" {
		t.Fatalf("version 2 evidence: %+v", env)
	}
	if countRows(t, s, `select count(*) from message_app_observations where mapper_version=1`) != 1 {
		t.Fatal("version 1 history was not retained")
	}
}

// An older binary on a newer archive neither reprocesses nor rewrites the marker, and does not
// claim a backfill it will never run is pending.
func TestApplicationBackfillNewerMarker(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	applyApp(t, s, "m1", 1, appSender, appRaw("Alpha"))
	s.qExec(t, `insert into meta(key,value) values('message_app_mapper_version','3') on conflict(key) do update set value=excluded.value`)
	before := dumpTable(t, s, "message_app_observations") + dumpTable(t, s, "message_app_current")
	if rep, err := s.BackfillMessageApplications(ctx); err != nil || rep.Processed != 0 {
		t.Fatalf("older binary backfilled a newer archive: %+v %v", rep, err)
	}
	var marker string
	if err := s.db.QueryRow(`select value from meta where key='message_app_mapper_version'`).Scan(&marker); err != nil || marker != "3" {
		t.Fatalf("marker = %q %v", marker, err)
	}
	if dumpTable(t, s, "message_app_observations")+dumpTable(t, s, "message_app_current") != before {
		t.Fatal("older binary wrote evidence")
	}
	if env := appEnvelope(t, s, "m1"); env.BackfillPending {
		t.Fatalf("pending claimed on a newer archive: %+v", env)
	}
	s.qExec(t, `update meta set value='not-a-version' where key='message_app_mapper_version'`)
	if rep, err := s.BackfillMessageApplications(ctx); err != nil || rep.Processed != 1 {
		t.Fatalf("an unreadable marker must reprocess: %+v %v", rep, err)
	}
}
