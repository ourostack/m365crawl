package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	crawlstore "github.com/openclaw/crawlkit/store"
)

// downgradeWorkIQWindowSchema puts an open archive back to the version 22 WorkIQ layout: the
// attempts kind check without mail_window and none of the version 23 columns.
func downgradeWorkIQWindowSchema(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	old := strings.Replace(workIQAttemptsColumns, ",'mail_window'", "", 1)
	for _, q := range []string{
		`pragma foreign_keys=off`, `begin`,
		`create table workiq_attempts_old` + old,
		`insert into workiq_attempts_old select id,family,account_id,started_at,finished_at,kind,status,window_start,window_end,qualification,
bound_user_id,bound_address,bound_mail,bound_upn,verified_at,error_code,counts_json,gap_count,all_folders from workiq_attempts`,
		`drop table workiq_attempts`, `alter table workiq_attempts_old rename to workiq_attempts`,
		`create index if not exists workiq_attempts_account on workiq_attempts(family,account_id,id)`,
		`alter table workiq_pages drop column body_bytes`, `alter table workiq_pages drop column body_sha256`,
		`alter table workiq_pages drop column message_ids_json`, `alter table workiq_pages drop column cleared_at`,
		`alter table workiq_rows drop column identity_unproven`, `commit`, `pragma foreign_keys=on`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func workIQPragmas(t *testing.T, s *Store) (int, int) {
	t.Helper()
	var keys, legacy int
	must0(s.db.QueryRowContext(context.Background(), `pragma foreign_keys`).Scan(&keys))
	must0(s.db.QueryRowContext(context.Background(), `pragma legacy_alter_table`).Scan(&legacy))
	return keys, legacy
}

func attemptsAllowWindows(t *testing.T, s *Store) bool {
	t.Helper()
	var q string
	must0(s.db.QueryRowContext(context.Background(), `select sql from sqlite_master where name='workiq_attempts'`).Scan(&q))
	return strings.Contains(q, "'mail_window'")
}

func TestWorkIQWindowMigrationKeepsEveryAttemptAndReference(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s := must(Open(ctx, path))
	attempt := seedCompletedWorkIQMail(t, s)
	proof := must(s.InspectWorkIQMailCompletion(ctx, attempt))
	must0(s.CompleteWorkIQAttempt(ctx, attempt, WorkIQSummary{Status: "ok", FinishedAt: mailT0, AllFolders: true, MailProof: &proof}))
	second := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "coverage"}, mailT0))
	keys, legacy := workIQPragmas(t, s)
	before := dumpTable(t, s, "workiq_attempts") + dumpTable(t, s, "workiq_censuses") +
		dumpTable(t, s, "workiq_gaps") + dumpTable(t, s, "workiq_mail_receipts")
	pages := rowCount(t, s, `select count(*) from workiq_pages`)
	members := rowCount(t, s, `select count(*) from workiq_census_members`)
	downgradeWorkIQWindowSchema(t, s)
	if attemptsAllowWindows(t, s) || rowCount(t, s, `select count(*) from pragma_table_info('workiq_rows') where name='identity_unproven'`) != 0 {
		t.Fatal("the fixture did not reproduce a version 22 archive")
	}
	// A sequence above every id must survive the rebuild.
	must(s.db.ExecContext(ctx, `update sqlite_sequence set seq=40 where name='workiq_attempts'`))
	must0(s.Close())
	s = must(Open(ctx, path))
	defer func() { _ = s.Close() }()
	after := dumpTable(t, s, "workiq_attempts") + dumpTable(t, s, "workiq_censuses") +
		dumpTable(t, s, "workiq_gaps") + dumpTable(t, s, "workiq_mail_receipts")
	if !attemptsAllowWindows(t, s) || members != rowCount(t, s, `select count(*) from workiq_census_members`) ||
		pages != rowCount(t, s, `select count(*) from workiq_pages where body_bytes is null`) || before != after || before == "" {
		t.Fatal("the migration did not add the window kind, or changed retained attempts, pages or members")
	}
	if rowCount(t, s, `select count(*) from pragma_foreign_key_check`) != 0 ||
		rowCount(t, s, `select count(*) from workiq_attempts where id in (?,?)`, attempt, second) != 2 ||
		rowCount(t, s, `select count(*) from workiq_mail_receipts r join workiq_attempts a on a.id=r.attempt_id`) != 1 ||
		rowCount(t, s, `select count(*) from sqlite_master where type='index' and name='workiq_attempts_account'`) != 1 {
		t.Fatal("the rebuild broke a reference or lost an index")
	}
	if k, l := workIQPragmas(t, s); k != keys || l != legacy {
		t.Fatalf("pragmas not restored: foreign_keys %d (was %d), legacy_alter_table %d (was %d)", k, keys, l, legacy)
	}
	window := must(s.BeginWorkIQAttempt(ctx, WorkIQScope{Family: "mail", Account: mailAcct, Kind: "mail_window",
		WindowStart: mailT0.Add(-48 * 3600e9), WindowEnd: mailT0}, mailT0))
	if window <= 40 {
		t.Fatalf("a new attempt reused an id at or below the old sequence: %d", window)
	}
	for _, kind := range []string{"sync", "coverage", "status"} {
		must(s.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,kind) values('mail',?,?,?)`, mailAcct, fmtTime(mailT0), kind))
	}
	must(s.db.ExecContext(ctx, `insert into workiq_rows(kind,account_id,item_rowid,graph_id,server_json,cache_first,cache_seen,winner,modified_at,identity_unproven)
values('mail',?,999,'g','{}',0,0,'workiq_server',?,1)`, mailAcct, fmtTime(mailT0)))
	must(s.db.ExecContext(ctx, `update workiq_pages set body_bytes=1,body_sha256='x',message_ids_json='[]',cleared_at=? where id=1`, fmtTime(mailT0)))
	ok := must(s.hasWorkIQWindows(ctx))
	if !ok {
		t.Fatal("the migrated archive does not report its window tables")
	}
	// A second open finds nothing to do.
	snapshot := dumpTable(t, s, "workiq_attempts")
	must0(s.migrateWorkIQWindow(ctx))
	if dumpTable(t, s, "workiq_attempts") != snapshot {
		t.Fatal("a second migration changed the attempts")
	}
}

func TestWorkIQWindowMigrationFailureKeepsTheOldTableAndSettings(t *testing.T) {
	ctx := context.Background()
	for _, step := range []string{"copy", "columns"} {
		t.Run(step, func(t *testing.T) {
			s := newStore(t)
			seedCompletedWorkIQMail(t, s)
			downgradeWorkIQWindowSchema(t, s)
			keys, legacy := workIQPragmas(t, s)
			before := dumpTable(t, s, "workiq_attempts")
			workIQWindowFault = func(at string) error {
				if at == step {
					return errors.New("injected")
				}
				return nil
			}
			t.Cleanup(func() { workIQWindowFault = func(string) error { return nil } })
			if err := s.migrateWorkIQWindow(ctx); err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("err = %v", err)
			}
			if attemptsAllowWindows(t, s) || dumpTable(t, s, "workiq_attempts") != before ||
				rowCount(t, s, `select count(*) from sqlite_master where name='workiq_attempts_new'`) != 0 ||
				rowCount(t, s, `select count(*) from pragma_table_info('workiq_rows') where name='identity_unproven'`) != 0 {
				t.Fatal("a failed migration kept part of its change")
			}
			if k, l := workIQPragmas(t, s); k != keys || l != legacy {
				t.Fatal("a failed migration left the archive settings changed")
			}
			workIQWindowFault = func(string) error { return nil }
			must0(s.migrateWorkIQWindow(ctx))
			if !attemptsAllowWindows(t, s) {
				t.Fatal("the next attempt did not finish the migration")
			}
		})
	}
}

func TestWorkIQWindowMigrationRefusesBrokenReferencesAndDependents(t *testing.T) {
	ctx := context.Background()
	t.Run("orphaned child", func(t *testing.T) {
		s := newStore(t)
		seedCompletedWorkIQMail(t, s)
		downgradeWorkIQWindowSchema(t, s)
		must(s.db.ExecContext(ctx, `pragma foreign_keys=off`))
		must(s.db.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,n) values(9999,'workiq_orphan',1)`))
		must(s.db.ExecContext(ctx, `pragma foreign_keys=on`))
		err := s.migrateWorkIQWindow(ctx)
		if err == nil || !strings.Contains(err.Error(), "workiq_gaps") || attemptsAllowWindows(t, s) {
			t.Fatalf("an orphaned reference did not roll the migration back: %v", err)
		}
	})
	t.Run("dependent view", func(t *testing.T) {
		s := newStore(t)
		seedCompletedWorkIQMail(t, s)
		downgradeWorkIQWindowSchema(t, s)
		must(s.db.ExecContext(ctx, `create view workiq_attempt_view as select id from workiq_attempts`))
		if err := s.migrateWorkIQWindow(ctx); err == nil || !strings.Contains(err.Error(), "view or trigger") || attemptsAllowWindows(t, s) {
			t.Fatalf("a dependent view was rebuilt around: %v", err)
		}
	})
	t.Run("empty attempts keep their sequence", func(t *testing.T) {
		s := newStore(t)
		downgradeWorkIQWindowSchema(t, s)
		must(s.db.ExecContext(ctx, `update sqlite_sequence set seq=7 where name='workiq_attempts'`))
		must(s.db.ExecContext(ctx, `insert into sqlite_sequence(name,seq) select 'workiq_attempts',7 where not exists(select 1 from sqlite_sequence where name='workiq_attempts')`))
		must0(s.migrateWorkIQWindow(ctx))
		if rowCount(t, s, `select count(*) from sqlite_sequence where name='workiq_attempts' and seq=7`) != 1 {
			t.Fatal("an empty rebuild lost the attempts sequence")
		}
	})
}

