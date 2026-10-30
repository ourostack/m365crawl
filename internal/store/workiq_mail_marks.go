package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

func workIQMailMarkSelection(account string, ids []int64) (string, []any) {
	scope := " where kind='mail' and account_id=?"
	args := []any{account}
	if len(ids) > 0 {
		scope += " and item_rowid in (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	return scope, args
}

func retainWorkIQMailCacheMarks(ctx context.Context, tx *sql.Tx, account string, ids ...int64) error {
	have, err := workIQHasRows(ctx, tx, "mail")
	if err != nil || !have {
		return err
	}
	scope, args := workIQMailMarkSelection(account, ids)
	//nolint:gosec // G202: scope contains only fixed predicates/placeholders; all values are bound.
	_, err = tx.ExecContext(ctx, `update workiq_rows set
cache_first_miss_at=coalesce(cache_first_miss_at,(select a.first_missed_at from mail_absent a join mail_messages m on m.account=a.account and m.detail_key=a.detail_key where m.account=workiq_rows.account_id and m.rowid=workiq_rows.item_rowid)),
cache_gone_at=coalesce(cache_gone_at,(select m.gone_at from mail_messages m where m.account=workiq_rows.account_id and m.rowid=workiq_rows.item_rowid)),
cache_evicted_at=coalesce(cache_evicted_at,(select m.evicted_at from mail_messages m where m.account=workiq_rows.account_id and m.rowid=workiq_rows.item_rowid))
`+scope+` and cache_seen=1`, args...)
	return err
}

func reduceWorkIQMailMarks(ctx context.Context, tx *sql.Tx, account string, ids ...int64) error {
	have, err := workIQHasRows(ctx, tx, "mail")
	if err != nil || !have {
		return err
	}
	type mark struct {
		rowid                  int64
		gone, evicted, deleted *time.Time
	}
	var updates []mark
	scope, args := workIQMailMarkSelection(account, ids)
	err = eachRow(ctx, tx, `select item_rowid,cache_seen,server_seen,cache_first_miss_at,cache_gone_at,cache_evicted_at,server_seen_at,server_gone_at,
cache_deleted_at,server_deleted_at,server_version,server_deleted_version from workiq_rows`+scope, args, func(row *sql.Rows) error {
		var id int64
		var in WorkIQReduceInput
		var first, gone, evicted, seen, serverGone, cacheDeleted, serverDeleted, liveVersion, deleteVersion sql.NullString
		if err := row.Scan(&id, &in.Cache.Seen, &in.Server.Seen, &first, &gone, &evicted, &seen, &serverGone, &cacheDeleted, &serverDeleted, &liveVersion, &deleteVersion); err != nil {
			return err
		}
		for _, clock := range []struct {
			raw  sql.NullString
			into **time.Time
		}{
			{first, &in.Cache.FirstMissAt}, {gone, &in.Cache.GoneAt}, {evicted, &in.Cache.EvictedAt},
			{seen, &in.Server.SeenAt}, {serverGone, &in.Server.GoneAt}, {cacheDeleted, &in.Cache.DeletedAt}, {serverDeleted, &in.Server.DeletedAt},
			{liveVersion, &in.Server.VersionAt}, {deleteVersion, &in.Server.DeletedVersionAt},
		} {
			at, err := workIQStoredTime(clock.raw)
			if err != nil {
				return err
			}
			*clock.into = at
		}
		effective := reduceWorkIQState(in)
		updates = append(updates, mark{id, effective.GoneAt, effective.EvictedAt, effective.DeletedAt})
		return nil
	})
	if err != nil {
		return err
	}
	for _, u := range updates {
		var gone, evicted, deleted any
		if u.gone != nil {
			gone = fmtTime(*u.gone)
		}
		if u.evicted != nil {
			evicted = fmtTime(*u.evicted)
		}
		if u.deleted != nil {
			deleted = fmtTime(*u.deleted)
		}
		if _, err := tx.ExecContext(ctx, `update mail_messages set gone_at=?,evicted_at=?,deleted_at=? where account=? and rowid=?`,
			gone, evicted, deleted, account, u.rowid); err != nil {
			return err
		}
	}
	return nil
}
