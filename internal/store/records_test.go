package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/teamscrawl/internal/errs"
	"github.com/ourostack/teamscrawl/internal/teamsdesktop"
)

const (
	srcA = "profile|https_teams.example_0"
	dbA  = "Teams:calendar-manager:react-web-client:" + "00000000-0000-4000-8000-0000000000a1"
)

func grec(a *teamsdesktop.Account, db, st, key, value string) teamsdesktop.GenericRecord {
	r := teamsdesktop.GenericRecord{Account: a, Database: db, Store: st, KeyJSON: []byte(key)}
	if value != "" {
		r.ValueJSON = []byte(value)
	}
	return r
}

func upsert(t *testing.T, s *Store, rs []teamsdesktop.GenericRecord, at time.Time) Counts {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	c, err := x.UpsertRecords(srcA, rs, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
	return c
}

func inSession(t *testing.T, s *Store, fn func(x *Session)) {
	t.Helper()
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer x.Rollback()
	fn(x)
	if err := x.Commit(); err != nil {
		t.Fatal(err)
	}
}

// recRow reads one stored row's value, removed_at and updated_at as text.
func recRow(t *testing.T, s *Store, db, st, key string) (value, removed, updated string) {
	t.Helper()
	var v, r sql.NullString
	err := s.db.QueryRow(`select value_json, removed_at, updated_at from records where database=? and store=? and key_json=?`, db, st, key).Scan(&v, &r, &updated)
	if err != nil {
		t.Fatalf("row %s/%s/%s: %v", db, st, key, err)
	}
	return v.String, r.String, updated
}

func TestUpsertRecordsInsertUpdateUnchanged(t *testing.T) {
	s := newStore(t)
	rs := []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `{"n":1}`),
		grec(nil, "Teams:odd", "things", `1`, `{"n":2}`),
	}
	if c := upsert(t, s, rs, base); c != (Counts{Seen: 2, Inserted: 2}) {
		t.Fatalf("insert: %+v", c)
	}
	var tenant, user string
	if err := s.db.QueryRow(`select tenant_id, user_id from records where database='Teams:odd'`).Scan(&tenant, &user); err != nil || tenant != "" || user != "" {
		t.Fatalf("unparsed name keeps empty account: %q %q %v", tenant, user, err)
	}
	if c := upsert(t, s, rs, base.Add(time.Hour)); c != (Counts{Seen: 2, Unchanged: 2}) {
		t.Fatalf("unchanged: %+v", c)
	}
	if _, _, updated := recRow(t, s, dbA, "events", `"e1"`); updated != fmtTime(base) {
		t.Fatalf("unchanged row moved updated_at to %s", updated)
	}
	rs[0] = grec(&acctA, dbA, "events", `"e1"`, `{"n":3}`)
	if c := upsert(t, s, rs, base.Add(2*time.Hour)); c != (Counts{Seen: 2, Updated: 1, Unchanged: 1}) {
		t.Fatalf("update: %+v", c)
	}
	v, _, updated := recRow(t, s, dbA, "events", `"e1"`)
	if v != `{"n":3}` || updated != fmtTime(base.Add(2*time.Hour)) {
		t.Fatalf("updated row: %s %s", v, updated)
	}
	var first string
	if err := s.db.QueryRow(`select first_seen_at from records where key_json='"e1"'`).Scan(&first); err != nil || first != fmtTime(base) {
		t.Fatalf("first_seen_at = %s %v", first, err)
	}
}

