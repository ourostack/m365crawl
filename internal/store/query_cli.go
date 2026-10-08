package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/ourostack/m365crawl/internal/teamsdesktop"
)

// LastSuccess is when the archive was last fully synced, or the zero time when it has never been.
// With several accounts it is the stalest account's time, so one account that no full sync has
// covered makes the whole archive stale. See LastSuccessFor.
func (s *Store) LastSuccess(ctx context.Context) (time.Time, error) {
	return s.lastSuccess(ctx, "", true)
}

// LastSuccessFor is when account ("<tenantId>/<userId>") was last covered by a fully successful
// sync (ok, ok_with_omissions or unchanged), or the zero time. Each sync records one run-level
// row (accounts_json set) that says which accounts it covered: every account for an unfiltered
// sync, one for `sync --account`. A partial or failed sync records a row that counts for nobody,
// so an account whose source failed stays stale. Per-source rows do not count.
func (s *Store) LastSuccessFor(ctx context.Context, account string) (time.Time, error) {
	return s.lastSuccess(ctx, account, false)
}

func (s *Store) lastSuccess(ctx context.Context, account string, stalest bool) (time.Time, error) {
	if old, err := s.NeedsUpgrade(ctx); err != nil || old {
		return time.Time{}, err // no complete per-account run is recorded yet
	}
	const newest = `max(coalesce(finished_at, started_at))`
	const covering = `status in ` + successStatuses + ` and accounts_json is not null`
	q, args := `select `+newest+` from sync_runs where `+covering+`
  and exists(select 1 from json_each(accounts_json) where value in ('*', ?))`, []any{account}
	if stalest {
		// The oldest of every account's newest covering run; an account no run covered counts as
		// never synced (the empty string). With no accounts in the archive, any full run will do.
		q, args = `select case when count(a.user_id) = 0 then (select `+newest+` from sync_runs where `+covering+`)
  else min(coalesce((select `+newest+` from sync_runs r where `+covering+`
    and exists(select 1 from json_each(r.accounts_json) where value in ('*', a.tenant_id || '/' || a.user_id))), '')) end
from (select 1) left join accounts a`, nil
	}
	var ok sql.NullString
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&ok); err != nil {
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

// MessageWindow is the sent time of the oldest and newest archived message, of one account when a
// is set; both are zero when the archive holds none.
func (s *Store) MessageWindow(ctx context.Context, a *teamsdesktop.Account) (oldest, newest time.Time, err error) {
	q, args := `select min(sent_at), max(sent_at) from messages`, []any{}
	if a != nil {
		q, args = q+` where tenant_id=? and user_id=?`, []any{a.TenantID, a.UserID}
	}
	var lo, hi sql.NullString
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&lo, &hi); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return parseTime(lo), parseTime(hi), nil
}
