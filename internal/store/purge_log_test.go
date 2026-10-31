package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// archiveBytes is the archive file and its write-ahead log, read raw from disk.
func archiveBytes(t *testing.T, path string) (db, wal []byte) {
	t.Helper()
	db, err := os.ReadFile(path) //nolint:gosec // test temp dir
	if err != nil {
		t.Fatal(err)
	}
	wal, err = os.ReadFile(path + "-wal") //nolint:gosec // test temp dir
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return db, wal
}

func openAt(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "m365crawl.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func pendingPurgedLog(t *testing.T, s *Store) bool {
	t.Helper()
	return rowCount(t, s, `select count(*) from meta where key=?`, purgedLogKey) == 1
}

// A purged credential value is gone from the bytes on disk, not only from the row: the purge
// overwrites the space it frees, and clearing the log afterwards leaves no older page image of it
// in the write-ahead log. Both purges are covered, with a value small enough to sit inside its
// page and one large enough to spill into overflow pages. Without secure_delete the in-page and
// overflow values are still on disk after the checkpoint.
func TestPurgeLeavesNoCopyOnDisk(t *testing.T) {
	const marker = "SYNTHETICPURGEDVALUE"
	big := strings.Repeat(marker+"-", 2000) // about 40 KB: several overflow pages
	purges := map[string]func(x *Session, at time.Time) (int, error){
		"denied": func(x *Session, at time.Time) (int, error) {
			return x.PurgeDenied(srcA, func(n string) bool { return n == "vault" }, at)
		},
		"unscrubbed key": func(x *Session, at time.Time) (int, error) {
			return x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, at)
		},
	}
	for name, purge := range purges {
		for size, value := range map[string]string{"small": marker, "in-page": strings.Repeat(marker, 30), "overflow": big} {
			t.Run(name+"/"+size, func(t *testing.T) {
				s, path := openAt(t)
				st, key := "vault", `"v1"`
				if name == "unscrubbed key" {
					st, key = "events", `{"access_token":"x"}`
				}
				// Ordinary rows on both sides of the purged one share its page.
				upsert(t, s, []teamsdesktop.GenericRecord{
					grec(&acctA, dbA, "events", `"a-kept"`, `{"v":"ordinary"}`),
					grec(&acctA, dbA, st, key, `{"v":"`+value+`"}`),
					grec(&acctA, dbA, "events", `"kept"`, `{"v":"ordinary"}`),
					grec(&acctA, dbA, "zzz", `"z-kept"`, `{"v":"ordinary"}`),
				}, base)
				// The value is on disk to start with, so its absence below means something.
				db, wal := archiveBytes(t, path)
				if !bytes.Contains(db, []byte(marker)) && !bytes.Contains(wal, []byte(marker)) {
					t.Fatal("the synthetic value is not on disk before the purge")
				}
				inSession(t, s, func(x *Session) {
					if n, err := purge(x, base.Add(time.Hour)); err != nil || n != 1 {
						t.Fatalf("purge: %d %v", n, err)
					}
				})
				if !pendingPurgedLog(t, s) {
					t.Fatal("the purge did not ask for the log to be cleared")
				}
				res, err := s.ClearPurgedLog(context.Background())
				if err != nil || res.Pending {
					t.Fatalf("ClearPurgedLog: %+v %v", res, err)
				}
				if pendingPurgedLog(t, s) {
					t.Fatal("the request to clear the log is still there")
				}
				db, wal = archiveBytes(t, path)
				if i := bytes.Index(db, []byte(marker)); i >= 0 {
					t.Fatalf("purged value still in the database file at byte %d", i)
				}
				if i := bytes.Index(wal, []byte(marker)); i >= 0 {
					t.Fatalf("purged value still in the write-ahead log at byte %d", i)
				}
				if v, _, _ := recRow(t, s, dbA, "events", `"kept"`); v != `{"v":"ordinary"}` {
					t.Fatalf("an ordinary row changed: %q", v)
				}
				// The purge's connection returns to its earlier setting.
				var secure int
				if err := s.db.QueryRowContext(context.Background(), `pragma secure_delete`).Scan(&secure); err != nil || secure != 0 {
					t.Fatalf("secure_delete left at %d (%v)", secure, err)
				}
			})
		}
	}
}

