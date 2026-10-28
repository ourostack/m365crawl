package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQProgress struct {
	ID, Revision, AdmittedRevision int64
	Cursor, PendingCursor          string `json:"-"`
	Baseline                       bool
}

func (WorkIQProgress) String() string   { return "WorkIQProgress" }
func (WorkIQProgress) GoString() string { return "WorkIQProgress" }

func (s *Store) WorkIQProgressFor(ctx context.Context, scope WorkIQScope, resource string) (WorkIQProgress, error) {
	if _, err := workiq.RelativePath(resource, ""); err != nil {
		return WorkIQProgress{}, err
	}
	return loadWorkIQProgress(ctx, s.db, scope, resource)
}

func loadWorkIQProgress(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, scope WorkIQScope, resource string) (WorkIQProgress, error) {
	if scope.Qualification == "" {
		scope.Qualification = "workiq-v1"
	}
	var out WorkIQProgress
	err := q.QueryRowContext(ctx, `select id,revision,admitted_revision,cursor,pending_cursor,baseline from workiq_frontiers
where family=? and account_id=? and resource=? and scope=? and qualification=? and retired=0`,
		scope.Family, scope.Account, resource, scopeTime(scope.WindowStart), scope.Qualification).
		Scan(&out.ID, &out.Revision, &out.AdmittedRevision, &out.Cursor, &out.PendingCursor, &out.Baseline)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkIQProgress{}, nil
	}
	return out, err
}

func scopeTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func preparedWorkIQFrontier(ctx context.Context, tx *sql.Tx, attempt int64, progress WorkIQProgress) error {
	var count, first, last, unprepared int64
	if err := tx.QueryRowContext(ctx, `select count(*),coalesce(min(expected_revision),-1),coalesce(max(expected_revision),-1),
coalesce(sum(case when prepared_attempt_id=? and prepared_complete=1 then 0 else 1 end),0)
from workiq_pages where frontier_id=? and state='staged' and expected_revision>=? and expected_revision<?`,
		attempt, progress.ID, progress.AdmittedRevision, progress.Revision).Scan(&count, &first, &last, &unprepared); err != nil {
		return err
	}
	if count == 0 || count != progress.Revision-progress.AdmittedRevision || first != progress.AdmittedRevision || last != progress.Revision-1 || unprepared != 0 {
		return errors.New("WorkIQ frontier has unprepared or discontinuous staged pages")
	}
	var next string
	if err := tx.QueryRowContext(ctx, `select next_cursor from workiq_pages where frontier_id=? and expected_revision=? and state='staged'`,
		progress.ID, last).Scan(&next); err != nil {
		return err
	}
	if next != "" {
		return errors.New("WorkIQ frontier has no terminal page")
	}
	return nil
}

func (s *Store) AdmitWorkIQFrontier(ctx context.Context, attempt int64, resource string, expected int64) error {
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
			parsed, err := workIQStoredTime(sql.NullString{String: window, Valid: true})
			if err != nil {
				return err
			}
			scope.WindowStart = *parsed
		}
		p, err := loadWorkIQProgress(ctx, tx, scope, resource)
		if err != nil {
			return err
		}
		if p.ID == 0 || p.Revision != expected {
			return errors.New("WorkIQ frontier changed before admission")
		}
		if p.AdmittedRevision == p.Revision {
			return nil
		}
		if err := preparedWorkIQFrontier(ctx, tx, attempt, p); err != nil {
			return err
		}
		if scope.Family == "mail" {
			var ready bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_censuses where attempt_id=? and account_id=? and complete=1)
and not exists(select 1 from workiq_census_members cm join workiq_pages pg on pg.id=cm.page_id
join workiq_censuses c on c.id=cm.census_id where c.attempt_id=? and pg.frontier_id=? and cm.admitted=0)`,
				attempt, scope.Account, attempt, p.ID).Scan(&ready); err != nil {
				return err
			}
			if !ready {
				return errors.New("WorkIQ mail page data is not admitted from a complete inventory")
			}
		}
		var delta string
		if err := tx.QueryRowContext(ctx, `select delta_cursor from workiq_pages where frontier_id=? and expected_revision=? and state='staged'`,
			p.ID, p.Revision-1).Scan(&delta); err != nil {
			return err
		}
		if strings.Contains(resource, "/messages/delta()") && delta == "" {
			return errors.New("WorkIQ mail baseline has no terminal delta cursor")
		}
		result, err := tx.ExecContext(ctx, `update workiq_frontiers set cursor=?,pending_cursor=?,admitted_revision=revision,baseline=0
where id=? and revision=? and admitted_revision=?`, delta, delta, p.ID, expected, p.AdmittedRevision)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return errors.New("WorkIQ frontier admission lost its revision")
		}
		_, err = tx.ExecContext(ctx, `update workiq_pages set state='admitted' where frontier_id=? and state='staged' and expected_revision>=? and expected_revision<?`,
			p.ID, p.AdmittedRevision, p.Revision)
		return err
	})
}
