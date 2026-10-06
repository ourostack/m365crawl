package calendar

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

var errNth = errors.New("injected failure")

// nthFail is a database over the same file as db whose at-th statement preparation fails.
type nthFail struct {
	dsn   string
	at    int
	seen  int
	fired bool
}

func (f *nthFail) Connect(context.Context) (driver.Conn, error) {
	inner, err := sql.Open("sqlite", f.dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = inner.Close() }()
	c, err := inner.Driver().Open(f.dsn)
	if err != nil {
		return nil, err
	}
	return &nthConn{Conn: c, f: f}, nil
}

func (f *nthFail) Driver() driver.Driver { return nil }

type nthConn struct {
	driver.Conn
	f *nthFail
}

func (c *nthConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}

func (c *nthConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if c.f.seen++; c.f.seen == c.f.at {
		c.f.fired = true
		return nil, errNth
	}
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}

// eachStatementFailure runs op against a copy of db's file with each statement failed in turn, and
// returns when an unfaulted run completes. Every faulted run must report an error.
func eachStatementFailure(t *testing.T, db *sql.DB, op func(*sql.DB) error) {
	t.Helper()
	var path string
	if err := db.QueryRow(`select file from pragma_database_list where name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	for at := 1; ; at++ {
		if at > 200 {
			t.Fatal("the sweep did not end")
		}
		f := &nthFail{dsn: "file:" + path + "?mode=ro", at: at}
		d := sql.OpenDB(f)
		d.SetMaxOpenConns(1)
		err := op(d)
		_ = d.Close()
		if !f.fired {
			if err != nil {
				t.Fatalf("unfaulted run failed: %v", err)
			}
			return
		}
		if !errors.Is(err, errNth) {
			t.Fatalf("statement %d: err = %v", at, err)
		}
	}
}