type failingPragmaConn struct {
	pragmaConn
	fail string
}

func (c failingPragmaConn) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if strings.Contains(q, c.fail) {
		return nil, errors.New("injected " + c.fail)
	}
	return c.pragmaConn.ExecContext(ctx, q, args...)
}

func TestWorkIQWindowMigrationFailsTheOpenWhenSettingsCannotBeRestored(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s := must(Open(ctx, path))
	downgradeWorkIQWindowSchema(t, s)
	must0(s.Close())
	workIQWindowFault = func(step string) error {
		if step == "restore" {
			return errors.New("injected restore")
		}
		return nil
	}
	t.Cleanup(func() { workIQWindowFault = func(string) error { return nil } })
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "injected restore") {
		t.Fatalf("an unrestored setting did not fail the open: %v", err)
	}
	workIQWindowFault = func(string) error { return nil }
	for _, fail := range []string{"pragma foreign_keys=1", "pragma legacy_alter_table=0", "begin immediate", "alter table workiq_rows", "commit", "rollback"} {
		t.Run(fail, func(t *testing.T) {
			s := newStore(t)
			downgradeWorkIQWindowSchema(t, s)
			pinWorkIQConn = func(ctx context.Context, db *sql.DB) (pragmaConn, error) {
				c, err := db.Conn(ctx)
				return failingPragmaConn{pragmaConn: c, fail: fail}, err
			}
			t.Cleanup(func() {
				pinWorkIQConn = func(ctx context.Context, db *sql.DB) (pragmaConn, error) { return db.Conn(ctx) }
			})
			if fail == "rollback" {
				workIQWindowFault = func(step string) error {
					if step == "columns" {
						return errors.New("injected columns")
					}
					return nil
				}
				t.Cleanup(func() { workIQWindowFault = func(string) error { return nil } })
			}
			if err := s.migrateWorkIQWindow(ctx); err == nil {
				t.Fatal("an injected failure was ignored")
			}
		})
	}
}

