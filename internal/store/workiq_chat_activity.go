package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ourostack/m365crawl/internal/workiq"
)

// WorkIQChatActivity reads archived activity, never an unqualified server
// preview. An empty result is unknown activity, not grounds to omit the chat.
func (s *Store) WorkIQChatActivity(ctx context.Context, account workiq.AccountFact, chat string) (time.Time, error) {
	if account.Family != "chats" || account.TenantID == "" || account.UserID == "" ||
		account.ID != account.TenantID+"/"+account.UserID || chat == "" {
		return time.Time{}, errors.New("invalid archived chat activity scope")
	}
	var known bool
	if err := s.db.QueryRowContext(ctx, `select exists(select 1 from accounts where tenant_id=? and user_id=?)`,
		account.TenantID, account.UserID).Scan(&known); err != nil {
		return time.Time{}, err
	}
	if !known {
		return time.Time{}, errors.New("chat activity owner is not archived")
	}
	var last sql.NullString
	if err := s.db.QueryRowContext(ctx, `select max(clock) from (
select last_message_at as clock from conversations where tenant_id=? and user_id=? and id=?
union all select max(sent_at) from messages where tenant_id=? and user_id=? and conversation_id=?)`,
		account.TenantID, account.UserID, chat, account.TenantID, account.UserID, chat).Scan(&last); err != nil {
		return time.Time{}, err
	}
	at, err := workIQStoredTime(last)
	if err != nil {
		return time.Time{}, err
	}
	if at == nil {
		return time.Time{}, nil
	}
	return *at, nil
}