func TestUpsertRecordsNilValueKeepsStored(t *testing.T) {
	s := newStore(t)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, `{"n":1}`)}, base)
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, "")}, base.Add(time.Hour)); c != (Counts{Seen: 1, Unchanged: 1}) {
		t.Fatalf("nil over stored: %+v", c)
	}
	if v, _, _ := recRow(t, s, dbA, "events", `"e1"`); v != `{"n":1}` {
		t.Fatalf("stored value overwritten: %q", v)
	}
	// A key whose value never decoded is archived with a NULL value, and a later decode fills it.
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e2"`, "")}, base); c.Inserted != 1 {
		t.Fatalf("nil insert: %+v", c)
	}
	var isNull int
	if err := s.db.QueryRow(`select value_json is null from records where key_json='"e2"'`).Scan(&isNull); err != nil || isNull != 1 {
		t.Fatalf("value_json should be NULL: %d %v", isNull, err)
	}
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e2"`, `{"ok":true}`)}, base); c.Updated != 1 {
		t.Fatalf("late value: %+v", c)
	}
	// A nil value seen on a removed row still revives it, and keeps the value.
	inSession(t, s, func(x *Session) {
		if n, err := x.MarkDatabasesRemoved(srcA, nil, nil, base); err != nil || n != 2 {
			t.Fatalf("remove: %d %v", n, err)
		}
	})
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, "")}, base.Add(time.Hour)); c.Updated != 1 {
		t.Fatalf("revive: %+v", c)
	}
	if v, removed, _ := recRow(t, s, dbA, "events", `"e1"`); v != `{"n":1}` || removed != "" {
		t.Fatalf("revived row: %q removed=%q", v, removed)
	}
}

func TestMarkRecordsRemovedSticky(t *testing.T) {
	s := newStore(t)
	other := "Teams:other"
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `1`),
		grec(&acctA, dbA, "events", `"e2"`, `2`),
		grec(&acctA, dbA, "gone", `"g1"`, `3`),
		grec(&acctA, other, "events", `"e1"`, `4`),
	}, base)
	seen := map[string]map[string]struct{}{"events": {`"e1"`: {}}}
	inSession(t, s, func(x *Session) {
		n, err := x.MarkRecordsRemoved(srcA, dbA, seen, base.Add(time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("removed %d, %v; want e2 and the whole 'gone' store", n, err)
		}
	})
	if _, r, _ := recRow(t, s, dbA, "events", `"e2"`); r != fmtTime(base.Add(time.Hour)) {
		t.Fatalf("e2 removed_at = %q", r)
	}
	if _, r, _ := recRow(t, s, dbA, "events", `"e1"`); r != "" {
		t.Fatalf("seen row marked removed: %q", r)
	}
	if _, r, _ := recRow(t, s, other, "events", `"e1"`); r != "" {
		t.Fatalf("another database was touched: %q", r)
	}
	// Sticky: a second pass keeps the first removal time and counts nothing.
	inSession(t, s, func(x *Session) {
		if n, err := x.MarkRecordsRemoved(srcA, dbA, seen, base.Add(2*time.Hour)); err != nil || n != 0 {
			t.Fatalf("second pass: %d %v", n, err)
		}
	})
	if _, r, _ := recRow(t, s, dbA, "events", `"e2"`); r != fmtTime(base.Add(time.Hour)) {
		t.Fatalf("removed_at moved: %q", r)
	}
	// Seen again: the row returns and removed_at clears.
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e2"`, `2`)}, base.Add(3*time.Hour)); c.Updated != 1 {
		t.Fatalf("reappear: %+v", c)
	}
	if v, r, updated := recRow(t, s, dbA, "events", `"e2"`); r != "" || v != "2" || updated != fmtTime(base.Add(3*time.Hour)) {
		t.Fatalf("reappeared row: %q %q %q", v, r, updated)
	}
	if n := rowCount(t, s, `select count(*) from records`); n != 4 {
		t.Fatalf("rows deleted: %d", n)
	}
}

func rowCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMarkDatabasesRemovedAbsentDatabase(t *testing.T) {
	s := newStore(t)
	dbB := "Teams:pinned-manager:react-web-client:" + acctA.UserID
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `1`),
		grec(&acctA, dbA, "events", `"e2"`, `2`),
		grec(&acctA, dbB, "pins", `"p1"`, `3`),
	}, base)
	inSession(t, s, func(x *Session) {
		n, err := x.MarkDatabasesRemoved(srcA, []string{dbB}, nil, base.Add(time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("marked %d, %v", n, err)
		}
	})
	if _, r, _ := recRow(t, s, dbA, "events", `"e1"`); r != fmtTime(base.Add(time.Hour)) {
		t.Fatalf("absent database not marked: %q", r)
	}
	if _, r, _ := recRow(t, s, dbB, "pins", `"p1"`); r != "" {
		t.Fatalf("present database marked: %q", r)
	}
	if n := rowCount(t, s, `select count(*) from records`); n != 3 {
		t.Fatalf("rows deleted: %d", n)
	}
	// Another source's rows are never touched.
	x, _ := s.Begin(context.Background())
	if _, err := x.UpsertRecords("other|source", []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, `9`)}, base); err != nil {
		t.Fatal(err)
	}
	if n, err := x.MarkDatabasesRemoved(srcA, nil, nil, base); err != nil || n != 1 {
		t.Fatalf("second source: %d %v (only dbB's live row should change)", n, err)
	}
	x.Rollback()
	// The database returns: its rows come back with removed_at cleared.
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, `1`)}, base.Add(2*time.Hour)); c.Updated != 1 {
		t.Fatalf("reappear: %+v", c)
	}
	if _, r, _ := recRow(t, s, dbA, "events", `"e1"`); r != "" {
		t.Fatalf("reappeared database keeps removed_at: %q", r)
	}
}

func TestMarkDatabasesRemovedRespectsAccount(t *testing.T) {
	s := newStore(t)
	dbOther := "Teams:calendar-manager:react-web-client:" + acctB.UserID
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `1`),
		grec(&acctB, dbOther, "events", `"e1"`, `2`),
		grec(nil, "Teams:unparsed", "things", `1`, `3`),
	}, base)
	inSession(t, s, func(x *Session) {
		n, err := x.MarkDatabasesRemoved(srcA, nil, &acctA, base.Add(time.Hour))
		if err != nil || n != 1 {
			t.Fatalf("marked %d, %v; a filtered read saw only account A", n, err)
		}
	})
	if _, r, _ := recRow(t, s, dbOther, "events", `"e1"`); r != "" {
		t.Fatalf("another account's database marked: %q", r)
	}
	if _, r, _ := recRow(t, s, "Teams:unparsed", "things", `1`); r != "" {
		t.Fatalf("an unparsed database marked under an account filter: %q", r)
	}
	if _, r, _ := recRow(t, s, dbA, "events", `"e1"`); r == "" {
		t.Fatal("the filtered account's database should be marked")
	}
}

func TestRecordsRollbackWithSource(t *testing.T) {
	s := newStore(t)
	x, err := s.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, `1`)}, base); err != nil {
		t.Fatal(err)
	}
	x.Rollback()
	if n := rowCount(t, s, `select count(*) from records`); n != 0 {
		t.Fatalf("%d records survived a rolled-back session", n)
	}
}

func TestSchemaMigratesV2ToV3(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	must0(s.ApplyAccount(ctx, acctA))
	must(s.ApplyConversations(ctx, []teamsdesktop.Conversation{conv(acctA, "c1", "Chat", "One")}))
	must(s.ApplyMessages(ctx, []teamsdesktop.Message{msg(acctA, "c1", "m1", "hello", base)}))
	// Make it a v2 archive: no records table, version 2.
	for _, q := range []string{`drop table records`, `update schema_migrations set version = 2`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open v2 archive: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if v := rowCount(t, s, `select version from schema_migrations`); v != SchemaVersion || SchemaVersion != 3 {
		t.Fatalf("version %d, want 3", v)
	}
	if n := rowCount(t, s, `select count(*) from sqlite_master where name in ('records','records_db_store','records_updated')`); n != 3 {
		t.Fatalf("records table and index: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from messages`); n != 1 {
		t.Fatalf("messages touched: %d", n)
	}
	if n := rowCount(t, s, `select count(*) from conversations`); n != 1 {
		t.Fatalf("conversations touched: %d", n)
	}
}

func TestArchiveNewerV3(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update schema_migrations set version = 4`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err = Open(ctx, path)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer || !strings.Contains(coded.Message, "schema version 4") {
		t.Fatalf("Open of a v4 archive = %v, want archive_newer", err)
	}
}

func TestOpenReadOnlyRefusesANewerArchive(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update schema_migrations set version = 4`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	_, err = OpenReadOnly(ctx, path)
	var coded *errs.Coded
	if !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer {
		t.Fatalf("OpenReadOnly of a v4 archive = %v, want archive_newer", err)
	}
	if _, err := OpenReadOnly(ctx, filepath.Join(t.TempDir(), "none.db")); !errors.Is(err, ErrNoArchive) {
		t.Fatalf("missing archive: %v", err)
	}
}

func writableArchivePath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		setCurrentUserAndSystemOnly(t, dir)
	}
	return filepath.Join(dir, "teamscrawl.db")
}

