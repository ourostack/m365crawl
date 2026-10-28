package store

import (
	"context"
	"database/sql"

	"github.com/ourostack/m365crawl/internal/outlookmail"
)

func fillWorkIQMailChildren(ctx context.Context, tx *sql.Tx, rowid int64, m outlookmail.Message, at any) error {
	var recipients bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from mail_recipients where message_rowid=?)`, rowid).Scan(&recipients); err != nil {
		return err
	}
	have := map[uint32]bool{}
	if err := eachRow(ctx, tx, `select attachment_key from mail_attachments where message_rowid=?`, []any{rowid}, func(row *sql.Rows) error {
		var key uint32
		if err := row.Scan(&key); err != nil {
			return err
		}
		have[key] = true
		return nil
	}); err != nil {
		return err
	}
	changed := !recipients && len(m.Recipients) > 0
	for _, a := range m.Attachments {
		changed = changed || !have[a.Key]
	}
	if err := insertMailChildren(ctx, tx, rowid, m, have, !recipients); err != nil {
		return err
	}
	if changed {
		if _, err := tx.ExecContext(ctx, `update mail_messages set modified_at=? where rowid=?`, at, rowid); err != nil {
			return err
		}
	}
	if len(m.Attachments) > 0 || m.HasAttachments != nil && *m.HasAttachments {
		_, err := tx.ExecContext(ctx, `update mail_messages set has_attachments=1 where rowid=? and has_attachments=0`, rowid)
		return err
	}
	return nil
}
