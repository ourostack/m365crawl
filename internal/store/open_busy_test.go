package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Another connection holding the archive's write lock makes a writable open wait for it (the
// connection's 5-second busy timeout), not fail at once with "database is locked" (#301). The
// schema batch must take the write lock when it begins: a transaction that reads before it writes
// cannot wait for the lock, because SQLite does not call the busy handler for that upgrade.
func TestOpenWaitsForAnotherWriter(t *testing.T) {
	ctx := context.Background()
	path := writableArchivePath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	must0(s.Close())

	other, err := sql.Open("sqlite", sqliteFileURI(path, "_pragma=busy_timeout(5000)"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `insert into meta(key, value) values('test-301', 'held')`); err != nil {
		t.Fatal(err)
	}
	const hold = 300 * time.Millisecond
	released := make(chan time.Time, 1)
	go func() {
		time.Sleep(hold)
		at := time.Now() // before the commit, so a waiting open cannot finish before it
		_, _ = conn.ExecContext(ctx, `commit`)
		released <- at
	}()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("open while another connection held the write lock: %v", err)
	}
	opened := time.Now()
	defer func() { _ = s.Close() }()
	if at := <-released; opened.Before(at) {
		t.Fatalf("the open did not wait for the other writer (opened %v before the release at %v)", opened, at)
	}
	if n := rowCount(t, s, `select count(*) from meta where key='test-301'`); n != 1 {
		t.Fatalf("the other writer's row: %d", n)
	}
}
