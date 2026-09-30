package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// LastSuccess is when the newest successful sync run finished, or the zero time when the archive
// has never synced. It is cheap, so read commands use it to decide whether to sync first.
func (s *Store) LastSuccess(ctx context.Context) (time.Time, error) {
	var ok sql.NullString
	err := s.db.QueryRowContext(ctx, `select max(coalesce(finished_at, started_at)) from sync_runs where status in `+successStatuses).Scan(&ok)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, err
	}
	return parseTime(ok), nil
}

// MessageKey identifies one archived message.
type MessageKey struct{ TenantID, UserID, ConversationID, ID string }

// MessageHTML returns the stored HTML body of each requested message that exists and has one.
func (s *Store) MessageHTML(ctx context.Context, keys []MessageKey) (map[MessageKey]string, error) {
	out := make(map[MessageKey]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	stmt, err := s.db.PrepareContext(ctx, `select content_html from messages where tenant_id=? and user_id=? and conversation_id=? and id=?`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stmt.Close() }()
	for _, k := range keys {
		var h sql.NullString
		err := stmt.QueryRowContext(ctx, k.TenantID, k.UserID, k.ConversationID, k.ID).Scan(&h)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if h.Valid && strings.TrimSpace(h.String) != "" {
			out[k] = h.String
		}
	}
	return out, nil
}
