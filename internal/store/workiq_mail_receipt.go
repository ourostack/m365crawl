package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

type WorkIQMailCompletionProof struct {
	attempt       int64
	account       string
	qualification string
	enumeration   string
	dataAt        time.Time
	folders       int
	revision      int64
	file          os.FileInfo
}

func (WorkIQMailCompletionProof) String() string   { return "WorkIQ private completion proof" }
func (WorkIQMailCompletionProof) GoString() string { return "WorkIQ private completion proof" }

func workIQInventoryFrontier(ctx context.Context, tx *sql.Tx, attempt int64, progress WorkIQProgress, admitted bool) error {
	if !admitted {
		return preparedWorkIQFrontier(ctx, tx, attempt, progress)
	}
	if progress.ID == 0 || progress.Revision < 1 || progress.AdmittedRevision != progress.Revision {
		return errors.New("mail completion has an unadmitted frontier")
	}
	var count, first, last, unprepared int64
	if err := tx.QueryRowContext(ctx, `select count(*),coalesce(min(expected_revision),-1),coalesce(max(expected_revision),-1),
coalesce(sum(case when prepared_complete=1 then 0 else 1 end),0)
from workiq_pages where frontier_id=? and prepared_attempt_id=? and state='admitted'`,
		progress.ID, attempt).Scan(&count, &first, &last, &unprepared); err != nil {
		return err
	}
	if count == 0 || first < 0 || last != progress.Revision-1 || count != last-first+1 || unprepared != 0 {
		return errors.New("mail completion has no contiguous prepared admission")
	}
	var next, delta string
	if err := tx.QueryRowContext(ctx, `select next_cursor,delta_cursor from workiq_pages
where frontier_id=? and expected_revision=? and prepared_attempt_id=? and state='admitted'`,
		progress.ID, last, attempt).Scan(&next, &delta); err != nil {
		return err
	}
	if next != "" || delta != progress.Cursor || progress.PendingCursor != progress.Cursor {
		return errors.New("mail completion cursor does not match the terminal admitted page")
	}
	return nil
}

// InspectWorkIQMailCompletion proves the actual tree, prepared/admitted pages,
// canonical membership and original observations in a read-only snapshot.
// The returned capability binds one attempt, physical archive and staging epoch.
func (s *Store) InspectWorkIQMailCompletion(ctx context.Context, attempt int64) (WorkIQMailCompletionProof, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	if binding.Fact.Family != "mail" {
		return WorkIQMailCompletionProof{}, errors.New("mail completion requires a data mail owner")
	}
	pending, err := s.WorkIQMailRecheckPending(ctx, attempt)
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	if pending {
		return WorkIQMailCompletionProof{}, errors.New("mail completion has pending census checks")
	}
	before, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var complete, ready, carried, full, confirmed, gaps bool
	var enumeration, observed string
	var revision int64
	if err := tx.QueryRowContext(ctx, `select c.complete,c.inventory_ready,c.carry_complete,c.full_enumeration,c.confirmation_complete,
c.enumeration_key,c.source_data_at,w.inventory_revision,
exists(select 1 from workiq_gaps g where g.attempt_id=c.attempt_id and g.resolved=0)
from workiq_censuses c join workiq_accounts w on w.family='mail' and w.account_id=c.account_id
where c.attempt_id=? and c.account_id=?`, attempt, binding.Fact.ID).
		Scan(&complete, &ready, &carried, &full, &confirmed, &enumeration, &observed, &revision, &gaps); err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	if !complete || !ready || !carried || full && !confirmed || gaps || enumeration == "" {
		return WorkIQMailCompletionProof{}, errors.New("mail completion is not gap-free and fully admitted")
	}
	var inventory WorkIQMailInventoryProof
	if err := workIQMailInventoryTransaction(ctx, tx, attempt, binding.Fact.ID, time.Time{}, &inventory, true); err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	at, err := workIQStoredTime(sql.NullString{String: observed, Valid: true})
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	if at == nil || at.IsZero() || enumeration != inventory.enumeration || !at.Equal(inventory.dataAt) {
		return WorkIQMailCompletionProof{}, errors.New("mail completion changed its original enumeration evidence")
	}
	if err := tx.Commit(); err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	after, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return WorkIQMailCompletionProof{}, err
	}
	if !os.SameFile(before, after) {
		return WorkIQMailCompletionProof{}, errors.New("archive changed during mail completion inspection")
	}
	return WorkIQMailCompletionProof{attempt: attempt, account: binding.Fact.ID,
		qualification: inventory.scope.Qualification, enumeration: enumeration, dataAt: *at,
		folders: inventory.folders, revision: revision, file: before}, nil
}

func (s *Store) recordWorkIQMailReceipt(ctx context.Context, tx *sql.Tx, attempt int64, family, account, kind string, summary WorkIQSummary, run int64) (bool, error) {
	if summary.MailProof == nil {
		return false, nil
	}
	proof := summary.MailProof
	if family != "mail" || kind != "sync" || summary.Status != "ok" || len(summary.Gaps) != 0 ||
		proof.file == nil || proof.attempt != attempt || proof.account != account || proof.enumeration == "" ||
		proof.dataAt.IsZero() || summary.FinishedAt.Before(proof.dataAt) {
		return false, errors.New("invalid or foreign complete mail proof")
	}
	file, err := workIQInventoryStat(s.cs.Path())
	if err != nil {
		return false, err
	}
	if !os.SameFile(file, proof.file) {
		return false, errors.New("mail completion proof belongs to another archive")
	}
	var revision int64
	var qualification string
	if err := tx.QueryRowContext(ctx, `select w.inventory_revision,a.qualification from workiq_attempts a
join workiq_accounts w on w.family=a.family and w.account_id=a.account_id where a.id=?`, attempt).
		Scan(&revision, &qualification); err != nil {
		return false, err
	}
	if revision != proof.revision || qualification != proof.qualification {
		return false, errors.New("mail staging changed after completion inspection")
	}
	_, err = tx.ExecContext(ctx, `insert into workiq_mail_receipts(attempt_id,sync_run_id,account_id,completed_at,source_data_at,folders,enumeration_key)
values(?,?,?,?,?,?,?)`, attempt, run, account, workIQTimeValue(&summary.FinishedAt), workIQTimeValue(&proof.dataAt), proof.folders, proof.enumeration)
	return err == nil, err
}
