package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) CarryWorkIQMailInventory(ctx context.Context, attempt, after int64, limit int) (int64, bool, error) {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return 0, false, err
	}
	if binding.Fact.Family != "mail" || after < 0 || limit < 1 || limit > 128 {
		return 0, false, errors.New("invalid retained mail inventory scope or bound")
	}
	next, done := after, false
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var census, stored int64
		var ready, full, complete bool
		if err := tx.QueryRowContext(ctx, `select id,inventory_ready,full_enumeration,carry_complete,carried_through
from workiq_censuses where attempt_id=? and account_id=?`, attempt, binding.Fact.ID).Scan(&census, &ready, &full, &complete, &stored); err != nil {
			return err
		}
		if !ready {
			return errors.New("retained inventory has no prepared folder proof")
		}
		if full || complete {
			next, done = stored, true
			return nil
		}
		if after != stored {
			return errors.New("retained inventory progress changed before preparation")
		}
		type entry struct {
			id                                              int64
			graph, raw                                      string
			seen, gone, deleted, liveVersion, deleteVersion sql.NullString
		}
		var entries []entry
		if err := eachRow(ctx, tx, `select item_rowid,graph_id,server_projection,server_seen_at,server_gone_at,server_deleted_at,server_version,server_deleted_version
from workiq_rows where kind='mail' and account_id=? and server_seen=1 and identity_unproven=0 and item_rowid>? order by item_rowid limit ?`,
			[]any{binding.Fact.ID, after, limit}, func(row *sql.Rows) error {
				var item entry
				if err := row.Scan(&item.id, &item.graph, &item.raw, &item.seen, &item.gone, &item.deleted, &item.liveVersion, &item.deleteVersion); err != nil {
					return err
				}
				entries = append(entries, item)
				return nil
			}); err != nil {
			return err
		}
		for _, item := range entries {
			next = item.id
			var in WorkIQSourceState
			in.Seen = true
			in.DeletedAt, err = workIQStoredTime(item.deleted)
			if err != nil {
				return err
			}
			in.VersionAt, err = workIQStoredTime(item.liveVersion)
			if err != nil {
				return err
			}
			in.DeletedVersionAt, err = workIQStoredTime(item.deleteVersion)
			if err != nil {
				return err
			}
			observed, err := workIQStoredTime(item.seen)
			if err != nil {
				return err
			}
			if item.gone.Valid || observed == nil || workIQOwnDeletion(in) != nil {
				continue
			}
			var exists bool
			if err := tx.QueryRowContext(ctx, `select exists(select 1 from workiq_census_members where census_id=? and graph_id=?)`, census, item.graph).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			} // Includes explicit @removed; never bring it back as retained live.
			var mail workiq.MailMessage
			if json.Unmarshal([]byte(item.raw), &mail) != nil || mail.ID != item.graph || len(mail.Raw) == 0 {
				return errors.New("invalid retained server mail identity")
			}
			normalized := workiq.NormalizeMessageID(mail.InternetMessageID)
			if normalized == "" {
				if _, err := tx.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,resource,n) values(?,'workiq_mail_message_id_missing',?,1)
on conflict(attempt_id,code,resource) do nothing`, attempt, "mail-retained:"+hashOf(item.graph)); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `insert into workiq_census_members(census_id,normalized_id,graph_id,folder_id,sender,sent_at,subject_norm,
data_json,observed_at,admitted,retained) values(?,?,?,?,?,?,?,?,?,1,1)`,
				census, normalized, item.graph, mail.FolderID, strings.ToLower(strings.TrimSpace(mail.Sender.Address)),
				workIQTimeValue(mail.SentAt), strings.ToLower(NormalizeSubject(mail.Subject)), string(mail.Raw), workIQTimeValue(observed)); err != nil {
				return err
			}
		}
		done = len(entries) < limit
		_, err := tx.ExecContext(ctx, `update workiq_censuses set carried_through=?,carry_complete=?,complete=? where id=? and carried_through=?`,
			next, b2i(done), b2i(done), census, stored)
		return err
	})
	if err != nil {
		return 0, false, err
	}
	return next, done, nil
}
