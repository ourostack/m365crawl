package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQMailFrontier struct {
	ID, Revision int64
	Resource     string
}

// ConfirmWorkIQMailCensusIfFull never treats an incremental inventory as
// absence evidence. The strict confirmer still proves and marks full censuses.
func (s *Store) ConfirmWorkIQMailCensusIfFull(ctx context.Context, attempt int64, limit int) (bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return false, err
	}
	if binding.Fact.Family != "mail" || limit < 1 || limit > 128 {
		return false, errors.New("invalid bounded mail census confirmation")
	}
	var complete, full, ready, carried bool
	if err := s.db.QueryRowContext(ctx, `select complete,full_enumeration,inventory_ready,carry_complete
from workiq_censuses where attempt_id=? and account_id=?`, attempt, binding.Fact.ID).
		Scan(&complete, &full, &ready, &carried); err != nil {
		return false, err
	}
	if !complete || !ready || !carried {
		return false, errors.New("mail census inventory is not complete")
	}
	if !full {
		return true, nil
	}
	return s.ConfirmWorkIQMailCensusBatch(ctx, attempt, limit)
}

// WorkIQMailRecheckPending is derived from actual removed items and distinct
// completed census identities. Cancellation cannot erase the request, and
// replaying one enumeration cannot satisfy it twice.
func (s *Store) WorkIQMailRecheckPending(ctx context.Context, attempt int64) (bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return false, err
	}
	if binding.Fact.Family != "mail" {
		return false, errors.New("mail census recheck requires a mail owner")
	}
	var trigger int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(c.id),0) from workiq_censuses c
join workiq_census_members m on m.census_id=c.id where c.account_id=? and m.removed=1`,
		binding.Fact.ID).Scan(&trigger); err != nil {
		return false, err
	}
	if trigger == 0 {
		return false, nil
	}
	var completed int
	if err := s.db.QueryRowContext(ctx, `select count(distinct c.enumeration_key) from workiq_censuses c
join workiq_attempts a on a.id=c.attempt_id
where c.account_id=? and c.id>? and c.complete=1 and c.full_enumeration=1
and c.confirmation_complete=1 and c.enumeration_key<>'' and c.source_data_at is not null
and a.family='mail' and a.account_id=c.account_id
and (a.status='ok' or (a.id=? and a.status='open'))
and not exists(select 1 from workiq_gaps g where g.attempt_id=a.id and g.resolved=0)`,
		binding.Fact.ID, trigger, attempt).Scan(&completed); err != nil {
		return false, err
	}
	return completed < 2, nil
}

func (s *Store) WorkIQMailResetFrontiers(ctx context.Context, attempt, after int64, limit int) ([]WorkIQMailFrontier, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return nil, err
	}
	if binding.Fact.Family != "mail" || after < 0 || limit < 1 || limit > 128 {
		return nil, errors.New("invalid bounded mail baseline frontier traversal")
	}
	var out []WorkIQMailFrontier
	err = mailEach(ctx, s.db, `select f.id,f.revision,f.resource from workiq_frontiers f
join workiq_attempts a on a.id=? and a.family=f.family and a.account_id=f.account_id
and a.qualification=f.qualification and coalesce(a.window_start,'')=f.scope
where f.retired=0 and f.id>? order by f.id limit ?`, []any{attempt, after, limit}, func(row *sql.Rows) error {
		var f WorkIQMailFrontier
		if err := row.Scan(&f.ID, &f.Revision, &f.Resource); err != nil {
			return err
		}
		if f.ID < 1 || f.Revision < 0 {
			return errors.New("invalid retained mail frontier identity/revision")
		}
		if _, err := workiq.RelativePath(f.Resource, ""); err != nil {
			return err
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
