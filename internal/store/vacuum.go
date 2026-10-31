package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ourostack/m365crawl/internal/diskspace"
)

// vacuumKey in meta says whether the archive still needs its one-time VACUUM: 'needed' on an
// archive an older version wrote, which can hold content it replaced or cleared in free space
// (secure_delete was not always on then), and 'done' once that VACUUM ran or on an archive this
// version created. Open records it the first time this version opens the archive for writing.
const vacuumKey = "free_space_vacuum"

// Why a VACUUM is still pending.
const (
	VacuumDiskSpace  = "disk_space" // less free space on the archive's volume than NeedBytes
	VacuumUnmeasured = "unmeasured" // the free space could not be measured
	VacuumFailed     = "failed"     // SQLite refused or failed the VACUUM; nothing changed
	VacuumCancelled  = "cancelled"  // the run was cancelled; nothing changed
	VacuumNotRun     = "not_run"    // only an explicit sync runs it (doctor)
	// VacuumLogPending: the VACUUM finished, but readers kept its write-ahead log (about the
	// archive's size) from being emptied; the next sync empties it (purged_log_pending).
	VacuumLogPending = "log_pending"
)

// VacuumPending says the archive still needs its one-time VACUUM, how much free space on the
// archive's volume it needs (twice the archive: SQLite's temporary copy and the write-ahead log
// that brings the copy back), how much there was when measured, and why it did not run.
type VacuumPending struct {
	NeedBytes uint64  `json:"need_bytes"`
	FreeBytes *uint64 `json:"free_bytes,omitempty"`
	Reason    string  `json:"reason"`
	Error     string  `json:"error,omitempty"`
}

// freeSpace is diskspace.Free; tests replace it.
var freeSpace = diskspace.Free

// VacuumState is what doctor reports: nil when the archive needs no VACUUM, else the space it
// needs and has now, with Reason VacuumNotRun (or VacuumUnmeasured).
func (s *Store) VacuumState(ctx context.Context) (*VacuumPending, error) {
	var state string
	err := s.db.QueryRowContext(ctx, `select coalesce((select value from meta where key=?),'needed')`, vacuumKey).Scan(&state)
	if err != nil || state == "done" {
		return nil, err
	}
	return measureVacuum(s.cs.Path(), VacuumNotRun)
}

// measureVacuum is the space a VACUUM of the archive at path needs and has.
func measureVacuum(path, reason string) (*VacuumPending, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	p := &VacuumPending{NeedBytes: 2 * uint64(info.Size()), Reason: reason} //nolint:gosec // G115: a file size is non-negative
	avail, measured, err := freeSpace(filepath.Dir(path))
	if err != nil || !measured {
		p.Reason = VacuumUnmeasured
		if err != nil {
			p.Error = err.Error()
		}
		return p, nil
	}
	p.FreeBytes = &avail
	if avail < p.NeedBytes {
		p.Reason = VacuumDiskSpace
	}
	return p, nil
}

// VacuumOnce runs the archive's one-time VACUUM when it still needs it: SQLite rewrites the whole
// archive into a fresh copy and brings it back, so nothing an older version left in free space
// survives in the database file. It runs only when the archive's volume has at least twice the
// archive's size free (SQLite's temporary copy, kept beside the archive, and the write-ahead log
// that brings it back); otherwise it changes nothing and returns the space needed. VACUUM is
// atomic: cancelled or failed, the archive stays as it was and the VACUUM stays pending. After
// it, the archive asks for its write-ahead log to be cleared (ClearPurgedLog), which also gives
// the log's space back. It returns nil when nothing is pending any more.
func (s *Store) VacuumOnce(ctx context.Context) (pending *VacuumPending, vacuumed bool, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	var state string
	if err := conn.QueryRowContext(ctx, `select coalesce((select value from meta where key=?),'needed')`, vacuumKey).Scan(&state); err != nil {
		return nil, false, err
	}
	if state == "done" {
		return nil, false, nil
	}
	path := s.cs.Path()
	if pending, err = measureVacuum(path, ""); err != nil || pending.Reason != "" {
		return pending, false, err
	}
	// SQLite's temporary copy goes to a file beside the archive, not into memory (the archive's
	// own setting) or the system's temporary directory, which may be on another volume. The
	// directory is a process-wide setting that m365crawl sets nowhere else, so it is unset again
	// afterwards; the connection's temp_store goes back to what it was.
	var tempStore int
	if err := conn.QueryRowContext(context.WithoutCancel(ctx), `pragma temp_store`).Scan(&tempStore); err != nil {
		pending.Reason, pending.Error = VacuumFailed, err.Error()
		return pending, false, err
	}
	restore := func() error {
		_, err := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf(`pragma temp_store=%d`, tempStore))
		_, derr := conn.ExecContext(context.WithoutCancel(ctx), `pragma temp_store_directory=''`)
		return errors.Join(err, derr)
	}
	_, verr := conn.ExecContext(context.WithoutCancel(ctx), `pragma temp_store=file`)
	if verr == nil {
		_, verr = conn.ExecContext(context.WithoutCancel(ctx), `pragma temp_store_directory=`+quoteSQL(filepath.Dir(path)))
	}
	if verr == nil {
		_, verr = conn.ExecContext(ctx, `vacuum`)
		if verr != nil {
			pending.Reason, pending.Error = VacuumFailed, verr.Error()
			verr = nil
			if ctx.Err() != nil {
				pending.Reason, verr = VacuumCancelled, ctx.Err()
			}
		}
	}
	if err := errors.Join(verr, restore()); err != nil || pending.Reason != "" {
		if pending.Reason == "" {
			pending.Reason, pending.Error = VacuumFailed, err.Error()
		}
		return pending, false, err
	}
	_, err = conn.ExecContext(context.WithoutCancel(ctx), `insert into meta(key,value) values(?,'done'),(?,'1') on conflict(key) do update set value=excluded.value`, vacuumKey, purgedLogKey)
	return nil, true, err
}

// quoteSQL quotes s as an SQL string literal.
func quoteSQL(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
