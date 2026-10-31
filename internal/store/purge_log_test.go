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

// diskBytes is the archive file and its write-ahead log, read raw from disk.
func diskBytes(t *testing.T, path string) (db, wal []byte) {
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
				db, wal := diskBytes(t, path)
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
				db, wal = diskBytes(t, path)
				if i := bytes.Index(db, []byte(marker)); i >= 0 {
					t.Fatalf("purged value still in the database file at byte %d", i)
				}
				if i := bytes.Index(wal, []byte(marker)); i >= 0 {
					t.Fatalf("purged value still in the write-ahead log at byte %d", i)
				}
				if v, _, _ := recRow(t, s, dbA, "events", `"kept"`); v != `{"v":"ordinary"}` {
					t.Fatalf("an ordinary row changed: %q", v)
				}
				// The purge's connection keeps the archive's own setting: on.
				var secure int
				if err := s.db.QueryRowContext(context.Background(), `pragma secure_delete`).Scan(&secure); err != nil || secure != 1 {
					t.Fatalf("secure_delete left at %d (%v)", secure, err)
				}
			})
		}
	}
}

// A credential in a record's key is gone from the bytes on disk after the purge, from the table and
// from the primary-key index, whether the row takes the scrubbed key or is deleted because another
// row holds it.
func TestPurgedKeyLeavesNoCopyOnDisk(t *testing.T) {
	const marker = "SYNTHETICKEYCREDENTIAL"
	key := `{"access_token":"` + marker + `"}`
	for name, held := range map[string]bool{"rekeyed": false, "deleted": true} {
		t.Run(name, func(t *testing.T) {
			s, path := openAt(t)
			recs := []teamsdesktop.GenericRecord{
				grec(&acctA, dbA, "events", `"a-kept"`, `{"v":"ordinary"}`),
				grec(&acctA, dbA, "events", key, `{"v":"value"}`),
				grec(&acctA, dbA, "events", `"kept"`, `{"v":"ordinary"}`),
			}
			if held {
				recs = append(recs, grec(&acctA, dbA, "events", `{"access_token":"[redacted]"}`, `{"v":"current"}`))
			}
			upsert(t, s, recs, base)
			if db, wal := diskBytes(t, path); !bytes.Contains(db, []byte(marker)) && !bytes.Contains(wal, []byte(marker)) {
				t.Fatal("the synthetic key is not on disk before the purge")
			}
			inSession(t, s, func(x *Session) {
				if n, err := x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, base.Add(time.Hour)); err != nil || n != 1 {
					t.Fatalf("purge: %d %v", n, err)
				}
			})
			if res, err := s.ClearPurgedLog(context.Background()); err != nil || res.Pending {
				t.Fatalf("ClearPurgedLog: %+v %v", res, err)
			}
			db, wal := diskBytes(t, path)
			if i := bytes.Index(db, []byte(marker)); i >= 0 {
				t.Fatalf("purged key still in the database file at byte %d", i)
			}
			if bytes.Contains(wal, []byte(marker)) {
				t.Fatal("purged key still in the write-ahead log")
			}
			// Rekeyed, the row stays; deleted, the row under the scrubbed key stays unchanged.
			if got := rowCount(t, s, `select count(*) from records`); got != 3 {
				t.Fatalf("%d rows, want 3", got)
			}
			if v, _, _ := recRow(t, s, dbA, "events", `{"access_token":"[redacted]"}`); held && v != `{"v":"current"}` {
				t.Fatalf("the row under the scrubbed key changed: %q", v)
			}
		})
	}
}