// TestWorkIQWindowMigrationFromRecordedVersions opens archives an earlier build left at version 24
// (before the directed write log), 26 (with it) and 28 (with its reply drafts), and a fresh one:
// the window schema is detected from the tables, not the recorded version, so each reaches
// SchemaVersion with its data kept.
func TestWorkIQWindowMigrationFromRecordedVersions(t *testing.T) {
	ctx := context.Background()
	fresh := newStore(t)
	if rowCount(t, fresh, `select max(version) from schema_migrations`) != SchemaVersion || !attemptsAllowWindows(t, fresh) ||
		!must(fresh.hasWorkIQWindows(ctx)) || rowCount(t, fresh, `select count(*) from sqlite_master where type='table' and name like 'directed_write%'`) != 3 {
		t.Fatal("a fresh archive lacks the window or directed write schema")
	}
	drafts := directedWriteSchemaDDL[strings.Index(directedWriteSchemaDDL, "create table if not exists directed_write_drafts("):strings.Index(directedWriteSchemaDDL, "create table if not exists directed_write_events(")]
	for _, version := range []int{24, DirectedWriteSchemaVersion, DirectedDraftSchemaVersion} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := writableArchivePath(t)
			ddl := strings.Replace(schemaDDL, workiqWindowSchemaDDL, "", 1)
			switch {
			case version < DirectedWriteSchemaVersion:
				ddl = strings.Replace(ddl, directedWriteSchemaDDL, "", 1)
			case version < DirectedDraftSchemaVersion:
				ddl = strings.Replace(ddl, drafts, "", 1)
			}
			cs := must(crawlstore.Open(ctx, crawlstore.Options{Path: path, Schema: ddl, SchemaVersion: version}))
			legacy := &Store{cs: cs, db: cs.DB()}
			downgradeWorkIQWindowSchema(t, legacy)
			must(legacy.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,kind,status) values('mail',?,?,'sync','ok')`, mailAcct, fmtTime(mailT0)))
			if rowCount(t, legacy, `select max(version) from schema_migrations`) != version || attemptsAllowWindows(t, legacy) ||
				rowCount(t, legacy, `select count(*) from sqlite_master where name like 'workiq_window_%'`) != 0 {
				t.Fatalf("the fixture is not a version %d archive", version)
			}
			want := map[int]int{24: 0, DirectedWriteSchemaVersion: 2, DirectedDraftSchemaVersion: 3}[version]
			if directed := rowCount(t, legacy, `select count(*) from sqlite_master where type='table' and name like 'directed_write%'`); directed != want {
				t.Fatalf("the version %d fixture has %d directed write tables, want %d", version, directed, want)
			}
			attempts := windowAttemptRows(t, legacy)
			must0(legacy.Close())
			s := must(Open(ctx, path))
			defer func() { _ = s.Close() }()
			if got := rowCount(t, s, `select max(version) from schema_migrations`); got != SchemaVersion {
				t.Fatalf("version %d archive is at %d, want %d", version, got, SchemaVersion)
			}
			if !attemptsAllowWindows(t, s) || !must(s.hasWorkIQWindows(ctx)) || windowAttemptRows(t, s) != attempts || attempts == "" ||
				rowCount(t, s, `select count(*) from pragma_table_info('workiq_rows') where name='identity_unproven'`) != 1 ||
				rowCount(t, s, `select count(*) from sqlite_master where type='table' and name like 'directed_write%'`) != 3 {
				t.Fatalf("version %d archive did not gain the window schema with its attempts kept", version)
			}
			must(s.db.ExecContext(ctx, `insert into workiq_attempts(family,account_id,started_at,kind) values('mail',?,?,'mail_window')`, mailAcct, fmtTime(mailT0)))
		})
	}
}

func windowAttemptRows(t *testing.T, s *Store) string {
	t.Helper()
	var out sql.NullString
	must0(s.db.QueryRowContext(context.Background(), `select group_concat(id||'/'||family||'/'||account_id||'/'||kind||'/'||status||'/'||started_at,'|') from workiq_attempts`).Scan(&out))
	return out.String
}