// A reader that keeps the checkpoint from truncating the log leaves the request in place and is
// reported, never read as cleared; the next call, with the reader gone, clears it.
func TestClearPurgedLogReportsAReaderThatBlocksIt(t *testing.T) {
	const marker = "SYNTHETICBLOCKEDVALUE"
	defer func(a int, w, p time.Duration) { purgedLogAttempts, purgedLogWait, purgedLogPause = a, w, p }(purgedLogAttempts, purgedLogWait, purgedLogPause)
	purgedLogAttempts, purgedLogWait, purgedLogPause = 2, 50*time.Millisecond, time.Millisecond

	s, path := openAt(t)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "vault", `"v1"`, `{"v":"`+marker+`"}`)}, base)
	reader, err := sql.Open("sqlite", sqliteFileURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	rtx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := rtx.QueryRow(`select count(*) from records`).Scan(&n); err != nil { // holds a snapshot
		t.Fatal(err)
	}
	inSession(t, s, func(x *Session) {
		if n, err := x.PurgeDenied(srcA, func(n string) bool { return n == "vault" }, base.Add(time.Hour)); err != nil || n != 1 {
			t.Fatalf("purge: %d %v", n, err)
		}
	})
	res, err := s.ClearPurgedLog(context.Background())
	if err != nil || !res.Pending || res.Attempts != 2 {
		t.Fatalf("blocked: %+v %v", res, err)
	}
	if !pendingPurgedLog(t, s) {
		t.Fatal("a blocked checkpoint dropped the request")
	}
	_ = rtx.Rollback()
	if res, err = s.ClearPurgedLog(context.Background()); err != nil || res.Pending {
		t.Fatalf("after the reader left: %+v %v", res, err)
	}
	db, wal := archiveBytes(t, path)
	if bytes.Contains(db, []byte(marker)) || bytes.Contains(wal, []byte(marker)) {
		t.Fatal("purged value still on disk")
	}
	// The connection's wait for locks is back to what it was.
	var wait int
	if err := s.db.QueryRowContext(context.Background(), `pragma busy_timeout`).Scan(&wait); err != nil || wait != 5000 {
		t.Fatalf("busy_timeout %d (%v)", wait, err)
	}
}

// With nothing purged there is nothing to clear, and no checkpoint is taken.
func TestClearPurgedLogWithoutAPurge(t *testing.T) {
	s, _ := openAt(t)
	if res, err := s.ClearPurgedLog(context.Background()); err != nil || res != (PurgedLog{}) {
		t.Fatalf("%+v %v", res, err)
	}
}

// purgeOne archives a synthetic credential row and purges it, so the archive asks for its log to
// be cleared.
func purgeOne(t *testing.T, s *Store) {
	t.Helper()
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "vault", `"v1"`, `{"v":"SYNTHETIC"}`)}, base)
	inSession(t, s, func(x *Session) {
		if n, err := x.PurgeDenied(srcA, func(n string) bool { return n == "vault" }, base.Add(time.Hour)); err != nil || n != 1 {
			t.Fatalf("purge: %d %v", n, err)
		}
	})
}

// Every failure inside ClearPurgedLog is returned, and none of them drops the request before the
// log was truncated.
func TestClearPurgedLogFaultsSurface(t *testing.T) {
	for _, kind := range faultKinds {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t)
			purgeOne(t, s)
			inj := injectFaults(t, s)
			op := func(ctx context.Context, s *Store) error {
				// A faulted call may have cleared the request already; ask again so each call
				// walks the whole path.
				if _, err := s.db.ExecContext(ctx, `insert or ignore into meta(key,value) values(?,'1')`, purgedLogKey); err != nil {
					return err
				}
				res, err := s.ClearPurgedLog(ctx)
				if err == nil && res.Pending {
					return errors.New("nothing blocks the checkpoint, yet it is pending")
				}
				// A failure before the log was truncated leaves the request, and says so.
				if err != nil && !res.Pending && pendingPurgedLog(t, s) {
					t.Errorf("%s: an error left the request but reported nothing pending: %v", kind, err)
				}
				return err
			}
			sweepKind(t, s, inj, kind, false, op, nil)
		})
	}
}

// A cancelled run stops waiting for readers and keeps the request.
func TestClearPurgedLogStopsWhenCancelled(t *testing.T) {
	defer func(a int, w, p time.Duration) { purgedLogAttempts, purgedLogWait, purgedLogPause = a, w, p }(purgedLogAttempts, purgedLogWait, purgedLogPause)
	purgedLogAttempts, purgedLogWait, purgedLogPause = 100, 10*time.Millisecond, time.Hour

	s, path := openAt(t)
	purgeOne(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res, err := s.ClearPurgedLog(ctx); !errors.Is(err, context.Canceled) || !res.Pending {
		t.Fatalf("cancelled before starting: %+v %v", res, err)
	}

	reader, err := sql.Open("sqlite", sqliteFileURI(path, "mode=ro"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	rtx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rtx.Rollback() }()
	var n int
	if err := rtx.QueryRow(`select count(*) from records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	// A write after the reader's snapshot, so the checkpoint has frames the reader still needs.
	inSession(t, s, func(x *Session) {
		if _, err := x.UpsertRecords(srcA, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e"`, `1`)}, base); err != nil {
			t.Fatal(err)
		}
	})
	ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	res, err := s.ClearPurgedLog(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !res.Pending || res.Attempts != 1 {
		t.Fatalf("cancelled while waiting: %+v %v", res, err)
	}
	if !pendingPurgedLog(t, s) {
		t.Fatal("a cancelled clear dropped the request")
	}
}
