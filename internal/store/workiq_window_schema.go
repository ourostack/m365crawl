package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// WorkIQWindowSchemaVersion is the archive schema that added WorkIQ mail windows. An archive below
// it has no window state to read: no window coverage, and its routes are the whole-account ones.
const WorkIQWindowSchemaVersion = 25

// A window member is the latest observation of one Graph message in one window position (a
// frontier). Out-of-window members keep only this row; in-window ones keep their staged page body
// until admission. State: staged, admitted, deferred (the archive already holds a matching row),
// resolved (a deferred member whose Graph ID is now bound) or removed.
const workiqWindowSchemaDDL = `
create table if not exists workiq_window_members(
 frontier_id integer not null references workiq_frontiers(id), graph_id text not null,
 account_id text not null, folder_id text not null,
 version text, received_at text,
 in_window integer not null default 0 check(in_window in (0,1)),
 clock_unknown integer not null default 0 check(clock_unknown in (0,1)),
 removed integer not null default 0 check(removed in (0,1)),
 page_id integer references workiq_pages(id), item_index integer not null default -1,
 state text not null default 'staged' check(state in ('staged','admitted','deferred','resolved','removed')),
 admitted_version text, match_rowid integer, observed_at text not null,
 primary key(frontier_id,graph_id)
);
create index if not exists workiq_window_members_account on workiq_window_members(account_id,state);
create index if not exists workiq_window_members_page on workiq_window_members(page_id);
create table if not exists workiq_window_coverage(
 id integer primary key autoincrement,
 attempt_id integer not null references workiq_attempts(id),
 account_id text not null, folder_id text not null default '',
 clock text not null default 'received' check(clock='received'),
 predicate integer not null default 1 check(predicate=1),
 since text not null, until text not null, snapshot_at text not null,
 deferred integer not null default 0 check(deferred>=0),
 unique(attempt_id,folder_id)
);
create index if not exists workiq_window_coverage_account on workiq_window_coverage(account_id,folder_id,since);
`

// workIQWindowColumns are the columns version 25 adds to tables an older archive already has.
var workIQWindowColumns = []struct{ table, column, definition string }{
	{"workiq_pages", "body_bytes", "integer"},
	{"workiq_pages", "body_sha256", "text"},
	{"workiq_pages", "message_ids_json", "text"},
	{"workiq_pages", "cleared_at", "text"},
	{"workiq_rows", "identity_unproven", "integer not null default 0 check(identity_unproven in (0,1))"},
}

// workIQWindowFault injects a failure at a named migration step; tests only.
var workIQWindowFault = func(step string) error { return nil }

// pragmaConn is the one pinned connection the migration runs on; a test seam.
type pragmaConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Close() error
}

var pinWorkIQConn = func(ctx context.Context, db *sql.DB) (pragmaConn, error) { return db.Conn(ctx) }

// migrateWorkIQWindow brings an earlier WorkIQ archive to version 25: it rebuilds workiq_attempts
// so its kind check allows mail_window, and adds the new columns. crawlkit records the version
// before this runs, so the change is detected from the archive itself and a failed attempt is
// retried by the next writable open. SQLite honors foreign_keys and legacy_alter_table only
// outside a transaction, and only on the connection that sets them, so every statement runs on
// one pinned connection, and both settings are restored afterwards; a restore that fails fails
// the open.
func (s *Store) migrateWorkIQWindow(ctx context.Context) (err error) {
	conn, err := pinWorkIQConn(ctx, s.db)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	// The schema created any missing WorkIQ table current before this runs.
	var attemptsSQL string
	if err := conn.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name='workiq_attempts'`).Scan(&attemptsSQL); err != nil {
		return err
	}
	rebuild := !strings.Contains(attemptsSQL, "'mail_window'")
	var missing []string
	for _, c := range workIQWindowColumns {
		var n int
		if err := conn.QueryRowContext(ctx, `select count(*) from pragma_table_info(?) where name=?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			missing = append(missing, "alter table "+c.table+" add column "+c.column+" "+c.definition)
		}
	}
	if !rebuild && len(missing) == 0 {
		return nil
	}
	var foreignKeys, legacyAlter int
	if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&foreignKeys); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `pragma legacy_alter_table`).Scan(&legacyAlter); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, restoreWorkIQPragmas(ctx, conn, foreignKeys, legacyAlter))
	}()
	if _, err := conn.ExecContext(ctx, `pragma legacy_alter_table=off`); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), `rollback`)
			err = errors.Join(err, rollbackErr)
		}
	}()
	if rebuild {
		if err := rebuildWorkIQAttempts(ctx, conn); err != nil {
			return err
		}
	}
	for _, q := range missing {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := workIQWindowFault("columns"); err != nil {
		return err
	}
	if err := checkWorkIQAttemptKeys(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return err
	}
	committed = true
	return nil
}