func TestOpenIgnoresAFileWithoutAVersionTable(t *testing.T) {
	// checkSchemaNotNewer leaves a file with no schema_migrations table to crawlkit's own open.
	path := filepath.Join(t.TempDir(), "plain.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table t(x)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if err := checkSchemaNotNewer(context.Background(), path); err != nil {
		t.Fatalf("plain database: %v", err)
	}
	if err := checkSchemaNotNewer(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}

func TestCheckSchemaNotNewerHandlesHashInPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash#archive.db")
	if runtime.GOOS == "windows" {
		setCurrentUserAndSystemOnly(t, filepath.Dir(path))
	}
	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	if _, err := st.db.Exec(`update schema_migrations set version=?`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	var coded *errs.Coded
	err = checkSchemaNotNewer(context.Background(), path)
	if !errors.As(err, &coded) || coded.Code != errs.CodeArchiveNewer {
		t.Fatalf("checkSchemaNotNewer(%q) = %v, want archive_newer", path, err)
	}
}

func TestSQLiteFileURI(t *testing.T) {
	got := sqliteFileURI(`C:\tmp\hash#archive.db`, "mode=ro")
	if got != "file:///C:/tmp/hash%23archive.db?mode=ro" {
		t.Fatalf("sqliteFileURI(windows) = %q", got)
	}
	got = sqliteFileURI("/tmp/hash#archive.db", "")
	if got != "file:///tmp/hash%23archive.db" {
		t.Fatalf("sqliteFileURI(unix) = %q", got)
	}
}

func seedRecords(t *testing.T, s *Store) {
	t.Helper()
	dbB := "Teams:calendar-manager:react-web-client:" + acctB.UserID
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `{"n":1}`),
		grec(&acctA, dbA, "events", `"e2"`, `{"n":2}`),
		grec(&acctA, dbA, "other", `"o1"`, ``),
		grec(&acctB, dbB, "events", `"b1"`, `{"n":3}`),
	}, base)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e2"`, `{"n":4}`)}, base.Add(time.Hour))
	inSession(t, s, func(x *Session) {
		if _, err := x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"events": {`"e1"`: {}, `"e2"`: {}}}, base.Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStoresListsCountsPerStore(t *testing.T) {
	s := newStore(t)
	seedRecords(t, s)
	rows, err := s.Stores(context.Background(), nil)
	if err != nil || len(rows) != 3 {
		t.Fatalf("%v %v", rows, err)
	}
	dbB := "Teams:calendar-manager:react-web-client:" + acctB.UserID
	want := []StoreRow{
		{Database: dbA, Store: "events", Records: 2, LastUpdatedAt: base.Add(time.Hour)},
		{Database: dbA, Store: "other", Removed: 1, LastUpdatedAt: base},
		{Database: dbB, Store: "events", Records: 1, LastUpdatedAt: base},
	}
	if rows[0] != want[0] || rows[1] != want[1] || rows[2] != want[2] {
		t.Fatalf("got %+v\nwant %+v", rows, want)
	}
	rows, err = s.Stores(context.Background(), &acctB)
	if err != nil || len(rows) != 1 || rows[0].Database != dbB {
		t.Fatalf("account filter: %+v %v", rows, err)
	}
}

func TestRecordsFilters(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedRecords(t, s)
	var total int
	rows, trunc, err := s.Records(ctx, RecordFilter{Database: "Teams:calendar-manager", Limit: 2, Total: &total})
	if err != nil || !trunc || len(rows) != 2 || total != 3 {
		t.Fatalf("prefix, truncated: %v %v %d %v", rows, trunc, total, err)
	}
	if rows[0].Database != dbA || rows[0].KeyJSON != `"e2"` || rows[0].ValueJSON != `{"n":4}` || rows[0].UpdatedAt != base.Add(time.Hour) {
		t.Fatalf("newest first: %+v", rows[0])
	}
	rows, trunc, err = s.Records(ctx, RecordFilter{Database: dbA, Store: "events", Account: &acctA})
	if err != nil || trunc || len(rows) != 2 {
		t.Fatalf("exact name, store, account: %v %v", rows, err)
	}
	rows, _, _ = s.Records(ctx, RecordFilter{Database: "Teams:", Since: base.Add(30 * time.Minute)})
	if len(rows) != 1 || rows[0].KeyJSON != `"e2"` {
		t.Fatalf("since: %+v", rows)
	}
	// A removal counts as a change: --since sees a row removed after the cutoff.
	rows, _, _ = s.Records(ctx, RecordFilter{Database: dbA, Store: "other", IncludeRemoved: true, Since: base.Add(time.Hour + 30*time.Minute)})
	if len(rows) != 1 || rows[0].KeyJSON != `"o1"` {
		t.Fatalf("since a removal: %+v", rows)
	}
	rows, _, _ = s.Records(ctx, RecordFilter{Database: dbA, Store: "other"})
	if len(rows) != 0 {
		t.Fatalf("removed rows hidden by default: %+v", rows)
	}
	rows, _, _ = s.Records(ctx, RecordFilter{Database: dbA, Store: "other", IncludeRemoved: true})
	if len(rows) != 1 || rows[0].ValueJSON != "" || rows[0].RemovedAt != base.Add(2*time.Hour) || rows[0].FirstSeenAt != base || rows[0].Source != srcA || rows[0].TenantID != acctA.TenantID {
		t.Fatalf("include removed: %+v", rows)
	}
	if rows, _, _ = s.Records(ctx, RecordFilter{Database: "Nope"}); len(rows) != 0 {
		t.Fatalf("unknown database: %+v", rows)
	}
}

func TestRecordsFaultsSurface(t *testing.T) {
	var total int
	ops := map[string]func(context.Context, *Store) error{
		"Stores": func(ctx context.Context, s *Store) error {
			_, err := s.Stores(ctx, &acctA)
			return err
		},
		"Records": func(ctx context.Context, s *Store) error {
			_, _, err := s.Records(ctx, RecordFilter{Database: "Teams", Limit: 1, Total: &total})
			return err
		},
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepReadFaults(t, seedRecords, op) })
	}
}