// Ordinary writes overwrite what they replace: a value a sync updates is not left in free space.
func TestWritableArchiveOverwritesReplacedValues(t *testing.T) {
	const marker = "SYNTHETICREPLACEDVALUE"
	s, path := openAt(t)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e"`, `{"v":"`+strings.Repeat(marker, 2000)+`"}`)}, base)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e"`, `{"v":"new"}`)}, base.Add(time.Hour))
	if _, err := s.db.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	db, wal := diskBytes(t, path)
	if bytes.Contains(db, []byte(marker)) || bytes.Contains(wal, []byte(marker)) {
		t.Fatal("a replaced value is still on disk")
	}
	var secure int
	if err := s.db.QueryRow(`pragma secure_delete`).Scan(&secure); err != nil || secure != 1 {
		t.Fatalf("writable archive secure_delete=%d (%v)", secure, err)
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
	db, wal := diskBytes(t, path)
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

// seedLoop registers a Loop record key directly, with an attempt, a source, a snapshot, a loss
// and one channel in the text index, as an admitted Loop acquisition leaves them.
func seedLoop(t *testing.T, s *Store, id int64, key string) {
	t.Helper()
	locator, ok := loopLocator(LoopTuple{Source: srcA, Database: LoopDatabase, Store: LoopStore, KeyJSON: key})
	if !ok {
		t.Fatal("seed tuple outside Loop's limits")
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`insert into knowledge_locators(id,kind,platform,locator) values(?,?,?,?)`, []any{id, LoopKind, LoopPlatform, locator}},
		{`insert into loop_locator_metadata(locator_id,source,database,store,key_json) values(?,?,?,?,?)`, []any{id, srcA, LoopDatabase, LoopStore, key}},
		{`insert into knowledge_attempts(id,locator_id,started_at) values(?,?,'2026-01-01T00:00:00.000Z')`, []any{id, id}},
		{`insert into loop_attempt_evidence(attempt_id,locator_id,tenant_id,user_id) values(?,?,'t','u')`, []any{id, id}},
		{`insert into knowledge_sources(id,locator_id,boundary,qualification) values(?,?,'b','q')`, []any{id, id}},
		{`insert into loop_snapshots(id,locator_id,knowledge_source_id,value_digest,qualification,file_id,sequence,latest_sequence,pending_operations,missing_blobs,partial,channel_count,tenant_id,user_id,record_updated_at_at_admission,projected_at,first_attempt)
values(?,?,?,'d','q','f',1,1,0,0,0,1,'t','u','x','x',?)`, []any{id, id, id, id}},
		{`insert into loop_snapshot_losses(snapshot_id,code,count) values(?,'c',1)`, []any{id}},
		{`insert into loop_channels(id,snapshot_id,ordinal,path,sequence,text,markers,pending_operations) values(?,?,0,'p',1,'channel text',0,0)`, []any{id, id}},
		{`insert into loop_fts(rowid,path,text) values(?,'p','channel text')`, []any{id}},
	} {
		if _, err := s.db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
}

// loopOwned counts the rows a Loop registration owns, over every table that holds them.
func loopOwned(t *testing.T, s *Store, id int64) int {
	t.Helper()
	return rowCount(t, s, `select (select count(*) from knowledge_locators where id=?1)+(select count(*) from loop_locator_metadata where locator_id=?1)
+(select count(*) from knowledge_attempts where locator_id=?1)+(select count(*) from loop_attempt_evidence where locator_id=?1)
+(select count(*) from knowledge_sources where locator_id=?1)+(select count(*) from loop_snapshots where locator_id=?1)
+(select count(*) from loop_snapshot_losses where snapshot_id=?1)+(select count(*) from loop_channels where snapshot_id=?1)
+(select count(*) from loop_fts where rowid=?1)`, id)
}

// A Loop registration keeps its record key as its identity, in loop_locator_metadata and in its
// knowledge locator. A key that held a credential follows the record-key rule there too: the
// registration takes the scrubbed key, or is deleted with everything it owns when another
// registration holds the scrubbed key or the scrubbed tuple is outside Loop's limits. Either way
// the credential is gone from the bytes on disk.
func TestPurgedLoopKeyLeavesNoCopyOnDisk(t *testing.T) {
	const marker = "SYNTHETICLOOPCREDENTIAL"
	key := `{"access_token":"` + marker + `"}`
	scrubbed := `{"access_token":"[redacted]"}`
	for _, c := range []string{"rekeyed", "held", "too long"} {
		t.Run(c, func(t *testing.T) {
			s, path := openAt(t)
			seedLoop(t, s, 7, key)
			if c == "held" {
				seedLoop(t, s, 8, scrubbed)
			}
			if c == "too long" {
				limit := loopLocatorLimit
				t.Cleanup(func() { loopLocatorLimit = limit })
				loc, _ := loopLocator(LoopTuple{Source: srcA, Database: LoopDatabase, Store: LoopStore, KeyJSON: scrubbed})
				loopLocatorLimit = len(loc) - 1 // a later, lower limit: the scrubbed tuple no longer fits
			}
			inSession(t, s, func(x *Session) {
				if n, err := x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, base); err != nil || n != 1 {
					t.Fatalf("purge: %d %v", n, err)
				}
				if n, err := x.PurgeUnscrubbedKeys(srcA, teamsdesktop.Scrub, base); err != nil || n != 0 {
					t.Fatalf("second pass: %d %v", n, err)
				}
			})
			if res, err := s.ClearPurgedLog(context.Background()); err != nil || res.Pending {
				t.Fatalf("ClearPurgedLog: %+v %v", res, err)
			}
			db, wal := diskBytes(t, path)
			if i := bytes.Index(db, []byte(marker)); i >= 0 {
				t.Fatalf("Loop key still in the database file at byte %d", i)
			}
			if bytes.Contains(wal, []byte(marker)) {
				t.Fatal("Loop key still in the write-ahead log")
			}
			if c == "rekeyed" {
				want, _ := loopLocator(LoopTuple{Source: srcA, Database: LoopDatabase, Store: LoopStore, KeyJSON: scrubbed})
				if n := rowCount(t, s, `select count(*) from loop_locator_metadata m join knowledge_locators l on l.id=m.locator_id where m.locator_id=7 and m.key_json=? and l.locator=?`, scrubbed, want); n != 1 {
					t.Fatal("the registration did not take the scrubbed key")
				}
				if n := loopOwned(t, s, 7); n != 9 {
					t.Fatalf("a rekeyed registration lost rows: %d", n)
				}
				return
			}
			if n := loopOwned(t, s, 7); n != 0 {
				t.Fatalf("%d rows of the deleted registration remain", n)
			}
			if c == "held" && loopOwned(t, s, 8) != 9 {
				t.Fatal("the registration under the scrubbed key changed")
			}
		})
	}
}

func TestPurgeLoopKeysFaultsSurface(t *testing.T) {
	seed := func(t *testing.T, s *Store) {
		t.Helper()
		seedLoop(t, s, 7, `{"access_token":"SYNTHETIC"}`)
		seedLoop(t, s, 8, `{"access_token":"[redacted]"}`) // so 7 is deleted
		seedLoop(t, s, 9, `{"password":"SYNTHETIC"}`)      // and 9 takes its scrubbed key
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
