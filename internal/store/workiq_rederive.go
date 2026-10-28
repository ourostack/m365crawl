package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// Rederivation changes the cache shadow, not the fact or time of a source sighting.
func rederiveWorkIQMessages(ctx context.Context, tx *sql.Tx) (int, error) {
	total, after := 0, int64(0)
	for {
		rows, err := tx.QueryContext(ctx, `select w.item_rowid,w.cache_projection,m.updated_at
from workiq_rows w join messages m on w.account_id=m.tenant_id||'/'||m.user_id and w.item_rowid=m.rowid
where w.kind='message' and w.cache_seen=1 and w.cache_projection is not null and w.item_rowid>?
order by w.item_rowid limit ?`, after, rederiveBatch)
		if err != nil {
			return 0, err
		}
		type entry struct {
			rowid               int64
			projection, updated string
		}
		var batch []entry
		for rows.Next() {
			var item entry
			if err := rows.Scan(&item.rowid, &item.projection, &item.updated); err != nil {
				_ = rows.Close()
				return 0, err
			}
			batch = append(batch, item)
		}
		err = rows.Err()
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return 0, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		after = batch[len(batch)-1].rowid
		for _, item := range batch {
			var cache teamsdesktop.Message
			if json.Unmarshal([]byte(item.projection), &cache) != nil {
				return 0, errors.New("invalid cache shadow during message rederive")
			}
			text, sender, err := teamsdesktop.DeriveMessage(cache.Raw)
			if err != nil {
				continue
			} // Keep an unreadable native copy, as ordinary rederive does.
			if text == cache.ContentText && sender == cache.SenderName {
				continue
			}
			cache.ContentText, cache.SenderName = text, sender
			// Recompute the retained content projection, not a fresh tombstone observation.
			// Its independent original deletion/version stays in workiq_rows and is reduced.
			cache.DeletedAt = time.Time{}
			source, err := applyWorkIQCacheMessage(ctx, tx, cache, time.Time{})
			if err != nil {
				return 0, err
			}
			source.PublishApps = false
			n, _, _, err := applyMessageSources(ctx, tx, []teamsdesktop.Message{source.Message}, map[string]workIQCacheMessageResult{cache.ID: source})
			if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `update messages set updated_at=? where rowid=?`, item.updated, item.rowid); err != nil {
				return 0, err
			}
			total += n.Updated
		}
	}
}
