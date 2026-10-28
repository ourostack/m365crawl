package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// RestartWorkIQFrontier is only for a newly verified attempt after the failed
// attempt has been settled. It retains all prior cursor/page evidence and never
// mixes a baseline restart into a partly materialized candidate census.
func (s *Store) RestartWorkIQFrontier(ctx context.Context, attempt int64, resource string, expected int64) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if _, err := workiq.RelativePath(resource, ""); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		scope := WorkIQScope{Family: binding.Fact.Family, Account: binding.Fact.ID}
		var window string
		if err := tx.QueryRowContext(ctx, `select coalesce(window_start,''),qualification from workiq_attempts where id=?`, attempt).Scan(&window, &scope.Qualification); err != nil {
			return err
		}
		if window != "" {
			at, err := workIQStoredTime(sql.NullString{String: window, Valid: true})
			if err != nil {
				return err
			}
			scope.WindowStart = *at
		}
		var used bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_pages where attempt_id=?)
or exists(select 1 from workiq_censuses where attempt_id=?)`, attempt, attempt).Scan(&used); err != nil {
			return err
		}
		if used {
			return errors.New("WorkIQ baseline restart requires a fresh attempt")
		}
		p, err := loadWorkIQProgress(ctx, tx, scope, resource)
		if err != nil {
			return err
		}
		if p.ID == 0 || p.Revision != expected {
			return errors.New("WorkIQ frontier changed before baseline restart")
		}
		result, err := tx.ExecContext(ctx, `update workiq_frontiers set retired=1 where id=? and revision=? and retired=0`, p.ID, expected)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("WorkIQ baseline restart lost its frontier revision")
		}
		_, err = tx.ExecContext(ctx, `update workiq_pages set state='retired' where frontier_id=? and state='staged'`, p.ID)
		return err
	})
}
