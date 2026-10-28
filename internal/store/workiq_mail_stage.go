package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

func (s *Store) StageWorkIQMailFolder(ctx context.Context, attempt int64, folder workiq.Folder) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if binding.Fact.Family != "mail" || folder.ID == "" || len(folder.ID) > 64*1024 || folder.ID == folder.ParentID {
		return errors.New("invalid WorkIQ mail folder scope")
	}
	switch folder.WellKnown {
	case "", "other", "inbox", "sent", "deleted", "drafts", "archive", "junk":
	default:
		return errors.New("invalid fixed WorkIQ folder kind")
	}
	raw, err := json.Marshal(folder)
	if err != nil {
		return errors.New("invalid WorkIQ folder projection")
	}
	safe, _ := scrubWorkIQProjection(raw)
	var cleaned workiq.Folder
	if json.Unmarshal(safe, &cleaned) != nil || cleaned.ID != folder.ID || cleaned.ParentID != folder.ParentID {
		return errors.New("WorkIQ folder identity could not be retained")
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		key, err := mailSourceKey(ctx, tx, binding.Fact.ID, "workiq:folder", folder.ID)
		if err != nil {
			return err
		}
		var parent uint32
		if folder.ParentID != "" {
			parent, err = mailSourceKey(ctx, tx, binding.Fact.ID, "workiq:folder", folder.ParentID)
			if err != nil {
				return err
			}
		}
		kind := cleaned.WellKnown
		if kind == "" {
			kind = "other"
		}
		if _, err := tx.ExecContext(ctx, `insert into workiq_folders(account_id,graph_id,folder_key,parent_graph_id,well_known,inventory_attempt,total_items,child_count)
values(?,?,?,?,?,?,?,?) on conflict(account_id,graph_id) do update set parent_graph_id=excluded.parent_graph_id,
well_known=case when excluded.well_known='other' then workiq_folders.well_known else excluded.well_known end,
root_listed=case when workiq_folders.inventory_attempt=excluded.inventory_attempt then workiq_folders.root_listed else 0 end,
inventory_attempt=excluded.inventory_attempt,total_items=excluded.total_items,child_count=excluded.child_count`,
			binding.Fact.ID, cleaned.ID, key, cleaned.ParentID, kind, attempt, cleaned.TotalCount, cleaned.ChildCount); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `select well_known from workiq_folders where account_id=? and graph_id=?`,
			binding.Fact.ID, cleaned.ID).Scan(&kind); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `insert into mail_folders(account,folder_key,parent_key,name,kind,modified_at) values(?,?,?,?,?,?)
on conflict(account,folder_key) do update set parent_key=excluded.parent_key,name=excluded.name,kind=excluded.kind
where parent_key<>excluded.parent_key or name<>excluded.name or kind<>excluded.kind`,
			binding.Fact.ID, key, parent, cleaned.Name, kind, nowText())
		return err
	})
}

func (s *Store) StageWorkIQMailItems(ctx context.Context, attempt int64, folder string, raws []json.RawMessage, observed time.Time) error {
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return err
	}
	if binding.Fact.Family != "mail" || folder == "" || observed.IsZero() {
		return errors.New("invalid WorkIQ mail page scope")
	}
	var items []workiq.MailMessage
	size := 0
	for _, raw := range raws {
		size += len(raw)
		if size > 16<<20 {
			return errors.New("WorkIQ mail page exceeds the page bound")
		}
		m, err := workiq.DecodeMail(ctx, raw)
		if err != nil {
			return err
		}
		if !m.Removed && m.FolderID != folder {
			return errors.New("WorkIQ mail item is outside its folder")
		}
		items = append(items, m)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var key uint32
		if err := tx.QueryRowContext(ctx, `select folder_key from workiq_folders where account_id=? and graph_id=? and inventory_attempt=?`,
			binding.Fact.ID, folder, attempt).Scan(&key); err != nil {
			return err
		}
		var census int64
		var complete bool
		err := tx.QueryRowContext(ctx, `select id,complete from workiq_censuses where attempt_id=? and account_id=?`, attempt, binding.Fact.ID).Scan(&census, &complete)
		if errors.Is(err, sql.ErrNoRows) {
			allocated, err := tx.ExecContext(ctx, `insert into workiq_censuses(attempt_id,account_id) values(?,?)`, attempt, binding.Fact.ID)
			if err != nil {
				return err
			}
			census, err = allocated.LastInsertId()
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if complete {
			return errors.New("WorkIQ mail inventory is already settled")
		}
		for _, m := range items {
			if _, err := tx.ExecContext(ctx, `insert into workiq_census_members(census_id,normalized_id,graph_id,folder_id,sender,sent_at,subject_norm,data_json,observed_at,removed)
values(?,?,?,?,?,?,?,?,?,?) on conflict(census_id,graph_id) do update set
normalized_id=case when excluded.removed=1 then workiq_census_members.normalized_id else excluded.normalized_id end,
folder_id=excluded.folder_id,sender=excluded.sender,sent_at=excluded.sent_at,subject_norm=excluded.subject_norm,
data_json=excluded.data_json,observed_at=excluded.observed_at,removed=excluded.removed`,
				census, workiq.NormalizeMessageID(m.InternetMessageID), m.ID, folder, strings.ToLower(strings.TrimSpace(m.Sender.Address)),
				workIQTimeValue(m.SentAt), strings.ToLower(NormalizeSubject(m.Subject)), string(m.Raw), workIQTimeValue(&observed), b2i(m.Removed)); err != nil {
				return err
			}
		}
		return nil
	})
}
