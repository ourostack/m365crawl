package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/store"
	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// forgetApplications turns an archive back into one written before application evidence existed.
func forgetApplications(t *testing.T, db string) {
	t.Helper()
	for _, q := range []string{`delete from message_app_current`, `delete from message_app_observations`, `update messages set raw_digest=null`, `delete from meta where key='message_app_mapper_version'`} {
		execSQL(t, db, q)
	}
}

// legacyMessages is what the backfill must not change: every message's content, hash and clock.
func legacyMessages(t *testing.T, db string) string {
	t.Helper()
	rows, err := openRaw(t, db).Query(`select id, content_hash, updated_at, content_text from messages order by rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := ""
	for rows.Next() {
		var id, hash, at, text string
		if err := rows.Scan(&id, &hash, &at, &text); err != nil {
			t.Fatal(err)
		}
		out += id + hash + at + text + "\n"
	}
	return out
}

func appDerived(t *testing.T, db string) int {
	t.Helper()
	return recordsQuery(t, db, `select count(*) from messages m join message_app_current p on p.tenant_id=m.tenant_id and p.user_id=m.user_id
 and p.conversation_id=m.conversation_id and p.message_id=m.id where (m.raw_json is null) = (m.raw_digest is null)`)
}

func TestApplicationBackfillWithUnchangedCache(t *testing.T) {
	isolateTmp(t)
	root, db := syncedStart(t)
	forgetApplications(t, db)
	before := legacyMessages(t, db)
	success := readStatus(t, db).LastSuccessAt
	r, _ := run(t, Options{Root: root, DBPath: db})
	if r.Status != StatusUnchanged || r.ApplicationBackfill == nil || r.ApplicationBackfill.Processed != fixtureMessages ||
		r.ApplicationBackfill.MapperVersion != store.MessageApplicationVersion || r.Migrated != nil {
		t.Fatalf("unchanged-cache sync did not backfill once: %+v %+v", r.Status, r.ApplicationBackfill)
	}
	if appDerived(t, db) != fixtureMessages || legacyMessages(t, db) != before || r.Messages.Updated != 0 || r.Messages.Inserted != 0 {
		t.Fatalf("backfill changed legacy rows or left evidence missing: %+v", r.Messages)
	}
	if st := readStatus(t, db); st.LastSuccessAt.Before(success) {
		t.Fatalf("legacy freshness moved backwards: %v -> %v", success, st.LastSuccessAt)
	}
	again, _ := run(t, Options{Root: root, DBPath: db})
	if again.ApplicationBackfill != nil {
		t.Fatalf("settled archive backfilled again: %+v", again.ApplicationBackfill)
	}
}

func TestApplicationBackfillWithAbsentOrFailedCache(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) string{
		"absent": func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-teams", "EBWebView") },
		"failed": func(t *testing.T) string {
			old := discoverSources
			discoverSources = func(string) ([]teamsdesktop.Source, []string, error) {
				return nil, nil, errors.New("injected discovery failure")
			}
			t.Cleanup(func() { discoverSources = old })
			return fixtureRoot
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolateTmp(t)
			_, db := syncedStart(t)
			forgetApplications(t, db)
			before := legacyMessages(t, db)
			if _, _, err := Run(context.Background(), Options{Root: setup(t), DBPath: db}); err == nil {
				t.Fatal("a sync without a readable cache succeeded")
			}
			if appDerived(t, db) != fixtureMessages || legacyMessages(t, db) != before ||
				recordsQuery(t, db, `select count(*) from meta where key='message_app_mapper_version'`) != 1 {
				t.Fatal("the backfill depends on the cache capture")
			}
		})
	}
}

func TestApplicationBackfillFailureIsExplicit(t *testing.T) {
	isolateTmp(t)
	root, db := syncedStart(t)
	forgetApplications(t, db)
	old, oldDiscover := backfillApplications, discoverSources
	t.Cleanup(func() { backfillApplications, discoverSources = old, oldDiscover })
	backfillApplications = func(*store.Store, context.Context) (store.ApplicationBackfillReport, error) {
		return store.ApplicationBackfillReport{}, errors.New("injected backfill failure")
	}
	discovered := false
	discoverSources = func(r string) ([]teamsdesktop.Source, []string, error) { discovered = true; return oldDiscover(r) }
	_, _, err := Run(context.Background(), Options{Root: root, DBPath: db})
	codedErr(t, err, errs.CodeDBError)
	if discovered || readStatus(t, db).LastRun.Status != StatusFailed {
		t.Fatal("a failed backfill was not reported before the capture")
	}
	backfillApplications = old
	if r, _ := run(t, Options{Root: root, DBPath: db}); r.ApplicationBackfill == nil || r.ApplicationBackfill.Processed != fixtureMessages {
		t.Fatalf("the next sync did not retry the backfill: %+v", r.ApplicationBackfill)
	}
}

func TestApplicationChangedMessageAtomicPublication(t *testing.T) {
	isolateTmp(t)
	hookFlush(t, 10, func(kind string, _ int) error {
		if kind == "activity" {
			return errors.New("injected activity failure")
		}
		return nil
	})
	db := newDB(t)
	if _, _, err := Run(context.Background(), Options{Root: fixtureRoot, DBPath: db}); err == nil {
		t.Fatal("the injected failure was swallowed")
	}
	if n := recordsQuery(t, db, `select count(*) from message_app_observations`) + recordsQuery(t, db, `select count(*) from message_app_current`); n != 0 {
		t.Fatalf("a rolled-back source kept %d evidence rows", n)
	}
	beforeFlush = func(string, int) error { return nil }
	r, _ := run(t, Options{Root: fixtureRoot, DBPath: db})
	if r.Messages.Inserted != fixtureMessages || appDerived(t, db) != fixtureMessages || r.ApplicationBackfill != nil {
		t.Fatalf("changed messages did not publish their evidence with their rows: %+v %+v", r.Messages, r.ApplicationBackfill)
	}
}
