package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

type WorkIQMailInventoryProof struct {
	attempt     int64
	scope       WorkIQScope
	full        bool
	folders     int
	enumeration string
	dataAt      time.Time
	revision    int64
	file        os.FileInfo
}

var workIQInventoryStat = os.Stat

func (WorkIQMailInventoryProof) String() string   { return "WorkIQ private inventory proof" }
func (WorkIQMailInventoryProof) GoString() string { return "WorkIQ private inventory proof" }

func bumpWorkIQMailInventory(ctx context.Context, tx *sql.Tx, account string) error {
	_, err := tx.ExecContext(ctx, `update workiq_accounts set inventory_revision=inventory_revision+1 where family='mail' and account_id=?`, account)
	return err
}

// InspectWorkIQMailInventory performs the potentially large tree/frontier proof
// in one read-only snapshot, without acquiring the archive writer role.
func (s *Store) InspectWorkIQMailInventory(ctx context.Context, attempt int64) (WorkIQMailInventoryProof, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	if binding.Fact.Family != "mail" {
		return WorkIQMailInventoryProof{}, errors.New("mail inventory proof requires a mail account")
	}
	before, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var proof WorkIQMailInventoryProof
	if err := workIQMailInventoryTransaction(ctx, tx, attempt, binding.Fact.ID, time.Time{}, &proof, false); err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	if err := tx.QueryRowContext(ctx, `select inventory_revision from workiq_accounts where family='mail' and account_id=?`, binding.Fact.ID).Scan(&proof.revision); err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	if err := tx.Commit(); err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	after, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return WorkIQMailInventoryProof{}, err
	}
	if !os.SameFile(before, after) {
		return WorkIQMailInventoryProof{}, errors.New("archive changed during mail inventory inspection")
	}
	proof.file = before
	return proof, nil
}

// SealWorkIQMailInventoryProof only verifies the opaque proof's owner, physical
// archive and current staging revision, then publishes one small census receipt.
func (s *Store) SealWorkIQMailInventoryProof(ctx context.Context, attempt int64, proof WorkIQMailInventoryProof, at time.Time) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if binding.Fact.Family != "mail" || proof.file == nil || proof.attempt != attempt || proof.scope.Account != binding.Fact.ID ||
		proof.enumeration == "" || proof.dataAt.IsZero() || at.IsZero() {
		return errors.New("invalid or foreign mail inventory proof")
	}
	current, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return err
	}
	if !os.SameFile(proof.file, current) {
		return errors.New("mail inventory proof belongs to a different archive")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var qualification string
		var revision int64
		if err := tx.QueryRowContext(ctx, `select a.qualification,w.inventory_revision from workiq_attempts a join workiq_accounts w
on w.family=a.family and w.account_id=a.account_id where a.id=?`, attempt).Scan(&qualification, &revision); err != nil {
			return err
		}
		if revision != proof.revision || qualification != proof.scope.Qualification {
			return errors.New("mail staging changed after inventory inspection")
		}
		var indexReady bool
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from meta where key=?)`, workIQMailIndexPrefix+binding.Fact.ID).Scan(&indexReady); err != nil {
			return err
		}
		if !indexReady {
			return errors.New("native mail candidate index is not prepared")
		}
		var id int64
		var ready bool
		var previous string
		err := tx.QueryRowContext(ctx, `select id,inventory_ready,enumeration_key from workiq_censuses where attempt_id=? and account_id=?`,
			attempt, binding.Fact.ID).Scan(&id, &ready, &previous)
		if errors.Is(err, sql.ErrNoRows) {
			r, err := tx.ExecContext(ctx, `insert into workiq_censuses(attempt_id,account_id) values(?,?)`, attempt, binding.Fact.ID)
			if err != nil {
				return err
			}
			id, err = r.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if ready {
			if previous != proof.enumeration {
				return errors.New("mail census already has another inventory proof")
			}
			return nil
		}
		r, err := tx.ExecContext(ctx, `update workiq_censuses set inventory_ready=1,full_enumeration=?,carry_complete=?,complete=?,completed_at=?,
enumeration_key=?,source_data_at=? where id=? and inventory_ready=0`, b2i(proof.full), b2i(proof.full), b2i(proof.full),
			workIQTimeValue(&at), proof.enumeration, workIQTimeValue(&proof.dataAt), id)
		if err != nil {
			return err
		}
		n, err := r.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("mail inventory receipt changed before publication")
		}
		return nil
	})
}