func restoreWorkIQPragmas(ctx context.Context, conn pragmaConn, foreignKeys, legacyAlter int) error {
	ctx = context.WithoutCancel(ctx)
	if err := workIQWindowFault("restore"); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`pragma foreign_keys=%d`, foreignKeys)); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`pragma legacy_alter_table=%d`, legacyAlter)); err != nil {
		return err
	}
	var gotKeys, gotLegacy int
	if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&gotKeys); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, `pragma legacy_alter_table`).Scan(&gotLegacy); err != nil {
		return err
	}
	if gotKeys != foreignKeys || gotLegacy != legacyAlter {
		return errors.New("archive settings could not be restored after the WorkIQ window migration")
	}
	return nil
}

// rebuildWorkIQAttempts follows SQLite's procedure for other kinds of table schema changes: copy
// into a new table, drop the old one, rename, then recreate its indexes. Child tables keep their
// text references to workiq_attempts(id), and every row keeps its id.
func rebuildWorkIQAttempts(ctx context.Context, conn pragmaConn) error {
	var dependents int
	if err := conn.QueryRowContext(ctx, `select count(*) from sqlite_master where type in ('view','trigger') and sql like '%workiq_attempts%'`).Scan(&dependents); err != nil {
		return err
	}
	if dependents != 0 {
		return errors.New("a view or trigger depends on workiq_attempts; the WorkIQ window migration cannot rebuild it")
	}
	var indexes []string
	rows, err := conn.QueryContext(ctx, `select sql from sqlite_master where type='index' and tbl_name='workiq_attempts' and sql is not null order by name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			_ = rows.Close()
			return err
		}
		indexes = append(indexes, q)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	var sequence int64
	if err := conn.QueryRowContext(ctx, `select coalesce((select seq from sqlite_sequence where name='workiq_attempts'),0)`).Scan(&sequence); err != nil {
		return err
	}
	const columns = `id,family,account_id,started_at,finished_at,kind,status,window_start,window_end,qualification,
bound_user_id,bound_address,bound_mail,bound_upn,verified_at,error_code,counts_json,gap_count,all_folders`
	for _, q := range []string{
		`create table workiq_attempts_new` + workIQAttemptsColumns,
		`insert into workiq_attempts_new(` + columns + `) select ` + columns + ` from workiq_attempts order by id`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if err := workIQWindowFault("copy"); err != nil {
		return err
	}
	for _, q := range []string{`drop table workiq_attempts`, `alter table workiq_attempts_new rename to workiq_attempts`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, q := range indexes {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `update sqlite_sequence set seq=max(seq,?) where name='workiq_attempts'`, sequence); err != nil {
		return err
	}
	// An empty copy leaves no sequence row; keep the old high-water mark so ids never repeat.
	_, err = conn.ExecContext(ctx, `insert into sqlite_sequence(name,seq) select 'workiq_attempts',?
where ?>0 and not exists(select 1 from sqlite_sequence where name='workiq_attempts')`, sequence, sequence)
	return err
}

// checkWorkIQAttemptKeys checks the foreign keys of workiq_attempts and of every table that
// references it, and only those.
func checkWorkIQAttemptKeys(ctx context.Context, conn pragmaConn) error {
	tables := []string{"workiq_attempts"}
	rows, err := conn.QueryContext(ctx, `select m.name from sqlite_master m join pragma_foreign_key_list(m.name) k
where m.type='table' and k."table"='workiq_attempts' group by m.name order by m.name`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, table := range tables {
		check, err := conn.QueryContext(ctx, `select "table" from pragma_foreign_key_check(?)`, table)
		if err != nil {
			return err
		}
		bad := check.Next()
		if err := errors.Join(check.Err(), check.Close()); err != nil {
			return err
		}
		if bad {
			return errors.New("WorkIQ window migration found a broken reference in " + table)
		}
	}
	return nil
}

// hasWorkIQWindows reports whether the archive holds the window tables.
func (s *Store) hasWorkIQWindows(ctx context.Context) (bool, error) {
	ok, err := s.hasWorkIQ(ctx)
	if err != nil || !ok {
		return false, err
	}
	var tables, column int
	if err := s.db.QueryRowContext(ctx, `select (select count(*) from sqlite_master where type='table' and name in ('workiq_window_members','workiq_window_coverage')),
(select count(*) from pragma_table_info('workiq_rows') where name='identity_unproven')`).Scan(&tables, &column); err != nil {
		return false, err
	}
	return tables == 2 && column == 1, nil
}
