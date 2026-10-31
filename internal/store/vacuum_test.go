package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

func vacuumState(t *testing.T, s *Store) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`select coalesce((select value from meta where key=?),'')`, vacuumKey).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func stubFreeSpace(t *testing.T, avail uint64, measured bool, err error) {
	t.Helper()
	old := freeSpace
	t.Cleanup(func() { freeSpace = old })
	freeSpace = func(string) (uint64, bool, error) { return avail, measured, err }
}

// leaveOldCopy writes a value and replaces it with secure_delete off, as an older version did, so
// the old value stays in free space; it then marks the archive as written by an older version.
func leaveOldCopy(t *testing.T, s *Store, path, marker string) {
	t.Helper()
	if _, err := s.db.Exec(`pragma secure_delete=off`); err != nil {
		t.Fatal(err)
	}
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e"`, `{"v":"`+strings.Repeat(marker, 300)+`"}`)}, base)
	upsert(t, s, []teamsdesktop.GenericRecord{grec(&acctA, dbA, "events", `"e"`, `{"v":"new"}`)}, base.Add(time.Hour))
	if _, err := s.db.Exec(`pragma secure_delete=on`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`update meta set value='needed' where key=?`, vacuumKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	if db, _ := archiveBytes(t, path); !bytes.Contains(db, []byte(marker)) {
		t.Fatal("the old copy is not in free space to start with")
	}
}

// An archive this version creates needs no VACUUM; one an older version wrote does, and that is
// recorded the first time this version opens it for writing.
func TestOpenRecordsWhetherTheArchiveNeedsItsVacuum(t *testing.T) {
	s, path := openAt(t)
	if got := vacuumState(t, s); got != "done" {
		t.Fatalf("new archive: %q", got)
	}
	if v, err := s.VacuumState(context.Background()); err != nil || v != nil {
		t.Fatalf("new archive state: %+v %v", v, err)
	}
	// An older version never wrote the record.
	if _, err := s.db.Exec(`delete from meta where key=?`, vacuumKey); err != nil {
		t.Fatal(err)
	}
	stubFreeSpace(t, 1<<40, true, nil)
	if v, err := s.VacuumState(context.Background()); err != nil || v == nil || v.Reason != VacuumNotRun || v.NeedBytes == 0 || *v.FreeBytes != 1<<40 {
		t.Fatalf("older archive state: %+v %v", v, err)
	}
	_ = s.Close()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := vacuumState(t, s); got != "needed" {
		t.Fatalf("older archive after open: %q", got)
	}
	// Opening again keeps the record.
	_ = s.Close()
	if s, err = Open(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got := vacuumState(t, s); got != "needed" {
		t.Fatalf("after a second open: %q", got)
	}
}

// The one-time VACUUM rewrites an older archive, so what the older version left in free space is
// gone from the file, and then it is never run again.
func TestVacuumOnceRewritesAnOlderArchive(t *testing.T) {
	const marker = "SYNTHETICOLDFREESPACE"
	s, path := openAt(t)
	leaveOldCopy(t, s, path, marker)
	ctx := context.Background()
	pending, vacuumed, err := s.VacuumOnce(ctx)
	if err != nil || pending != nil || !vacuumed {
		t.Fatalf("VacuumOnce: %+v %v %v", pending, vacuumed, err)
	}
	if got := vacuumState(t, s); got != "done" {
		t.Fatalf("state after: %q", got)
	}
	if res, err := s.ClearPurgedLog(ctx); err != nil || res.Pending || res.Attempts != 1 {
		t.Fatalf("the VACUUM did not ask for the log to be cleared: %+v %v", res, err)
	}
	db, wal := archiveBytes(t, path)
	if bytes.Contains(db, []byte(marker)) || bytes.Contains(wal, []byte(marker)) {
		t.Fatal("the old copy survived the VACUUM")
	}
	if v, _, _ := recRow(t, s, dbA, "events", `"e"`); v != `{"v":"new"}` {
		t.Fatalf("content changed: %q", v)
	}
	// The connection's temporary storage is back in memory, and the directory is unset.
	var tempStore int
	if err := s.db.QueryRow(`pragma temp_store`).Scan(&tempStore); err != nil || tempStore != 2 {
		t.Fatalf("temp_store %d (%v)", tempStore, err)
	}
	var dir sql.NullString
	if err := s.db.QueryRow(`pragma temp_store_directory`).Scan(&dir); err != nil && !errors.Is(err, sql.ErrNoRows) || dir.String != "" {
		t.Fatalf("temp_store_directory %q (%v)", dir.String, err)
	}
	if pending, vacuumed, err := s.VacuumOnce(ctx); err != nil || pending != nil || vacuumed {
		t.Fatalf("second call: %+v %v %v", pending, vacuumed, err)
	}
}

// Without twice the archive's size free on its volume, or without a measurement, nothing runs and
// the space needed is reported; the VACUUM stays due.
func TestVacuumOnceWaitsForSpace(t *testing.T) {
	s, path := openAt(t)
	leaveOldCopy(t, s, path, "SYNTHETICWAITING")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	need := 2 * uint64(info.Size()) //nolint:gosec // G115: a file size is non-negative
	stubFreeSpace(t, need-1, true, nil)
	pending, vacuumed, err := s.VacuumOnce(context.Background())
	if err != nil || vacuumed || pending == nil || pending.Reason != VacuumDiskSpace || pending.NeedBytes != need || *pending.FreeBytes != need-1 {
		t.Fatalf("low space: %+v %v %v", pending, vacuumed, err)
	}
	if v, err := s.VacuumState(context.Background()); err != nil || v == nil || v.Reason != VacuumDiskSpace {
		t.Fatalf("doctor's view: %+v %v", v, err)
	}
	stubFreeSpace(t, 0, false, nil)
	if pending, _, err := s.VacuumOnce(context.Background()); err != nil || pending == nil || pending.Reason != VacuumUnmeasured || pending.FreeBytes != nil {
		t.Fatalf("unmeasured: %+v %v", pending, err)
	}
	stubFreeSpace(t, 0, false, errors.New("statfs failed"))
	if pending, _, err := s.VacuumOnce(context.Background()); err != nil || pending == nil || pending.Reason != VacuumUnmeasured || pending.Error != "statfs failed" {
		t.Fatalf("measurement error: %+v %v", pending, err)
	}
	if got := vacuumState(t, s); got != "needed" {
		t.Fatalf("state: %q", got)
	}
	// An archive file that cannot be read for its size is an error.
	if _, err := measureVacuum(path+"-missing", ""); err == nil {
		t.Fatal("a missing archive file measured")
	}
}

// A cancelled run stops the VACUUM, which changes nothing, and reports it.
func TestVacuumOnceCancelled(t *testing.T) {
	s, path := openAt(t)
	leaveOldCopy(t, s, path, "SYNTHETICCANCELLED")
	ctx, cancel := context.WithCancel(context.Background())
	stubFreeSpace(t, 1<<40, true, nil)
	old := freeSpace
	freeSpace = func(dir string) (uint64, bool, error) { cancel(); return old(dir) }
	pending, vacuumed, err := s.VacuumOnce(ctx)
	if !errors.Is(err, context.Canceled) || vacuumed || pending == nil || pending.Reason != VacuumCancelled {
		t.Fatalf("cancelled: %+v %v %v", pending, vacuumed, err)
	}
	if got := vacuumState(t, s); got != "needed" {
		t.Fatalf("state: %q", got)
	}
	if _, _, err := s.VacuumOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled before starting: %v", err)
	}
}

// Every failure inside VacuumOnce is returned or reported, never swallowed.
func TestVacuumOnceFaultsSurface(t *testing.T) {
	for _, kind := range faultKinds {
		t.Run(kind, func(t *testing.T) {
			s, path := openAt(t)
			leaveOldCopy(t, s, path, "SYNTHETICFAULTS")
			inj := injectFaults(t, s)
			op := func(ctx context.Context, s *Store) error {
				if _, err := s.db.ExecContext(ctx, `update meta set value='needed' where key=?`, vacuumKey); err != nil {
					return err
				}
				pending, vacuumed, err := s.VacuumOnce(ctx)
				if err == nil && pending != nil {
					if pending.Reason == VacuumFailed && strings.Contains(pending.Error, errInjected.Error()) {
						return fmt.Errorf("%w: %s", errInjected, pending.Error)
					}
					return fmt.Errorf("unexpected pending %+v", pending)
				}
				if err == nil && !vacuumed {
					return errors.New("nothing ran and nothing is pending")
				}
				return err
			}
			sweepKind(t, s, inj, kind, false, op, nil)
		})
	}
}

func TestQuoteSQL(t *testing.T) {
	if got := quoteSQL(`/a'b`); got != `'/a''b'` {
		t.Fatalf("%s", got)
	}
}
