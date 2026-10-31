package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// purgedLogKey in meta asks for the write-ahead log to be cleared: a purge set it in the
// transaction that overwrote archived content, and ClearPurgedLog removes it once a checkpoint has
// truncated the log. Keeping it in the archive means a checkpoint a reader blocked is tried again
// by the next sync, not forgotten.
const purgedLogKey = "purged_log_pending"

// How hard ClearPurgedLog tries: purgedLogAttempts checkpoints, each waiting up to purgedLogWait
// for readers, purgedLogPause apart. Tests shorten them.
var (
	purgedLogAttempts = 3
	purgedLogWait     = 2 * time.Second
	purgedLogPause    = 250 * time.Millisecond
)

// PurgedLog is what ClearPurgedLog did. Pending is true when content a purge overwrote may still
// be in the write-ahead log because readers kept every checkpoint from truncating it; the request
// stays in the archive and the next call tries again. Attempts counts the checkpoints taken.
type PurgedLog struct {
	Pending  bool
	Attempts int
}

// secureDelete turns secure_delete on for the connection of tx, so the content a purge replaces
// or deletes is overwritten with zeros in its page and in freed pages, instead of staying there
// until the space is reused. The returned restore sets the connection back to its earlier setting;
// a pragma is not part of the transaction, so restore runs whether or not tx commits.
func secureDelete(ctx context.Context, tx *sql.Tx) (restore func() error, err error) {
	var was int
	if err := tx.QueryRowContext(ctx, `pragma secure_delete`).Scan(&was); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `pragma secure_delete=on`); err != nil {
		return nil, err
	}
	return func() error {
		_, err := tx.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf(`pragma secure_delete=%d`, was))
		return err
	}, nil
}

// markPurgedLog asks, in the purge's own transaction, for the write-ahead log to be cleared.
func markPurgedLog(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `insert into meta(key,value) values(?,'1') on conflict(key) do update set value='1'`, purgedLogKey)
	return err
}

// ClearPurgedLog clears the write-ahead log after a purge: a truncating checkpoint copies the log
// into the database file, so the purge's overwritten pages replace the old ones there, and then
// cuts the log to zero bytes, so no older page image of the purged content stays in it. It does
// nothing unless a purge asked for it. A reader still using an older snapshot keeps the checkpoint
// from finishing; ClearPurgedLog then tries again, and when every attempt is blocked it reports
// Pending and keeps the request for the next call. It never reports a log it could not truncate
// as cleared: every return that leaves the request in place, errors included, reports Pending
// (an error before the request could be read reports it too, since it may be there). Removing the
// request writes the meta table's page to the new log; that page holds no purged content.
func (s *Store) ClearPurgedLog(ctx context.Context) (res PurgedLog, err error) {
	res.Pending = true
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	var pending int
	if err := conn.QueryRowContext(ctx, `select count(*) from meta where key=?`, purgedLogKey).Scan(&pending); err != nil {
		return res, err
	}
	if pending == 0 {
		return PurgedLog{}, nil
	}
	var wait int
	if err := conn.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&wait); err != nil {
		return res, err
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`pragma busy_timeout=%d`, purgedLogWait.Milliseconds())); err != nil {
		return res, err
	}
	defer func() {
		_, rerr := conn.ExecContext(context.WithoutCancel(ctx), fmt.Sprintf(`pragma busy_timeout=%d`, wait))
		err = errors.Join(err, rerr)
	}()
	for {
		res.Attempts++
		// busy is 1 when a reader or writer kept the checkpoint from finishing; outside WAL mode the
		// log counts are -1 and there is no log to clear.
		var busy, frames, done int
		if err := conn.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(&busy, &frames, &done); err != nil {
			return res, err
		}
		if busy == 0 {
			if _, err := conn.ExecContext(ctx, `delete from meta where key=?`, purgedLogKey); err != nil {
				return res, err
			}
			res.Pending = false
			return res, nil
		}
		if res.Attempts >= purgedLogAttempts {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(purgedLogPause):
		}
	}
}
