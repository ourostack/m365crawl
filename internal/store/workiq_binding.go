package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/errs"
	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) VerifyWorkIQAttempt(ctx context.Context, attempt int64, binding workiq.AccountBinding, pinned workiq.User, at time.Time) error {
	if err := binding.Verify(pinned); err != nil {
		return err
	}
	enabled, err := s.WorkIQEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled || at.IsZero() {
		return errors.New("WorkIQ binding requires opt-in and a verification clock")
	}
	var family, account, status string
	if err := s.db.QueryRowContext(ctx, `select family,account_id,status from workiq_attempts where id=?`, attempt).
		Scan(&family, &account, &status); err != nil {
		return err
	}
	if status != "open" || account != binding.Fact.ID || family != binding.Fact.Family {
		return errors.New("WorkIQ binding does not match an open account attempt")
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return err
	}
	var current workiq.AccountBinding
	if family == "mail" {
		current, err = workiq.SelectMailAccount(pinned, facts, account)
	} else {
		current, err = workiq.SelectChatAccount(pinned, workiq.Chat{TenantID: binding.Fact.TenantID}, facts, account)
	}
	if err != nil {
		return err
	}
	kind := "stored_identity"
	if current.Bootstrap {
		kind = "explicit_bootstrap"
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var currentStatus, boundUser string
		if err := tx.QueryRowContext(ctx, `select status,bound_user_id from workiq_attempts where id=?`, attempt).
			Scan(&currentStatus, &boundUser); err != nil {
			return err
		}
		if currentStatus != "open" || boundUser != "" && boundUser != pinned.ID {
			return errors.New("WorkIQ attempt identity or outcome has changed")
		}
		var previous string
		err := tx.QueryRowContext(ctx, `select user_id from workiq_accounts where family=? and account_id=?`, family, account).Scan(&previous)
		switch {
		case err == nil && previous != pinned.ID:
			return &errs.Coded{Code: "workiq_identity_mismatch", Exit: errs.ExitEnvironment,
				Message: "the verified WorkIQ user differs from this archive account's retained binding",
				Fix:     "Use the original signed-in user or a separate archive account; no mismatch override is available."}
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into workiq_accounts(family,account_id,user_id,tenant_id,address,binding,bound_at) values(?,?,?,?,?,?,?)
on conflict(family,account_id) do update set address=excluded.address,binding=excluded.binding,bound_at=excluded.bound_at`,
			family, account, pinned.ID, current.Fact.TenantID, binding.Address, kind, workIQTimeValue(&at)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `update workiq_attempts set bound_user_id=?,bound_address=?,bound_mail=?,bound_upn=?,verified_at=? where id=? and status='open'`,
			pinned.ID, binding.Address, pinned.Mail, pinned.UPN, workIQTimeValue(&at), attempt)
		return err
	})
}

// validateWorkIQAttemptOwner re-reads archive identity after network I/O and before retention.
// The caller holds its short writer role; no external call is made under that role.
func (s *Store) validateWorkIQAttemptOwner(ctx context.Context, attempt int64) (workiq.AccountBinding, error) {
	return s.validateWorkIQOwner(ctx, attempt, true)
}

func (s *Store) validateWorkIQOwner(ctx context.Context, attempt int64, retention bool) (workiq.AccountBinding, error) {
	return s.validateWorkIQOwnerKind(ctx, attempt, retention, false)
}

// validateWorkIQMailRetention admits a verified whole-account or window mail attempt, for the
// steps both share, such as preparing the Message-ID index.
func (s *Store) validateWorkIQMailRetention(ctx context.Context, attempt int64) (workiq.AccountBinding, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		if window, windowErr := s.validateWorkIQWindowOwner(ctx, attempt); windowErr == nil {
			return window, nil
		}
	}
	return binding, err
}

// validateWorkIQWindowOwner admits only an open, verified mail window attempt: window retention
// never runs under a whole-account attempt, and whole-account retention never under a window.
func (s *Store) validateWorkIQWindowOwner(ctx context.Context, attempt int64) (workiq.AccountBinding, error) {
	return s.validateWorkIQOwnerKind(ctx, attempt, true, true)
}

func (s *Store) validateWorkIQOwnerKind(ctx context.Context, attempt int64, retention, window bool) (workiq.AccountBinding, error) {
	enabled, err := s.WorkIQEnabled(ctx)
	if err != nil {
		return workiq.AccountBinding{}, err
	}
	if !enabled {
		return workiq.AccountBinding{}, errors.New("WorkIQ source is disabled")
	}
	var family, account, status, userID, address, mail, upn, kind string
	var verified sql.NullString
	if err := s.db.QueryRowContext(ctx, `select family,account_id,status,bound_user_id,bound_address,bound_mail,bound_upn,verified_at,kind
from workiq_attempts where id=?`, attempt).Scan(&family, &account, &status, &userID, &address, &mail, &upn, &verified, &kind); err != nil {
		return workiq.AccountBinding{}, err
	}
	if status != "open" || userID == "" || !verified.Valid {
		return workiq.AccountBinding{}, errors.New("WorkIQ data attempt has not been identity verified")
	}
	allowed := map[string]bool{"sync": true, "coverage": !retention, "status": !retention, "mail_window": !retention}
	if window {
		allowed = map[string]bool{"mail_window": true}
	}
	if !allowed[kind] {
		return workiq.AccountBinding{}, errors.New("WorkIQ outcome kind does not permit data retention")
	}
	clock, err := workIQStoredTime(verified)
	if err != nil {
		return workiq.AccountBinding{}, err
	}
	if clock.IsZero() {
		return workiq.AccountBinding{}, errors.New("WorkIQ verification clock is unknown")
	}
	user := workiq.User{ID: userID, Mail: mail, UPN: upn}
	binding := workiq.AccountBinding{Fact: workiq.AccountFact{ID: account, Family: family}, UserID: userID, Address: address}
	if err := binding.Verify(user); err != nil {
		return workiq.AccountBinding{}, err
	}
	var previous, tenant string
	if err := s.db.QueryRowContext(ctx, `select user_id,tenant_id from workiq_accounts where family=? and account_id=?`, family, account).
		Scan(&previous, &tenant); err != nil {
		return workiq.AccountBinding{}, err
	}
	if previous != userID {
		return workiq.AccountBinding{}, errors.New("WorkIQ retained account identity has changed")
	}
	facts, err := s.WorkIQAccounts(ctx)
	if err != nil {
		return workiq.AccountBinding{}, err
	}
	if family == "mail" {
		binding, err = workiq.SelectMailAccount(user, facts, account)
	} else {
		binding, err = workiq.SelectChatAccount(user, workiq.Chat{TenantID: tenant}, facts, account)
	}
	if err != nil {
		return workiq.AccountBinding{}, err
	}
	binding.Address = address // retain the actual verified pin, not a newly preferred alias
	return binding, nil
}
