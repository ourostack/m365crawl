package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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
	_, _, pending, err := s.workIQMailRecheck(ctx, attempt)
	return pending, err
}

// workIQMailRecheck is the recheck an attempt's mail account owes. key scopes its restart record
// to the account and qualification; epoch names the recheck by its latest removal census and the
// complete checks that followed it, so each owed check has its own epoch.
func (s *Store) workIQMailRecheck(ctx context.Context, attempt int64) (key, epoch string, pending bool, err error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return "", "", false, err
	}
	if binding.Fact.Family != "mail" {
		return "", "", false, errors.New("mail census recheck requires a mail owner")
	}
	var trigger int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(max(c.id),0) from workiq_censuses c
join workiq_census_members m on m.census_id=c.id where c.account_id=? and m.removed=1`,
		binding.Fact.ID).Scan(&trigger); err != nil {
		return "", "", false, err
	}
	if trigger == 0 {
		return "", "", false, nil
	}
	var completed int
	var qualification string
	if err := s.db.QueryRowContext(ctx, `select count(distinct c.enumeration_key),
(select qualification from workiq_attempts where id=?) from workiq_censuses c
join workiq_attempts a on a.id=c.attempt_id
where c.account_id=? and c.id>? and c.complete=1 and c.full_enumeration=1
and c.confirmation_complete=1 and c.enumeration_key<>'' and c.source_data_at is not null
and a.family='mail' and a.account_id=c.account_id
and (a.status='ok' or (a.id=? and a.status='open'))
and not exists(select 1 from workiq_gaps g where g.attempt_id=a.id and g.resolved=0)`,
		attempt, binding.Fact.ID, trigger, attempt).Scan(&completed, &qualification); err != nil {
		return "", "", false, err
	}
	scope, _ := json.Marshal([]string{binding.Fact.ID, qualification}) // strings always encode
	return "workiq_mail_recheck:" + string(scope), fmt.Sprintf("%d:%d", trigger, completed), completed < 2, nil
}

// WorkIQMailRecheckContinues says the owed recheck already retired the account's cursors and its
// rebuild is still on its first pass, so this run continues that rebuild instead of retiring its
// progress, whatever stopped the previous run (a page budget, a transient failure, a busy archive,
// a failed start). Once any of the account's current cursors is admitted the rebuild's traversal is
// over and only a restart can make the full check, so the check restarts. A finished complete check
// changes the epoch, so the next owed check restarts too.
func (s *Store) WorkIQMailRecheckContinues(ctx context.Context, attempt int64) (bool, error) {
	key, epoch, pending, err := s.workIQMailRecheck(ctx, attempt)
	if err != nil || !pending {
		return false, err
	}
	var recorded string
	var admitted bool
	err = s.db.QueryRowContext(ctx, `select coalesce((select value from meta where key=?),''),
exists(select 1 from workiq_frontiers f join workiq_attempts a on a.id=?
where f.family='mail' and f.account_id=a.account_id and f.qualification=a.qualification and f.retired=0 and f.baseline=0)`,
		key, attempt).Scan(&recorded, &admitted)
	return recorded == epoch && !admitted, err
}

// MarkWorkIQMailRecheckRestarted records that the owed recheck retired the account's cursors.
func (s *Store) MarkWorkIQMailRecheckRestarted(ctx context.Context, attempt int64) error {
	key, epoch, pending, err := s.workIQMailRecheck(ctx, attempt)
	if err != nil {
		return err
	}
	if !pending {
		return errors.New("no mail census recheck is owed")
	}
	_, err = s.db.ExecContext(ctx, `insert into meta(key,value) values(?,?) on conflict(key) do update set value=excluded.value`, key, epoch)
	return err
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
