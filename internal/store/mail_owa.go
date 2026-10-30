package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/ourostack/m365crawl/internal/outlookowa"
)

// OWAMailBatch is one Windows Outlook account's mail from a validated source snapshot.
type OWAMailBatch struct {
	Account         string
	ReadAt, FreshAt time.Time
	Result          outlookowa.Result
	// Trusted requires a stable snapshot; decoder losses or doubt independently prevent absence.
	Trusted bool
	// Identity, when set, is the mailbox identity bound to Account in this transaction within
	// Scope, the profile's own account. Rebind replaces an existing binding the caller proved
	// belongs to the same mailbox.
	Identity, Scope string
	Rebind          bool
}

// CommitOWAMail assigns native identities and writes mail in the same transaction.
func (s *Store) CommitOWAMail(ctx context.Context, b OWAMailBatch) (MailResult, error) {
	var res MailResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := bindOWAMailbox(ctx, tx, b); err != nil {
			return err
		}
		r, err := b.Result.Normalize(func(kind, sourceID string) (uint32, error) {
			return mailSourceKey(ctx, tx, b.Account, kind, sourceID)
		})
		if err != nil {
			return err
		}
		res, err = commitMail(ctx, tx, MailBatch{
			Account: b.Account, ReadAt: b.ReadAt, FreshAt: b.FreshAt, Result: r,
			Trusted:            b.Trusted && !r.Doubtful && len(r.Losses) == 0,
			RekeyAcrossFolders: true,
		})
		return err
	})
	if err != nil {
		return MailResult{}, err
	}
	return res, nil
}