func TestRecordsWritesFaultsSurface(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		t.Helper()
		upsert(t, s, []teamsdesktop.GenericRecord{
			grec(&acctA, dbA, "events", `"e1"`, `1`),
			grec(&acctA, dbA, "events", `"e2"`, `2`),
			grec(&acctA, dbA, "events", `"e3"`, `3`),
		}, base)
		inSession(t, s, func(x *Session) {
			if _, err := x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"events": {`"e1"`: {}, `"e3"`: {}}}, base); err != nil {
				t.Fatal(err)
			}
		})
	}
	run := func(fn func(x *Session) error) func(context.Context, *Store) error {
		return func(ctx context.Context, s *Store) error {
			x, err := s.Begin(ctx)
			if err != nil {
				return err
			}
			defer x.Rollback()
			if err := fn(x); err != nil {
				return err
			}
			return x.Commit()
		}
	}
	ops := map[string]func(context.Context, *Store) error{
		"insert update revive": run(func(x *Session) error {
			_, err := x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{
				grec(&acctA, dbA, "events", `"e1"`, `9`), // update
				grec(&acctA, dbA, "events", `"e2"`, `2`), // revive (removed, same value)
				grec(&acctA, dbA, "events", `"e4"`, `4`), // insert
				grec(&acctA, dbA, "events", `"e3"`, `3`), // unchanged
			}, base)
			return err
		}),
		"mark records": run(func(x *Session) error {
			_, err := x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{}, base)
			return err
		}),
		"purge denied": run(func(x *Session) error {
			_, err := x.PurgeDenied(srcA, func(n string) bool { return n == "events" }, base)
			return err
		}),
		"mark databases": run(func(x *Session) error {
			_, err := x.MarkDatabasesRemoved(srcA, nil, &acctA, base)
			return err
		}),
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) { sweepFaults(t, seed, op, 1, 2, 3, 4) })
	}
}

