package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type workIQCensusMailRow struct {
	id         int64
	normalized string
	seen, gone *time.Time
	first      sql.NullInt64
	source     WorkIQSourceState
}

func loadWorkIQCensusMailRows(ctx context.Context, tx *sql.Tx, account string, after int64, limit int) ([]workIQCensusMailRow, error) {
	var out []workIQCensusMailRow
	err := eachRow(ctx, tx, `select item_rowid,graph_id,json_extract(server_projection,'$.ID'),json_type(server_projection,'$.ID'),
json_extract(server_projection,'$.InternetMessageID'),json_type(server_projection,'$.InternetMessageID'),server_seen_at,server_gone_at,server_first_missing_census,
server_deleted_at,server_version,server_deleted_version from workiq_rows where kind='mail' and account_id=? and item_rowid>? order by item_rowid limit ?`,
		[]any{account, after, limit}, func(row *sql.Rows) error {
			var item workIQCensusMailRow
			var graph string
			var id, idType, header, headerType sql.NullString
			var seen, gone, deleted, liveVersion, deletedVersion sql.NullString
			if err := row.Scan(&item.id, &graph, &id, &idType, &header, &headerType, &seen, &gone, &item.first, &deleted, &liveVersion, &deletedVersion); err != nil {
				return err
			}
			if idType.String != "text" || id.String != graph || headerType.String != "text" {
				return errors.New("invalid retained server mail identity")
			}
			item.normalized = workiq.NormalizeMessageID(header.String)
			if item.normalized == "" {
				return errors.New("unknown mail identity prevents a gap-free absence census")
			}
			item.source.Seen = true
			for _, clock := range []struct {
				raw  sql.NullString
				into **time.Time
			}{
				{seen, &item.seen}, {gone, &item.gone}, {deleted, &item.source.DeletedAt},
				{liveVersion, &item.source.VersionAt}, {deletedVersion, &item.source.DeletedVersionAt},
			} {
				at, err := workIQStoredTime(clock.raw)
				if err != nil {
					return err
				}
				*clock.into = at
			}
			out = append(out, item)
			return nil
		})
	return out, err
}

func applyWorkIQCensusMailRows(ctx context.Context, tx *sql.Tx, account string, census int64, proof string, at time.Time, items []workIQCensusMailRow) error {
	var ids []int64
	for _, item := range items {
		ids = append(ids, item.id)
		if workIQOwnDeletion(item.source) != nil || item.seen != nil && item.seen.After(at) {
			continue
		}
		var present bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_census_members where census_id=? and normalized_id=? and removed=0)`,
			census, item.normalized).Scan(&present); err != nil {
			return err
		}
		if present {
			continue
		}
		if !item.first.Valid {
			if _, err := tx.ExecContext(ctx, `update workiq_rows set server_first_missing_census=? where kind='mail' and account_id=? and item_rowid=?`,
				census, account, item.id); err != nil {
				return err
			}
			continue
		}
		if item.first.Int64 == census || item.gone != nil {
			continue
		}
		if item.first.Int64 >= census {
			return errors.New("WorkIQ census ordering is inconsistent")
		}
		var previous bool
		var priorProof string
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_censuses c where c.id=? and c.account_id=? and c.complete=1 and c.full_enumeration=1
and not exists(select 1 from workiq_gaps g where g.attempt_id=c.attempt_id and g.resolved=0)),
coalesce((select enumeration_key from workiq_censuses where id=?),'')`, item.first.Int64, account, item.first.Int64).Scan(&previous, &priorProof); err != nil {
			return err
		}
		if !previous || priorProof == "" {
			return errors.New("previous WorkIQ absence census is not a gap-free full enumeration")
		}
		if priorProof == proof {
			continue
		}
		if _, err := tx.ExecContext(ctx, `update workiq_rows set server_gone_at=? where kind='mail' and account_id=? and item_rowid=?`,
			workIQTimeValue(&at), account, item.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update mail_messages set modified_at=? where account=? and rowid=?`,
			fmtTime(at), account, item.id); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return reduceWorkIQMailMarks(ctx, tx, account, ids...)
}

// ConfirmWorkIQMailCensusBatch has a persisted read-only identity-preflight
// phase before any absence mark. Every later call marks at most limit rows and
// rechecks the full-census/admission/gap proof in the same short transaction.
func (s *Store) ConfirmWorkIQMailCensusBatch(ctx context.Context, attempt int64, limit int) (bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return false, err
	}
	if binding.Fact.Family != "mail" || limit < 1 || limit > 128 {
		return false, errors.New("invalid bounded mail census scope")
	}
	done := false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var census, checked, confirmed int64
		var complete, full, preflight, already bool
		var finished sql.NullString
		var proof string
		if err := tx.QueryRowContext(ctx, `select id,complete,full_enumeration,completed_at,identity_checked_through,identity_check_complete,
confirmed_through,confirmation_complete,enumeration_key from workiq_censuses where attempt_id=? and account_id=?`,
			attempt, binding.Fact.ID).Scan(&census, &complete, &full, &finished, &checked, &preflight, &confirmed, &already, &proof); err != nil {
			return err
		}
		at, err := workIQStoredTime(finished)
		if err != nil {
			return err
		}
		if !complete || !full || at == nil || at.IsZero() || proof == "" {
			return errors.New("WorkIQ mail absence requires a completed full enumeration")
		}
		var blocked bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_gaps where attempt_id=? and resolved=0)
or exists(select 1 from workiq_census_members where census_id=? and admitted=0)`, attempt, census).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return errors.New("WorkIQ mail census still has a gap or unadmitted item")
		}
		if already {
			done = true
			return nil
		}
		after := checked
		if preflight {
			after = confirmed
		}
		items, err := loadWorkIQCensusMailRows(ctx, tx, binding.Fact.ID, after, limit)
		if err != nil {
			return err
		}
		next := after
		if len(items) > 0 {
			next = items[len(items)-1].id
		}
		finishedBatch := len(items) < limit
		var result sql.Result
		if !preflight {
			result, err = tx.ExecContext(ctx, `update workiq_censuses set identity_checked_through=?,identity_check_complete=?
where id=? and identity_checked_through=? and identity_check_complete=0`, next, b2i(finishedBatch), census, checked)
		} else {
			if err := applyWorkIQCensusMailRows(ctx, tx, binding.Fact.ID, census, proof, *at, items); err != nil {
				return err
			}
			result, err = tx.ExecContext(ctx, `update workiq_censuses set confirmed_through=?,confirmation_complete=?
where id=? and confirmed_through=? and confirmation_complete=0`, next, b2i(finishedBatch), census, confirmed)
			done = finishedBatch
		}
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("bounded WorkIQ census progress changed before confirmation")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return done, nil
}
