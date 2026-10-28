package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/ourostack/m365crawl/internal/workiq"
)

type WorkIQChatPageResult struct {
	WorkIQMessageResult
	Complete                                    bool
	Seen, InWindow, OutsideWindow, UnknownClock int
}

func (s *Store) PrepareWorkIQChatPage(ctx context.Context, attempt, pageID int64, rawChat json.RawMessage, limit int) (WorkIQChatPageResult, error) {
	if limit < 1 || limit > 128 {
		return WorkIQChatPageResult{}, errors.New("invalid bounded chat page preparation")
	}
	binding, err := s.validateWorkIQAttemptOwner(ctx, attempt)
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	chat, err := workiq.DecodeChat(ctx, rawChat)
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	if binding.Fact.Family != "chats" || binding.Fact.TenantID != chat.TenantID {
		return WorkIQChatPageResult{}, errors.New("chat page does not match the verified archive tenant")
	}
	var data, observed, resource, window, started, sourceStarted, qualification, frontierQualification string
	var family, account, originalFamily, originalAccount, originalQualification, originalWindow, scope, state string
	var prepared, offset int64
	var complete bool
	if err := s.db.QueryRowContext(ctx, `select p.data_json,p.observed_at,p.prepared_attempt_id,p.prepared_offset,p.prepared_complete,
f.resource,f.family,f.account_id,f.scope,f.qualification,p.state,
o.family,o.account_id,o.qualification,coalesce(o.window_start,''),coalesce(o.window_end,o.started_at),o.started_at,coalesce(a.window_start,''),a.qualification
from workiq_pages p join workiq_frontiers f on f.id=p.frontier_id
join workiq_attempts o on o.id=p.attempt_id join workiq_attempts a on a.id=? where p.id=?`,
		attempt, pageID).Scan(&data, &observed, &prepared, &offset, &complete, &resource, &family, &account, &scope, &frontierQualification,
		&state, &originalFamily, &originalAccount, &originalQualification, &originalWindow, &started, &sourceStarted, &window, &qualification); err != nil {
		return WorkIQChatPageResult{}, err
	}
	// Scope, original owner and qualification are immutable page facts. No page
	// from a different window, tenant or endpoint may become this cursor's work.
	if family != "chats" || originalFamily != family || account != binding.Fact.ID || originalAccount != account ||
		originalQualification != qualification || frontierQualification != qualification || originalWindow != window || scope != window || state != "staged" ||
		resource != "/chats/"+url.PathEscape(chat.ID)+"/messages" {
		return WorkIQChatPageResult{}, errors.New("chat page belongs to another source/window/endpoint")
	}
	begin, err := workIQStoredTime(sql.NullString{String: window, Valid: window != ""})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	end, err := workIQStoredTime(sql.NullString{String: started, Valid: true})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	originalAt, err := workIQStoredTime(sql.NullString{String: sourceStarted, Valid: true})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	at, err := workIQStoredTime(sql.NullString{String: observed, Valid: true})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	if begin == nil || begin.IsZero() || end == nil || end.IsZero() || originalAt == nil || originalAt.IsZero() ||
		end.Before(*begin) || at == nil || at.IsZero() {
		return WorkIQChatPageResult{}, errors.New("chat page has no known original window/observation clock")
	}
	page, err := workiq.DecodePage(ctx, []byte(data))
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	var collection struct {
		Value *[]json.RawMessage `json:"value"`
	}
	if json.Unmarshal(page.Data, &collection) != nil || collection.Value == nil {
		return WorkIQChatPageResult{}, errors.New("chat page has no message collection")
	}
	values := *collection.Value
	headers := make([]workiq.MessageHeader, len(values))
	seen := map[string]bool{}
	for i, raw := range values {
		header, err := workiq.DecodeMessageHeader(ctx, raw)
		if err != nil {
			return WorkIQChatPageResult{}, err
		}
		if seen[header.ID] {
			return WorkIQChatPageResult{}, errors.New("chat page repeats an identity across preparation chunks")
		}
		seen[header.ID] = true
		headers[i] = header
	}
	oldOffset := offset
	if prepared != attempt {
		offset, complete = 0, false
	}
	if offset < 0 || offset > int64(len(values)) || complete && offset != int64(len(values)) {
		return WorkIQChatPageResult{}, errors.New("invalid retained chat preparation offset")
	}
	result := WorkIQChatPageResult{WorkIQMessageResult: WorkIQMessageResult{Gaps: map[string]int{}}}
	if complete {
		result.Complete = true
		return result, nil
	}
	until := min(offset+int64(limit), int64(len(values)))
	var selected []json.RawMessage
	for i := offset; i < until; i++ {
		result.Seen++
		header := headers[i]
		if header.CreatedAt == nil {
			result.UnknownClock++
		} else if header.CreatedAt.Before(*begin) || header.CreatedAt.After(*end) {
			result.OutsideWindow++
		} else {
			result.InWindow++
			selected = append(selected, values[i])
		}
	}
	mapped, qualified, err := prepareWorkIQMessageBatch(ctx, binding, WorkIQMessageBatch{
		AttemptID: attempt, Chat: chat.Raw, Messages: selected, ObservedAt: *at,
	})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	result.WorkIQMessageResult = mapped
	if result.UnknownClock > 0 {
		result.Gaps["workiq_clock_unknown"] += result.UnknownClock
	}
	result.Complete = until == int64(len(values))
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		update, err := tx.ExecContext(ctx, `update workiq_pages set prepared_attempt_id=?,prepared_offset=?,prepared_complete=?
where id=? and state='staged' and prepared_attempt_id=? and prepared_offset=?`,
			attempt, until, b2i(result.Complete), pageID, prepared, oldOffset)
		if err != nil {
			return err
		}
		n, err := update.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("chat page preparation lost its offset revision")
		}
		if len(qualified) > 0 {
			if err := commitWorkIQMessageRecords(ctx, tx, binding.Fact.ID, qualified, *at, &result.WorkIQMessageResult); err != nil {
				return err
			}
		}
		for code, n := range result.Gaps {
			if _, err := tx.ExecContext(ctx, `insert into workiq_gaps(attempt_id,code,resource,n) values(?,?,?,?)
on conflict(attempt_id,code,resource) do update set n=excluded.n`,
				attempt, code, "chat-page:"+strconv.FormatInt(pageID, 10)+":"+strconv.FormatInt(offset, 10), n); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return WorkIQChatPageResult{}, err
	}
	return result, nil
}