func TestPrefixSuccessor(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"Teams:", "Teams;", true},
		{"a\xff", "b", true},
		{"a\xff\xff", "b", true},
		{"\xff", "", false},
	} {
		got, ok := prefixSuccessor(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("prefixSuccessor(%q) = %q, %v", c.in, got, ok)
		}
	}
}

// The prefix filter is a range, so it uses the (database, store) index and matches exactly the
// names that start with the prefix, including ones that sort next to it.
func TestRecordsPrefixIsARange(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, "ab", "s", `"1"`, `1`),
		grec(&acctA, "abc", "s", `"1"`, `2`),
		grec(&acctA, "ac", "s", `"1"`, `3`),
		grec(&acctA, "aa", "s", `"1"`, `4`),
		grec(&acctA, "\xff\xffz", "s", `"1"`, `5`),
	}, base)
	for prefix, want := range map[string]int{"ab": 2, "a": 4, "abc": 1, "b": 0, "\xff": 1, "": 5} {
		rows, _, err := s.Records(ctx, RecordFilter{Database: prefix})
		if err != nil || len(rows) != want {
			t.Errorf("prefix %q: %d rows (want %d) %v", prefix, len(rows), want, err)
		}
	}
	var plan string
	rows, err := s.db.Query(`explain query plan select 1 from records where database >= 'ab' and database < 'ac'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan += d
	}
	_ = rows.Close()
	if !strings.Contains(plan, "USING") {
		t.Fatalf("prefix range does not use an index: %s", plan)
	}
}

// PurgeDenied is the archive's one deletion of content: a row whose database or store now
// matches the denylist keeps its key but loses its value and hash, and is marked removed.
func TestPurgeDenied(t *testing.T) {
	s := newStore(t)
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", `"e1"`, `{"n":1}`),
		grec(&acctA, dbA, "events", `"e2"`, `{"n":2}`),
		grec(&acctA, dbA, "vault", `"v1"`, `{"token":"x"}`),
		grec(&acctA, "Teams:vault-manager:"+acctA.UserID, "any", `"k"`, `{"n":3}`),
	}, base)
	later := base.Add(time.Hour)
	var n int
	inSession(t, s, func(x *Session) {
		if _, err := x.MarkRecordsRemoved(srcA, dbA, map[string]map[string]struct{}{"vault": {}, "events": {`"e1"`: {}, `"e2"`: {}}}, base.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		var err error
		n, err = x.PurgeDenied(srcA, func(name string) bool { return name == "vault" || strings.HasPrefix(name, "Teams:vault") }, later)
		if err != nil {
			t.Fatal(err)
		}
	})
	if n != 2 {
		t.Fatalf("cleared %d rows, want 2", n)
	}
	v, removed, _ := recRow(t, s, dbA, "vault", `"v1"`)
	if v != "" || removed != fmtTime(base.Add(time.Minute)) {
		t.Fatalf("store purge: value %q removed %q (an existing removal time is kept)", v, removed)
	}
	if got := rowCount(t, s, `select count(*) from records where database like 'Teams:vault%' and value_json is null and content_hash='' and removed_at is not null`); got != 1 {
		t.Fatalf("database purge: %d rows", got)
	}
	if v, removed, _ := recRow(t, s, dbA, "events", `"e1"`); v != `{"n":1}` || removed != "" {
		t.Fatalf("a row that is not denied changed: %q %q", v, removed)
	}
	// Idempotent: nothing left to clear.
	inSession(t, s, func(x *Session) {
		if n, err := x.PurgeDenied(srcA, func(string) bool { return true }, later); err != nil || n != 2 {
			// events rows are now denied too, so they are cleared; the already cleared rows are not.
			t.Fatalf("second purge: %d %v", n, err)
		}
		if n, err := x.PurgeDenied(srcA, func(string) bool { return true }, later); err != nil || n != 0 {
			t.Fatalf("third purge: %d %v", n, err)
		}
	})
	// Another source is untouched.
	inSession(t, s, func(x *Session) {
		if n, err := x.PurgeDenied("other-source", func(string) bool { return true }, later); err != nil || n != 0 {
			t.Fatalf("other source: %d %v", n, err)
		}
	})
}

func TestUpsertRecordsRescrubbedValueReplacesStored(t *testing.T) {
	s := newStore(t)
	old := `{"access_token":{"a":"secret"}}`
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, old)}, base)
	scrubbed, n := teamsdesktop.Scrub([]byte(old))
	if n != 1 {
		t.Fatalf("scrub n = %d", n)
	}
	if c := upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e1"`, string(scrubbed))}, base.Add(time.Hour)); c != (Counts{Seen: 1, Updated: 1}) {
		t.Fatalf("counts: %+v", c)
	}
	if v, _, _ := recRow(t, s, dbA, "events", `"e1"`); v != `{"access_token":"[redacted]"}` {
		t.Fatalf("stored value still unredacted: %s", v)
	}
}

