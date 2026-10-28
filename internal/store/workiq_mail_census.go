package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) ConfirmWorkIQMailCensus(ctx context.Context, attempt int64) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if binding.Fact.Family != "mail" {
		return errors.New("WorkIQ absence proof requires a mail account")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var census int64
		var complete, full bool
		var finished sql.NullString
		if err := tx.QueryRowContext(ctx, `select id,complete,full_enumeration,completed_at from workiq_censuses where attempt_id=? and account_id=?`,
			attempt, binding.Fact.ID).Scan(&census, &complete, &full, &finished); err != nil {
			return err
		}
		at, err := workIQStoredTime(finished)
		if err != nil {
			return err
		}
		if !complete || !full || at == nil || at.IsZero() {
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
		type item struct {
			id                  int64
			normalized          string
			seen, deleted, gone sql.NullString
			first               sql.NullInt64
		}
		var items []item
		if err := eachRow(ctx, tx, `select item_rowid,server_projection,server_seen_at,server_deleted_at,server_first_missing_census,server_gone_at
from workiq_rows where kind='mail' and account_id=?`, []any{binding.Fact.ID}, func(row *sql.Rows) error {
			var i item
			var raw string
			if err := row.Scan(&i.id, &raw, &i.seen, &i.deleted, &i.first, &i.gone); err != nil {
				return err
			}
			var m workiq.MailMessage
			if json.Unmarshal([]byte(raw), &m) != nil {
				return errors.New("invalid retained server mail identity")
			}
			i.normalized = workiq.NormalizeMessageID(m.InternetMessageID)
			if i.normalized == "" {
				return errors.New("unknown mail identity prevents a gap-free absence census")
			}
			items = append(items, i)
			return nil
		}); err != nil {
			return err
		}
		for _, i := range items {
			seen, err := workIQStoredTime(i.seen)
			if err != nil {
				return err
			}
			if i.deleted.Valid || seen != nil && seen.After(*at) {
				continue
			}
			var present bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_census_members where census_id=? and normalized_id=? and removed=0)`,
				census, i.normalized).Scan(&present); err != nil {
				return err
			}
			if present {
				continue
			} // Actual live admission, not enumeration replay, clears marks.
			if !i.first.Valid {
				if _, err := tx.ExecContext(ctx, `update workiq_rows set server_first_missing_census=? where kind='mail' and account_id=? and item_rowid=?`,
					census, binding.Fact.ID, i.id); err != nil {
					return err
				}
				continue
			}
			if i.first.Int64 == census || i.gone.Valid {
				continue
			}
			if i.first.Int64 >= census {
				return errors.New("WorkIQ census ordering is inconsistent")
			}
			var previous bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_censuses c where c.id=? and c.account_id=? and c.complete=1 and c.full_enumeration=1
and not exists(select 1 from workiq_gaps g where g.attempt_id=c.attempt_id and g.resolved=0))`, i.first.Int64, binding.Fact.ID).Scan(&previous); err != nil {
				return err
			}
			if !previous {
				return errors.New("previous WorkIQ absence census is not a gap-free full enumeration")
			}
			if _, err := tx.ExecContext(ctx, `update workiq_rows set server_gone_at=? where kind='mail' and account_id=? and item_rowid=?`,
				workIQTimeValue(at), binding.Fact.ID, i.id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `update mail_messages set modified_at=? where account=? and rowid=?`,
				fmtTime(*at), binding.Fact.ID, i.id); err != nil {
				return err
			}
		}
		return reduceWorkIQMailMarks(ctx, tx, binding.Fact.ID)
	})
}