// A row archived under a key that scrubs differently loses its value and hash and is marked
// removed; rows whose keys scrub to themselves are untouched.
func TestPurgeUnscrubbedKeys(t *testing.T) {
	s := newStore(t)
	const bad = `"Bearer abc"`
	upsert(t, s, []teamsdesktop.GenericRecord{
		grec(&acctA, dbA, "events", bad, `{"n":1}`),
		grec(&acctA, dbA, "events", `"fine"`, `{"n":2}`),
		grec(&acctA, dbA, "events", `"https://x/y?sig=[redacted]"`, `{"n":4}`),
		grec(&acctA, dbA, "events", `{"password":"[redacted]"}`, `{"n":5}`),
		grec(&acctA, dbA, "events", `"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.c2ln"`, `{"n":3}`),
	}, base)
	later := base.Add(time.Hour)
	var n int
	inSession(t, s, func(x *Session) {
		var err error
		if n, err = x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, later); err != nil {
			t.Fatal(err)
		}
		if again, err := x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, later); err != nil || again != 0 {
			t.Fatalf("second pass: %d %v", again, err)
		}
		if other, err := x.PurgeUnscrubbedKeys("other-source", teamsdesktop.Scrub, later); err != nil || other != 0 {
			t.Fatalf("other source: %d %v", other, err)
		}
	})
	if n != 2 {
		t.Fatalf("cleared %d rows, want 2", n)
	}
	if v, removed, _ := recRow(t, s, dbA, "events", bad); v != "" || removed != fmtTime(later) {
		t.Fatalf("bad key row: %q %q", v, removed)
	}
	if v, removed, _ := recRow(t, s, dbA, "events", `"fine"`); v != `{"n":2}` || removed != "" {
		t.Fatalf("fine row changed: %q %q", v, removed)
	}
	for _, k := range []string{`"https://x/y?sig=[redacted]"`, `{"password":"[redacted]"}`} {
		if v, removed, _ := recRow(t, s, dbA, "events", k); v == "" || removed != "" {
			t.Fatalf("an already scrubbed key %s was purged: %q %q", k, v, removed)
		}
	}
	if got := rowCount(t, s, `select count(*) from records where content_hash=''`); got != 2 {
		t.Fatalf("%d rows with cleared hash", got)
	}
}

func TestPurgeUnscrubbedKeysFaultsSurface(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		t.Helper()
		upsert(t, s, []teamsdesktop.GenericRecord{
			grec(&acctA, dbA, "events", `"Bearer x"`, `1`),
			grec(&acctA, dbA, "events", `"Bearer y"`, `2`),
		}, base)
	}
	op := func(ctx context.Context, s *Store) error {
		x, err := s.Begin(ctx)
		if err != nil {
			return err
		}
		defer x.Rollback()
		if _, err := x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, base); err != nil {
			return err
		}
		return x.Commit()
	}
	sweepFaults(t, seed, op)
}
